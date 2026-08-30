package detect

import "github.com/yug/upi-city/internal/obs"

// Pipeline runs a fixed set of detectors over an event stream.
// DefaultWarmup is how long detectors observe before they are allowed to
// raise anything.
//
// At the start of a run every counterparty is new, every baseline is empty,
// and every agent looks anomalous — a payroll company's first disbursal is
// 100% novel recipients and scores a perfect 1.0 on pure cold start. Judging
// during that period does not measure detection, it measures ignorance. Real
// monitoring systems have the same constraint: they need history before they
// can say anything about a deviation from it.
const DefaultWarmup obs.Tick = 3000

type Pipeline struct {
	// dets is a slice, never a map. Go randomises map iteration order, and
	// running detectors in a different order between runs would change the
	// finding stream and break every comparison built on identical traffic.
	dets   []Detector
	warmup obs.Tick
}

// NewPipeline builds a pipeline from an explicit, ordered detector list.
func NewPipeline(d ...Detector) *Pipeline {
	return &Pipeline{dets: d, warmup: DefaultWarmup}
}

// SetWarmup overrides the warmup period.
func (p *Pipeline) SetWarmup(t obs.Tick) { p.warmup = t }

// Warmup returns the warmup period.
func (p *Pipeline) Warmup() obs.Tick { return p.warmup }

// NewDefault returns the standard detector set.
func NewDefault() *Pipeline {
	return NewPipeline(NewVelocity(), NewFanout(), NewCycle())
}

// NewNamed builds a pipeline from detector names, for comparing detector
// configurations against identical traffic.
func NewNamed(names []string) *Pipeline {
	var d []Detector
	for _, n := range names {
		switch n {
		case "velocity":
			d = append(d, NewVelocity())
		case "fanout":
			d = append(d, NewFanout())
		case "cycle":
			d = append(d, NewCycle())
		}
	}
	return NewPipeline(d...)
}

// Names lists the active detectors in execution order.
func (p *Pipeline) Names() []string {
	out := make([]string, len(p.dets))
	for i, d := range p.dets {
		out[i] = d.Name()
	}
	return out
}

// Observe runs every detector over one event, in fixed order.
//
// During warmup detectors still consume the event — they must, or they would
// have no history when scoring begins — but their findings are discarded.
func (p *Pipeline) Observe(ev obs.Event, dst []Finding) []Finding {
	keep := len(dst)
	for _, d := range p.dets {
		dst = d.Observe(ev, dst)
	}
	if ev.SettleTick < p.warmup {
		return dst[:keep]
	}
	return dst
}

// Reset clears all detector state, so a pipeline can be replayed.
func (p *Pipeline) Reset() {
	for _, d := range p.dets {
		d.Reset()
	}
}
