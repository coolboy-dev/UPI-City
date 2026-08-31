package detect

import "github.com/yug/upi-city/internal/obs"

// Fanout scores concentration of NEW counterparties.
//
// Counterparty count alone is not a fraud signal — that is the whole point of
// the hard negatives. A large merchant is paid by hundreds of people; a
// payroll company pays a hundred at once. Both would be flagged by any
// detector keyed on degree.
//
// What separates them from a collection mule is NOVELTY. The merchant's
// payers recur, month after month. The payroll roster is the same roster
// every cycle. A mule's payers are accounts that have never paid it before,
// because the ring only just recruited them.
//
// Scored on both directions, since the two fraud shapes are mirror images:
// fan-IN identifies a collection point, fan-OUT identifies a disbursal point.
//
// ─── Why there is more than one window ──────────────────────────────────────
//
// This detector originally ran a single 400-tick window and earned nothing:
// AUC-PR 0.017 on the ring, exactly the prevalence floor. It was kept in the
// report as a visible failure rather than deleted.
//
// The cause was found by measuring the fan-in profile of a scam collection
// account directly, and it was not a coding error — the detector was looking
// at the right edge, in the right direction. It was looking over the wrong
// TIMESCALE. Those accounts each drew ~20 distinct never-seen-before payers,
// which is exactly the shape this detector exists to find, but spread across
// ~6,000 ticks. Inside any 400-tick window the count peaked at 4-5, against a
// minPeers floor of 14. The signal was real and the detector was blind to it:
//
//	span=  400   max new payers in window =  4    below minPeers
//	span= 1000   max new payers in window =  9    below minPeers
//	span= 4000   max new payers in window = 17    fires
//
// So the windows are now multi-scale and the score is the strongest across
// them. A single fixed window can only ever see campaigns that match its own
// duration, which is an arbitrary thing for a detector to be sensitive to.
//
// ─── The cost, which is not zero ────────────────────────────────────────────
//
// A long window is exactly where the hard negatives get dangerous. A large
// merchant accumulates novel payers indefinitely; given 4,000 ticks it will
// find plenty. The novelty RATIO guard is what has to hold the line, because
// degree alone no longer can. Whether it does is measured, not assumed — see
// the per-archetype false-positive breakdown in the report.
type Fanout struct {
	in  []*side
	out []*side

	// spans are the observation windows, shortest first. Multi-scale because
	// campaign duration is not known in advance and a single window silently
	// picks one.
	spans    []obs.Tick
	minPeers int
	// minRatio is the share of counterparties that must be NEW. A busy
	// account whose peers mostly recur is a business, not a collection point,
	// however high its degree.
	minRatio float64
	knee     float64
}

// side is one direction of one agent. The seen-set is shared across scales:
// "novel" means never seen before at all, which is scale-independent. Only the
// counting windows differ, so the extra scale costs two ring buffers, not a
// second set.
type side struct {
	peers *seenSet
	win   []*obs.Window
	novel []*obs.Window
}

// NewFanout returns the default fan-out detector.
//
// 400 ticks catches a burst; 4,000 catches a slow campaign that a burst window
// cannot see. See the type comment for how those numbers were arrived at.
func NewFanout() *Fanout {
	return &Fanout{
		spans:    []obs.Tick{400, 4000},
		minPeers: 14, minRatio: 0.55, knee: 10.0,
	}
}

func (f *Fanout) Name() string { return "fanout" }

func (f *Fanout) Reset() { f.in, f.out = nil, nil }

func (f *Fanout) Observe(ev obs.Event, dst []Finding) []Finding {
	t := ev.SettleTick
	f.in = grow(f.in, ev.To)
	f.out = grow(f.out, ev.From)

	dst = f.score(&f.in[ev.To], ev.To, ev.From, t, ev, "in", dst)
	dst = f.score(&f.out[ev.From], ev.From, ev.To, t, ev, "out", dst)
	return dst
}

func (f *Fanout) score(sp **side, self, peer obs.AgentID, t obs.Tick, ev obs.Event, dir string, dst []Finding) []Finding {
	if *sp == nil {
		s := &side{peers: newSeenSet(512)}
		for _, span := range f.spans {
			s.win = append(s.win, obs.NewWindow(span, 20))
			s.novel = append(s.novel, obs.NewWindow(span, 20))
		}
		*sp = s
	}
	s := *sp

	// Novelty is scale-independent: a peer is new the first time it is ever
	// seen, so the set is updated once and every scale reads the same answer.
	known := s.peers.Add(peer)

	var best float64
	var bestNovel, bestTotal, bestRatio float64
	var bestSpan obs.Tick

	for i, span := range f.spans {
		s.win[i].Add(t, 0)
		if !known {
			s.novel[i].Add(t, 0)
		} else {
			s.novel[i].Advance(t)
		}

		total := float64(s.win[i].Count())
		novel := float64(s.novel[i].Count())
		if total < float64(f.minPeers) {
			continue
		}
		// Novelty ratio is the discriminator; volume only sets the weight. A
		// merchant scores near zero here however busy it is, because almost
		// everyone paying it has paid it before. At the longer scale this
		// guard is doing considerably more work.
		ratio := novel / total
		if ratio < f.minRatio {
			continue
		}
		score := squash(novel, f.knee) * ratio * ratio
		if score > best {
			best, bestNovel, bestTotal, bestRatio, bestSpan = score, novel, total, ratio, span
		}
	}

	if best < ScoreFloor {
		return dst
	}

	return append(dst, Finding{
		Tick: t, TxID: ev.TxID, Agent: self,
		Detector: f.Name(), Score: best,
		Evidence: map[string]float64{
			"new_counterparties": bestNovel,
			"total_in_window":    bestTotal,
			"novelty_ratio":      bestRatio,
			"window_ticks":       float64(bestSpan),
			"direction_is_in":    boolf(dir == "in"),
		},
	})
}

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
