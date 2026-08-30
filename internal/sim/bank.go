package sim

import (
	"math/rand/v2"

	"github.com/yug/upi-city/internal/obs"
)

// Bank is a PSP that routes and settles transactions.
//
// Outage state lives here rather than in the chaos package so that the core
// loop has no knowledge of scenarios: a chaos effect sets these fields, and
// routing behaviour changes as a consequence.
type Bank struct {
	ID   obs.BankID
	Name string

	// Baseline health.
	BaseLatencyMs uint16
	BaseFailRate  float64

	// Degradation, set by a chaos effect. Zero values mean healthy.
	OutageFailRate float64
	OutageExtraMs  uint16
	OutageUntil    obs.Tick
}

// Degraded reports whether the bank is currently in an induced outage.
func (b *Bank) Degraded(t obs.Tick) bool { return t < b.OutageUntil }

// Route decides the outcome of one transaction.
//
// A degraded bank both fails more and responds slower. Legitimate agents
// retry on failure, which produces a genuine network-wide velocity spike —
// that is the point. The interesting question a bank outage poses is not
// "does the dashboard turn red" but "does the fraud detector fire on an
// infrastructure failure", and answering it honestly requires the outage to
// perturb real traffic rather than being cosmetically flagged.
func (b *Bank) Route(rng *rand.Rand, t obs.Tick) (obs.Status, uint16) {
	fail := b.BaseFailRate
	extra := uint16(0)
	if b.Degraded(t) {
		fail = b.OutageFailRate
		extra = b.OutageExtraMs
	}

	lat := b.BaseLatencyMs + extra
	// Jitter is proportional to the base, so a degraded bank is both slower
	// and more erratic.
	lat += uint16(rng.IntN(int(b.BaseLatencyMs)/2 + 1))

	if rng.Float64() < fail {
		// Slow failures are timeouts, fast ones are hard declines. The
		// distinction matters: timeouts are what drive retry storms.
		if extra > 0 {
			return obs.StatusTimeout, lat
		}
		return obs.StatusFailed, lat
	}
	return obs.StatusSuccess, lat
}

// SettleDelay converts bank latency into whole ticks of settlement delay.
func (b *Bank) SettleDelay(base obs.Tick, msPerTick uint64, t obs.Tick) obs.Tick {
	d := base
	if b.Degraded(t) {
		d += obs.Tick(uint64(b.OutageExtraMs) / msPerTick)
	}
	return d
}
