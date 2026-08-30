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
type Fanout struct {
	in  []*side
	out []*side

	span     obs.Tick
	minPeers int
	// minRatio is the share of counterparties that must be NEW. A busy
	// account whose peers mostly recur is a business, not a collection point,
	// however high its degree.
	minRatio float64
	knee     float64
}

type side struct {
	peers *seenSet
	win   *obs.Window
	novel *obs.Window
}

// NewFanout returns the default fan-out detector.
func NewFanout() *Fanout {
	return &Fanout{span: 400, minPeers: 14, minRatio: 0.55, knee: 10.0}
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
		*sp = &side{
			peers: newSeenSet(512),
			win:   obs.NewWindow(f.span, 20),
			novel: obs.NewWindow(f.span, 20),
		}
	}
	s := *sp

	known := s.peers.Add(peer)
	s.win.Add(t, 0)
	if !known {
		s.novel.Add(t, 0)
	} else {
		s.novel.Advance(t)
	}

	total := float64(s.win.Count())
	novel := float64(s.novel.Count())
	if total < float64(f.minPeers) {
		return dst
	}

	// Novelty ratio is the discriminator; volume only sets the weight. A
	// merchant scores near zero here however busy it is, because almost
	// everyone paying it has paid it before.
	ratio := novel / total
	if ratio < f.minRatio {
		return dst
	}
	score := squash(novel, f.knee) * ratio * ratio
	if score < ScoreFloor {
		return dst
	}

	return append(dst, Finding{
		Tick: t, TxID: ev.TxID, Agent: self,
		Detector: f.Name(), Score: score,
		Evidence: map[string]float64{
			"new_counterparties": novel,
			"total_in_window":    total,
			"novelty_ratio":      ratio,
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
