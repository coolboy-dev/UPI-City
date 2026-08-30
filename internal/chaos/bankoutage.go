package chaos

import (
	"math/rand/v2"

	"github.com/yug/upi-city/internal/obs"
)

func init() { Register("bank-outage", func() Scenario { return &BankOutage{} }) }

// BankOutage degrades one PSP: failures rise, latency climbs, and legitimate
// agents retry.
//
// This is deliberately framed and measured as a SYSTEM-SCALE HARD NEGATIVE
// rather than as a second kind of fraud. The uninteresting question is
// whether the dashboard turns red. The interesting one — and the reason this
// scenario is worth building at all — is whether the FRAUD detector fires on
// an infrastructure failure.
//
// It plausibly should: retry storms produce exactly the velocity spike a
// mule hub produces, across thousands of accounts at once. False positives
// attributable to the outage window are therefore reported as a headline
// number, because distinguishing "our bank is broken" from "we are being
// robbed" is the actual competency being demonstrated.
type BankOutage struct {
	cfg    Config
	bank   obs.BankID
	armed  bool
	closed bool
	last   float64
}

func (b *BankOutage) Name() string { return "bank-outage" }

func (b *BankOutage) Arm(cfg Config, view WorldView, rng *rand.Rand) {
	b.cfg = cfg
	if cfg.OutageBank >= 0 && cfg.OutageBank < view.NumBanks() {
		b.bank = obs.BankID(cfg.OutageBank)
	} else {
		b.bank = obs.BankID(rng.IntN(view.NumBanks()))
	}
	b.armed = view.NumBanks() > 0
	b.last = -1
}

func (b *BankOutage) Intensity(t obs.Tick) float64 {
	return ramp(t, b.cfg.StartTick, b.cfg.RampTicks, b.cfg.Duration)
}

func (b *BankOutage) Done(t obs.Tick) bool {
	return t >= b.cfg.StartTick+b.cfg.Duration
}

func (b *BankOutage) Members() []obs.AgentID { return nil }

// Bank reports which PSP this scenario degrades, for reporting.
func (b *BankOutage) Bank() obs.BankID { return b.bank }

func (b *BankOutage) Step(t obs.Tick, view WorldView) []Effect {
	if !b.armed {
		return nil
	}
	if b.Done(t) {
		if b.closed {
			return nil
		}
		b.closed = true
		return []Effect{RestoreBank{Bank: b.bank}}
	}

	in := b.Intensity(t)
	if in <= 0 {
		return nil
	}
	// Re-emit only when the degradation has moved materially, so the effect
	// stream stays small enough to read in a log.
	if b.last >= 0 && in-b.last < 0.05 {
		return nil
	}
	b.last = in

	return []Effect{DegradeBank{
		Bank:     b.bank,
		FailRate: b.cfg.OutageFailRate * in,
		ExtraMs:  uint16(float64(b.cfg.OutageExtraMs) * in),
		Until:    b.cfg.StartTick + b.cfg.Duration,
	}}
}
