package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/decide"
	"github.com/yug/upi-city/internal/detect"
	"github.com/yug/upi-city/internal/explain"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/risk"
	"github.com/yug/upi-city/internal/sim"
	"github.com/yug/upi-city/internal/truth"
)

//go:embed web
var webFS embed.FS

// Server runs one live world and streams it to any number of browsers.
type Server struct {
	cfg  sim.Config
	ccfg chaos.Config
	src  Source
	pipe *detect.Pipeline
	fuse risk.Fusion

	mu      sync.Mutex
	clients map[chan []byte]bool

	// cmds carries control messages into the simulation goroutine, so the
	// world is still only ever touched by one goroutine. Mutating it from an
	// HTTP handler would race the tick loop.
	cmds chan Command

	// Live state, guarded by mu.
	tau      float64
	tauBlock float64
	speed    float64
	paused   bool
	dropped  int
	live     Live

	// explain produces the audit-trail narrative. It is consulted only for
	// display and only after a decision has been made; it can never delay a
	// tick or influence a score.
	explain  *explain.Layer
	incStats map[truth.IncidentID]*incidentStats

	// narratives holds the most recent finished explanation per incident,
	// keyed by incident rather than by facts — a running incident's facts
	// change every tick, so a fact-keyed lookup would never hit.
	narratives  map[truth.IncidentID]explain.Explanation
	narrativeAt map[truth.IncidentID]time.Time
}

// New builds a server around a live world.
func New(cfg sim.Config, ccfg chaos.Config, detectors []string) *Server {
	return NewWithSource(NewLiveSource(cfg), cfg, ccfg, detectors)
}

// NewWithSource builds a server around any event source — a live world or a
// recorded run. Everything downstream is identical, which is what makes a
// replay indistinguishable from the real thing.
func NewWithSource(src Source, cfg sim.Config, ccfg chaos.Config, detectors []string) *Server {
	s := &Server{
		cfg:         cfg,
		ccfg:        ccfg,
		src:         src,
		pipe:        detect.NewNamed(detectors),
		fuse:        risk.DefaultFusion(),
		clients:     map[chan []byte]bool{},
		cmds:        make(chan Command, 32),
		tau:         0.15,
		tauBlock:    0.55,
		speed:       1,
		incStats:    map[truth.IncidentID]*incidentStats{},
		narratives:  map[truth.IncidentID]explain.Explanation{},
		narrativeAt: map[truth.IncidentID]time.Time{},
	}
	// Naming the challenger here is what switches the head-to-head view on.
	// A recording with no scores-*.jsonl beside it leaves this empty and the
	// interface shows the single-detector view unchanged.
	s.live.Rival = src.RivalName()
	if src.Live() {
		s.prewarm()
	}
	return s
}

// SetExplainer attaches the narrative layer and starts draining its results.
func (s *Server) SetExplainer(l *explain.Layer) {
	s.explain = l
	go s.collectNarratives()
}

// prewarm runs the world past the detectors' warmup before anyone connects.
//
// Detectors deliberately raise nothing until they have enough history to
// judge a deviation against — at the start of a run every counterparty is new
// and every baseline is empty, so scoring then would measure ignorance rather
// than detection. That is right, but it means the first ~3,000 ticks produce
// no findings at all: a fraud ring injected during that window is invisible,
// and the live panel sits at zero while the audience watches.
//
// So the warmup is served before the page opens. Nothing is hidden — the tick
// counter starts where it really is, and the interface reports it — but the
// detectors are ready the moment anyone is looking.
func (s *Server) prewarm() {
	buf := make([]detect.Finding, 0, 16)
	target := s.pipe.Warmup() + 600
	for s.src.Now() < target {
		for _, e := range s.src.Step() {
			buf = s.pipe.Observe(e, buf[:0])
		}
	}
	// Counters start clean: warmup traffic is history, not results.
	s.live = Live{}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/control", s.handleControl)
	return mux
}

