package runner

import (
	"github.com/yug/upi-city/internal/detect"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/risk"
	"github.com/yug/upi-city/internal/truth"
)

// Labels resolves ground truth for one transaction.
//
// ─── Why this is an interface ───────────────────────────────────────────────
//
// The simulator knows the truth because it created it. An external dataset
// knows the truth because a payment processor recorded a chargeback months
// later. Those are completely different provenance, and neither is more
// authoritative than the other for the purpose of grading a detector — so the
// scorer takes truth as a dependency rather than reaching into a world.
//
// This is what lets real, externally-labelled payments be scored by *exactly*
// the code that scores simulated ones. A second scoring loop written for the
// real-data path would be the obvious thing to build and would quietly destroy
// the comparison: any divergence in warmup, fusion, ordering or thresholding
// would show up as a difference between real and simulated performance, and
// would be indistinguishable from a real finding.
//
// ─── What it is NOT ─────────────────────────────────────────────────────────
//
// It is not visible to detection. Scorer.Observe hands obs.Event to the
// pipeline and consults Labels only afterwards, to attach an answer to a score
// that has already been computed. The compiler enforces the same separation
// one level down: internal/detect cannot import internal/truth at all, which
// the firewall test in that package checks against the real dependency graph.
type Labels interface {
	Label(obs.TxID) truth.Label
	Incident(obs.TxID) truth.IncidentID
	Archetype(obs.AgentID) truth.Archetype
	// Intensity is how far an attack had ramped at the tick a payment was
	// created. Real data has no such notion and returns 0.
	Intensity(obs.Tick) float64
}

// ScoreConfig configures a Scorer.
type ScoreConfig struct {
	Pipeline   *detect.Pipeline
	Fusion     risk.Fusion
	Calibrator *risk.Calibrator
	Propagator *risk.Propagator
	// PropagateEvery is how often the propagation pass runs, in ticks. Zero
	// means the default of 100.
	PropagateEvery obs.Tick

	// AgentCap is one past the highest agent id that can appear. Per-agent
	// state is indexed directly by id, so this sizes it.
	AgentCap int

	// Optional recorders. Events and labels go to SEPARATE files; see
	// record.LabelRow for why that separation is structural rather than tidy.
	EventW   *record.Writer
	FindingW *record.Writer
	LabelW   *record.Writer
}

// Scorer runs the detection pipeline over an event stream and joins each score
// against ground truth.
//
// The caller owns iteration. The simulator drives ticks and drains a world;
// the real-data path walks a sorted file. Sharing the loop as well as the body
// would have meant forcing both into one iteration model for no benefit — what
// has to be identical is the scoring, and that is what lives here.
type Scorer struct {
	pipe     *detect.Pipeline
	fuse     risk.Fusion
	cal      *risk.Calibrator
	prop     *risk.Propagator
	propTick obs.Tick

	eventW, findingW, labelW *record.Writer

	rows metrics.Rows
	peak []float64

	// agentRisk carries per-agent suspicion between propagation passes;
	// propagated holds the most recent pass's output.
	agentRisk  []float64
	propagated []float64
	lastProp   obs.Tick

	hash     obs.StreamHash
	buf      []detect.Finding
	events   int
	findings int
}

// NewScorer builds a scorer. The pipeline must already be configured.
func NewScorer(c ScoreConfig) *Scorer {
	propEvery := c.PropagateEvery
	if propEvery == 0 {
		propEvery = 100
	}
	return &Scorer{
		pipe:     c.Pipeline,
		fuse:     c.Fusion,
		cal:      c.Calibrator,
		prop:     c.Propagator,
		propTick: propEvery,
		eventW:   c.EventW, findingW: c.FindingW, labelW: c.LabelW,
		peak:       make([]float64, c.AgentCap),
		agentRisk:  make([]float64, c.AgentCap),
		propagated: make([]float64, c.AgentCap),
		hash:       obs.NewStreamHash(),
		buf:        make([]detect.Finding, 0, 16),
		rows:       make(metrics.Rows, 0, c.AgentCap*40),
	}
}

