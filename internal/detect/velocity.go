package detect

import "github.com/yug/upi-city/internal/obs"

// Velocity scores an agent's transaction rate against ITS OWN history.
//
// The naive version of this detector — a global "more than N payments per
// minute is suspicious" threshold — fails twice over, and both failures are
// represented in the population:
//
//   - A large merchant legitimately transacts hundreds of times a minute and
//     would be permanently flagged.
//   - During a festival surge every agent's rate rises together, so a global
//     threshold fires on the entire network at once.
//
// Scoring against a per-agent baseline survives both: the merchant's own
// normal is high, and a network-wide surge lifts the observed rate and the
// baseline together.
type Velocity struct {
	win  []*obs.Window
	base []obs.Baseline
	last []obs.Tick

	// span is the observation window; sample is how often the current rate
	// is folded into the baseline.
	span   obs.Tick
	sample obs.Tick
	// minCount is an absolute activity floor. Without it, an agent that
	// normally sends nothing has near-zero variance, so two transactions
	// produce an enormous z-score — a false positive manufactured out of
	// quietness.
	minCount int32
	// minRatio requires the burst to be a MULTIPLE of the agent's own normal,
	// not merely above it.
	minRatio float64
	// zFloor is the deviation below which nothing is reported.
	//
	// This detector's first version scored squash(z, 3), which rated z=2 —
	// an utterly ordinary fluctuation — at 0.49, and consequently flagged
	// every consumer in the network above the actual fraud ring. Ordinary
	// traffic deviates by two sigma constantly; only the far tail is worth
	// anyone's attention.
	zFloor float64
	knee   float64
}

// NewVelocity returns the default velocity detector.
func NewVelocity() *Velocity {
	return &Velocity{
		span:     300,
		sample:   50,
		minCount: 12,
		minRatio: 2.5,
		zFloor:   3.0,
		knee:     4.0,
	}
}

func (v *Velocity) Name() string { return "velocity" }

func (v *Velocity) Reset() {
	v.win, v.base, v.last = nil, nil, nil
}

func (v *Velocity) Observe(ev obs.Event, dst []Finding) []Finding {
	id := ev.From
	v.win = grow(v.win, id)
	v.base = grow(v.base, id)
	v.last = grow(v.last, id)

	if v.win[id] == nil {
		v.win[id] = obs.NewWindow(v.span, 10)
		v.base[id] = obs.NewBaseline(0.02)
	}
	w := v.win[id]
	t := ev.SettleTick
	w.Add(t, ev.AmountP)

	count := w.Count()
	ready := v.base[id].Ready(12)
	z := 0.0
	if ready {
		z = v.base[id].Z(float64(count))
	}

	// ─── Baseline poisoning ────────────────────────────────────────────
	//
	// The baseline is frozen while the agent looks anomalous, and resumes
	// updating once it settles back to normal.
	//
	// Without this the attack trains the detector to accept it. An earlier
	// version folded the current rate in unconditionally, and the measured
	// consequence was stark: recall against the fraud ring was 72% while the
	// attack was ramping up and 7% once it reached full strength. The mules
	// were not becoming harder to see — their own baselines had absorbed the
	// elevated rate and made it unremarkable. A slow smoothing factor is not
	// a defence, only a slower surrender.
	//
	// Freezing is self-correcting rather than permanent: a genuinely growing
	// merchant is briefly anomalous, stops being scored once its new level
	// persists past the window, and its baseline resumes tracking.
	if t-v.last[id] >= v.sample {
		if !ready || z < v.zFloor {
			v.base[id].Observe(float64(count))
		}
		v.last[id] = t
	}

	// A cold baseline cannot say anything useful, and scoring against one is
	// how new accounts get flagged for existing.
	if !ready || count < v.minCount {
		return dst
	}
	mean := v.base[id].Mean
	if mean <= 0 || float64(count) < v.minRatio*mean {
		return dst
	}
	if z < v.zFloor {
		return dst
	}
	score := squash(z-v.zFloor, v.knee)
	if score < ScoreFloor {
		return dst
	}

	return append(dst, Finding{
		Tick: t, TxID: ev.TxID, Agent: id,
		Detector: v.Name(), Score: score,
		Evidence: map[string]float64{
			"tx_in_window": float64(count),
			"agent_mean":   v.base[id].Mean,
			"z":            z,
		},
	})
}