// handleStream is one browser's event stream.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Buffered: the simulation must never block waiting for a browser.
	ch := make(chan []byte, 8)
	s.mu.Lock()
	s.clients[ch] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()

	// Send the layout immediately so the client can draw before any activity.
	if b, err := json.Marshal(s.snapshot()); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	var c Command
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	select {
	case s.cmds <- c:
	default: // control queue full; dropping is better than blocking the UI
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) snapshot() Snapshot {
	return Snapshot{Kind: KindSnapshot, Tick: s.src.Now(), Nodes: s.src.Nodes()}
}

// broadcast pushes a frame to every client, dropping it for any client that
// is not keeping up.
//
// This is the rule that keeps a slow browser from stalling the simulation:
// the send is non-blocking, and a dropped frame is counted and surfaced in the
// interface rather than silently swallowed.
func (s *Server) broadcast(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- b:
		default:
			s.dropped++
		}
	}
}

// Run drives the world forever, emitting a delta at a fixed frame rate.
func (s *Server) Run(ticksPerSecond float64, fps float64) {
	frame := time.NewTicker(time.Duration(float64(time.Second) / fps))
	defer frame.Stop()

	var (
		pending    []Tx
		flags      []Flag
		buf        = make([]detect.Finding, 0, 16)
		lastReport = time.Now()
		sinceTx    int
		accum      float64
	)

	for range frame.C {
		// Apply any pending control commands first, on this goroutine.
		for {
			select {
			case c := <-s.cmds:
				s.apply(c)
				continue
			default:
			}
			break
		}

		s.mu.Lock()
		paused, speed, tau, tauBlock := s.paused, s.speed, s.tau, s.tauBlock
		// Hold the challenger to the same operational load: whatever share of
		// traffic this project is flagging at tau, give the challenger a
		// threshold that flags the same share. Recomputed once per frame from
		// the run so far rather than per transaction, so the cut is stable for
		// the whole frame and every payment in it is judged the same way.
		if s.live.Rival != "" && s.live.Settled > 0 {
			rate := float64(s.live.TP+s.live.FP) / float64(s.live.Settled)
			s.live.RivalTau = s.src.RivalTauForRate(rate)
		}
		s.mu.Unlock()

		if !paused {
			accum += ticksPerSecond * speed / fps
			steps := int(accum)
			accum -= float64(steps)
			if steps > 400 {
				steps = 400 // never let a stall become an unbounded catch-up
			}

			for i := 0; i < steps; i++ {
				if s.src.Done() {
					// A replay that has run out stops advancing rather than
					// looping or pretending: the panel freezes on the final
					// state, which is what a finished recording actually is.
					break
				}
				for _, e := range s.src.Step() {
					buf = pipe(s.pipe, e, buf)
					rs := s.fuse.ScoreWith(buf)

					lbl := s.src.Label(e.TxID)
					rival := s.src.Rival(e.TxID)
					pending = append(pending, Tx{
						int64(e.From), int64(e.To), e.AmountP,
						int64(e.Status), int64(rs.Raw * 1000), int64(lbl),
						int64(rival * 1000),
					})
					if rs.Raw >= tau {
						flags = append(flags, Flag{int64(e.From), int64(rs.Raw * 1000)})
					}
					s.score(rs.Raw, tau, tauBlock, lbl)
					s.scoreRival(rs.Raw, rival, tau, lbl)
					s.trackIncident(e, rs, tau, lbl)
					sinceTx++
				}
			}
		}

		// Cap the frame so one slow frame cannot produce a huge payload.
		if len(pending) > 900 {
			pending = pending[len(pending)-900:]
		}

		now := time.Now()
		if dt := now.Sub(lastReport).Seconds(); dt >= 0.5 {
			s.mu.Lock()
			s.live.TPS = float64(sinceTx) / dt
			s.mu.Unlock()
			sinceTx, lastReport = 0, now
		}

		d := Delta{
			Kind: KindDelta, Tick: s.src.Now(),
			Tx: pending, Flags: flags,
			Live: s.liveState(tau, speed, paused), Incidents: s.incidents(),
		}
		if b, err := json.Marshal(d); err == nil {
			s.broadcast(b)
		}
		pending, flags = pending[:0], flags[:0]
	}
}