// Observe scores one settled transaction and records the resulting row.
//
// Events must arrive in settlement order: the detectors are online and treat
// SettleTick as "now", so feeding them out of order silently changes what they
// see. The simulator emits in order by construction; a file-backed source has
// to sort first.
func (s *Scorer) Observe(e obs.Event, lb Labels) error {
	s.hash = s.hash.Add(e)
	s.events++
	if s.eventW != nil {
		if err := s.eventW.Write(record.RowOf(e)); err != nil {
			return err
		}
	}

	// Detection sees only the observable event.
	s.buf = s.pipe.Observe(e, s.buf[:0])

	// Fuse the detectors' findings into one score for this transaction,
	// keeping each detector's contribution.
	var bestDet string
	var bestRaw float64
	for _, f := range s.buf {
		s.findings++
		if int(f.Agent) < len(s.peak) && f.Score > s.peak[f.Agent] {
			s.peak[f.Agent] = f.Score
		}
		if f.Score > bestRaw {
			bestRaw, bestDet = f.Score, f.Detector
		}
		if s.findingW != nil {
			if err := s.findingW.Write(f); err != nil {
				return err
			}
		}
	}
	rs := s.fuse.ScoreWith(s.buf)
	score := rs.Raw

	if s.prop != nil {
		s.prop.Observe(e)
		if int(e.From) < len(s.agentRisk) && rs.Raw > s.agentRisk[e.From] {
			s.agentRisk[e.From] = rs.Raw
		}
		// The pass is periodic, not per-transaction: running a two-round graph
		// walk on every payment would dominate the loop and buy nothing, since
		// the graph barely moves between consecutive transactions.
		if e.SettleTick-s.lastProp >= s.propTick {
			s.propagated = s.prop.Adjust(s.agentRisk, e.SettleTick)
			s.lastProp = e.SettleTick
		}
		if int(e.From) < len(s.propagated) && s.propagated[e.From] > score {
			score = s.propagated[e.From]
		}
	}

	if s.cal != nil {
		score = s.cal.Apply(score)
	}

	// Attack strength at the moment the payment was initiated, not when it
	// settled — the ramp is a property of the attacker.
	intensity := lb.Intensity(e.Tick)

	if s.labelW != nil {
		if err := s.labelW.Write(record.LabelRow{
			TxID:      e.TxID,
			Label:     lb.Label(e.TxID),
			Incident:  lb.Incident(e.TxID),
			Archetype: lb.Archetype(e.From),
			Intensity: intensity,
		}); err != nil {
			return err
		}
	}

	// EVERY transaction becomes a row, including the vast majority nothing
	// flagged. Evaluating only flagged transactions would make recall
	// unmeasurable and precision meaningless.
	s.rows = append(s.rows, metrics.ScoredRow{
		TxID:          e.TxID,
		Tick:          e.SettleTick,
		Intensity:     intensity,
		From:          e.From,
		To:            e.To,
		AmountP:       e.AmountP,
		Failed:        e.Status != obs.StatusSuccess,
		Score:         score,
		Raw:           rs.Raw,
		Detector:      bestDet,
		Contributions: rs.Contributions,
		Label:         lb.Label(e.TxID),
		Archetype:     lb.Archetype(e.From),
		Incident:      lb.Incident(e.TxID),
	})
	return nil
}

// Rows returns the scored transactions, in settlement order.
func (s *Scorer) Rows() metrics.Rows { return s.rows }

// Peak is the highest score any detector gave each agent, indexed by agent id.
func (s *Scorer) Peak() []float64 { return s.peak }

// Hash is the running stream hash over every event observed. Two runs that
// agree here saw byte-identical traffic.
func (s *Scorer) Hash() obs.StreamHash { return s.hash }

// Events returns how many transactions were scored.
func (s *Scorer) Events() int { return s.events }

// Findings returns how many findings the detectors raised in total.
func (s *Scorer) Findings() int { return s.findings }
