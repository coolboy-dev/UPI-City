package chaos

import (
	"math/rand/v2"

	"github.com/yug/upi-city/internal/obs"
)

func init() { Register("scam-payout", func() Scenario { return &ScamPayout{} }) }

// ScamPayout is a social-engineering scam: victims are talked into paying a
// fraudster directly.
//
// ─── This exists as a GENERALISATION TEST ───────────────────────────────────
//
// Every detector in this project was designed against the laundering ring, and
// evaluated against the laundering ring. Nothing has ever asked the question
// that matters most: does it catch an attack it was not built for?
//
// So this scenario is deliberately built AFTER the detectors were frozen, and
// nothing is tuned against it. Whatever the numbers come out as is the result.
//
// ─── Why this shape specifically ────────────────────────────────────────────
//
// Validating the simulator against published figures turned up a diagnostic
// mismatch. Real UPI fraud is roughly 0.5% of transaction VALUE but 0.001% of
// transaction COUNT (RBI FY24) — meaning real fraud is a small number of large
// payments. The laundering ring produces the opposite: many small structured
// payments. The dominant real pattern is not laundering at all, it is scams,
// where a victim voluntarily makes one large transfer.
//
// It is also close to a worst case for these detectors, which is the point:
//
//   - No cycle. Money moves one hop and stops, so the cycle detector — the
//     strongest of the three — has nothing to find.
//   - No velocity anomaly on the victim. They make ONE payment. Their own
//     baseline barely registers it.
//   - The only real signal is fan-in: many payers, none of whom have paid
//     this account before. That is precisely what the fanout detector is for,
//     and fanout currently scores at the prevalence floor.
type ScamPayout struct {
	cfg Config

	fraudsters []obs.AgentID
	victims    []obs.AgentID

	activeVictims int
	armed         bool
	closed        bool
}

func (s *ScamPayout) Name() string { return "scam-payout" }

func (s *ScamPayout) Arm(cfg Config, view WorldView, rng *rand.Rand) {
	s.cfg = cfg

	// A handful of collection accounts. Real scam operations run few
	// beneficiary accounts and many victims, which is the inverse of a
	// laundering ring's structure.
	n := cfg.RingSize / 4
	if n < 2 {
		n = 2
	}
	s.fraudsters = view.Recruitable(rng, n)

	// Many more victims than the ring scenario uses: the harm here is spread
	// across the customer base rather than concentrated in a chain.
	v := cfg.Victims * 4
	if v < 20 {
		v = 20
	}
	s.victims = view.Wealthy(rng, v, s.fraudsters)

	s.armed = len(s.fraudsters) > 0 && len(s.victims) > 0
}

func (s *ScamPayout) Intensity(t obs.Tick) float64 {
	return ramp(t, s.cfg.StartTick, s.cfg.RampTicks, s.cfg.Duration)
}

func (s *ScamPayout) Done(t obs.Tick) bool {
	return t >= s.cfg.StartTick+s.cfg.Duration
}

func (s *ScamPayout) Members() []obs.AgentID {
	out := make([]obs.AgentID, 0, len(s.fraudsters)+len(s.victims))
	out = append(out, s.fraudsters...)
	out = append(out, s.victims...)
	return out
}

func (s *ScamPayout) Step(t obs.Tick, view WorldView) []Effect {
	if !s.armed {
		return nil
	}
	if s.Done(t) {
		if s.closed {
			return nil
		}
		s.closed = true
		return []Effect{Release{Agents: s.Members()}}
	}

	in := s.Intensity(t)
	if in <= 0 {
		return nil
	}

	var fx []Effect

	// The collection accounts are activated once, at the start. They simply
	// receive; they do not forward, which is what removes the cycle.
	if s.activeVictims == 0 {
		fx = append(fx, Recruit{Agents: s.fraudsters, Role: RoleCashout})
	}

	// Victims are taken in one at a time as the scam campaign spreads. Each
	// is pointed at a collection account and makes a single large payment.
	want := int(in * float64(len(s.victims)))
	for s.activeVictims < want && s.activeVictims < len(s.victims) {
		target := s.fraudsters[s.activeVictims%len(s.fraudsters)]
		fx = append(fx, Recruit{
			Agents: []obs.AgentID{s.victims[s.activeVictims]},
			Role:   RoleScamVictim,
			Next:   target,
		})
		s.activeVictims++
	}
	return fx
}