// trackIncident accumulates the figures an audit-trail narrative needs.
//
// Kept here rather than derived later because a live run never produces a
// completed metrics report — there is no end of run to compute from.
func (s *Server) trackIncident(e obs.Event, rs risk.RiskScore, tau float64, lbl truth.Label) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, inc := range s.src.Incidents() {
		st := s.incStats[inc.ID]
		if st == nil {
			st = &incidentStats{contrib: map[string]float64{}}
			s.incStats[inc.ID] = st
		}

		if inc.Kind == "bank-outage" {
			if e.SettleTick < inc.StartTick {
				continue
			}
			st.txInWindow++
			if e.Status != obs.StatusSuccess {
				st.failed++
			}
			if !lbl.Fraudulent() && rs.Raw >= tau {
				st.falseAlarms++
			}
			continue
		}

		if s.src.IncidentOf(e.TxID) != inc.ID {
			continue
		}
		st.tx++
		st.totalP += e.AmountP
		if len(st.amounts) < 20000 {
			st.amounts = append(st.amounts, e.AmountP)
		}
		if rs.Raw >= tau {
			st.flagged++
			if st.firstFlagTick == 0 {
				st.firstFlagTick = e.SettleTick
			}
		}
		for k, v := range rs.Contributions {
			st.contrib[k] += v
		}
	}
}

func pipe(p *detect.Pipeline, e obs.Event, buf []detect.Finding) []detect.Finding {
	return p.Observe(e, buf[:0])
}

// score updates the running confusion matrix and the decision counters.
//
// The comparison against ground truth happens HERE, on the server, after
// detection has already produced its answer. Nothing computed in this function
// can reach a detector.
// scoreRival keeps the head-to-head tally: for each payment, which of the two
// detectors flagged it, judged against the same threshold and the same truth.
//
// ─── Why one threshold for both ─────────────────────────────────────────────
//
// Comparing detectors at thresholds chosen separately compares two policies as
// much as two detectors, and the difference cannot be attributed. Holding tau
// fixed makes the comparison about the scores, which is what is actually being
// claimed on screen. It is not entirely fair to a challenger whose scores are
// distributed differently — and that is precisely why the leaderboard reports
// AUC-PR, which no threshold can flatter, alongside this view.
//
// The challenger's threshold is not tau. It is whatever score makes it flag
// the same SHARE of traffic that tau makes this project flag, recomputed as
// the run proceeds — see Source.RivalTauForRate for why comparing two
// independently designed score scales at one number is meaningless.
//
// BothMissed is the one that matters. Fraud neither detector reached is
// invisible in any per-detector statistic, because each one is only ever
// measured against what IT caught.
func (s *Server) scoreRival(mine, rival, tau float64, lbl truth.Label) {
	if s.live.Rival == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	mineHit, rivalHit := mine >= tau, rival >= s.live.RivalTau
	if !lbl.Fraudulent() {
		if rivalHit {
			s.live.RivalFP++
		}
		return
	}
	switch {
	case mineHit && rivalHit:
		s.live.BothCaught++
	case mineHit:
		s.live.MineOnly++
	case rivalHit:
		s.live.RivalOnly++
	default:
		s.live.BothMissed++
	}
}

func (s *Server) score(raw, tau, tauBlock float64, lbl truth.Label) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live.Settled++
	fraud := lbl.Fraudulent()
	if fraud {
		s.live.Fraudulent++
	}
	switch {
	case raw >= tau && fraud:
		s.live.TP++
	case raw >= tau && !fraud:
		s.live.FP++
	case raw < tau && fraud:
		s.live.FN++
	}

	policy := decide.Policy{TauReview: tau, TauBlock: tauBlock}
	switch policy.Decide(raw) {
	case decide.Block:
		s.live.Blocked++
		if fraud {
			s.live.FraudBlocked++
		} else {
			s.live.FalseBlocked++
		}
	case decide.Review:
		s.live.Reviewed++
		if fraud {
			s.live.FraudReviewed++
		}
	}
}

func (s *Server) liveState(tau, speed float64, paused bool) Live {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.live
	l.Tick = s.src.Now()
	l.Threshold, l.Speed, l.Paused, l.Dropped = tau, speed, paused, s.dropped
	if l.TP+l.FP > 0 {
		l.Precision = float64(l.TP) / float64(l.TP+l.FP)
	}
	if l.TP+l.FN > 0 {
		l.Recall = float64(l.TP) / float64(l.TP+l.FN)
	}
	if legit := l.Settled - l.Fraudulent; legit > 0 {
		l.FPPer1k = 1000 * float64(l.FP) / float64(legit)
	}
	l.Replay = !s.src.Live()
	l.Finished = s.src.Done()
	l.TauBlock = s.tauBlock
	if l.Blocked > 0 {
		l.BlockPrecision = float64(l.FraudBlocked) / float64(l.Blocked)
	}
	if l.Settled > 0 {
		l.ReviewRate = float64(l.Reviewed) / float64(l.Settled)
	}
	return l
}

func (s *Server) incidents() []Incident {
	var out []Incident
	for _, in := range s.src.Incidents() {
		i := Incident{
			ID: uint32(in.ID), Kind: in.Kind,
			StartTick: uint64(in.StartTick), Members: len(in.Members),
		}
		if ls, ok := s.src.(*liveSource); ok {
			if sc := ls.World().Scenario(); sc != nil && sc.Name() == in.Kind {
				i.Intensity = sc.Intensity(s.src.Now())
			}
		}
		if s.explain != nil {
			e := s.narrative(in)
			i.Headline, i.Narrative, i.Action, i.Source =
				e.Headline, e.Narrative, e.Action, string(e.Source)
		}
		out = append(out, i)
	}
	return out
}

func (s *Server) apply(c Command) {
	s.mu.Lock()
	switch c.Action {
	case "pause":
		s.paused = true
	case "resume":
		s.paused = false
	case "speed":
		s.speed = clamp(c.Value, 0.1, 20)
	case "blockThreshold":
		// A block threshold below the review threshold would stop payments
		// that were never suspicious enough to look at.
		s.tauBlock = clamp(c.Value, s.tau, 1)
		s.live.Blocked, s.live.Reviewed = 0, 0
		s.live.FraudBlocked, s.live.FraudReviewed, s.live.FalseBlocked = 0, 0, 0
	case "threshold":
		s.tau = clamp(c.Value, 0, 1)
		if s.tauBlock < s.tau {
			s.tauBlock = s.tau
		}
		// Re-thresholding invalidates the running confusion matrix: the
		// counters were accumulated under the old cut. Reset rather than
		// present a mixture of two policies as one number.
		s.live.TP, s.live.FP, s.live.FN = 0, 0, 0
		s.live.Settled, s.live.Fraudulent = 0, 0
		s.live.Blocked, s.live.Reviewed = 0, 0
		s.live.FraudBlocked, s.live.FraudReviewed, s.live.FalseBlocked = 0, 0, 0
		// The head-to-head tally is thresholded too, so it is stale for the
		// same reason and reset for the same reason.
		s.live.BothCaught, s.live.MineOnly = 0, 0
		s.live.RivalOnly, s.live.BothMissed, s.live.RivalFP = 0, 0, 0
		// The matched threshold is derived from the flag rate that was just
		// reset, so it has to go too or the first frames after a drag would
		// judge the challenger by the old policy's budget.
		s.live.RivalTau = 2
	}
	s.mu.Unlock()

	if c.Action == "inject" {
		cfg := s.ccfg
		if c.RingSize > 0 {
			cfg.RingSize = c.RingSize
		}
		if c.Victims > 0 {
			cfg.Victims = c.Victims
		}
		if c.RampTicks > 0 {
			cfg.RampTicks = obs.Tick(c.RampTicks)
		}
		// A replay refuses this, which is correct: its future is already
		// recorded and accepting an injection would make the interface claim
		// something the data does not contain.
		_ = s.src.Inject(c.Scenario, cfg)
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
