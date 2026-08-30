package chaos

import (
	"math/rand/v2"

	"github.com/yug/upi-city/internal/obs"
)

func init() { Register("fraud-ring", func() Scenario { return &FraudRing{} }) }

// FraudRing is a money-laundering ring: compromised accounts drain into a
// chain of mules, which forward the funds through several fast hops to a
// cash-out point.
//
// The ring is built as a NEAR-TWIN of legitimate structures, not as an
// obvious anomaly:
//
//   - Its fan-in looks like a merchant's, because dozens of ordinary
//     consumers pay into one account. What differs is that the payers do not
//     recur and the funds do not stay.
//   - Its hops form a cycle, exactly as a supply chain does. What differs is
//     that the cycle is traversed in seconds rather than days, and value is
//     retained at each hop rather than returning.
//
// Members activate progressively across the ramp, so the attack grows from
// two accounts moving small amounts to the full ring moving large ones. This
// is what makes detection latency a meaningful measurement rather than an
// artefact of when the scenario was switched on.
type FraudRing struct {
	cfg Config
	// cells are short independent laundering cycles, each Hops long, rather
	// than one long chain.
	//
	// This is how layering actually works — funds round-trip through two or
	// three intermediaries and back — and a single twelve-member conga line
	// would form a twelve-hop cycle that no bounded graph search could close.
	// Building the attack the way it really happens and building it so it is
	// detectable at a sane cost turn out to be the same choice.
	cells   [][]obs.AgentID
	flat    []obs.AgentID // activation order across all cells
	victims []obs.AgentID

	activeMules   int
	activeVictims int
	armed         bool
	closed        bool
}

// next returns the forwarding target for a member, wrapping inside its cell.
func (f *FraudRing) next(id obs.AgentID) obs.AgentID {
	for _, c := range f.cells {
		for i, m := range c {
			if m == id {
				return c[(i+1)%len(c)]
			}
		}
	}
	return 0
}

// entry returns the first member of the cell containing id.
func (f *FraudRing) entry(id obs.AgentID) obs.AgentID {
	for _, c := range f.cells {
		for _, m := range c {
			if m == id {
				return c[0]
			}
		}
	}
	return 0
}

func (f *FraudRing) Name() string { return "fraud-ring" }

func (f *FraudRing) Arm(cfg Config, view WorldView, rng *rand.Rand) {
	f.cfg = cfg
	size := cfg.RingSize
	if size < 2 {
		size = 2
	}
	pool := view.Recruitable(rng, size)

	hops := cfg.Hops
	if hops < 3 {
		hops = 3 // a 2-cycle is a refund pattern, not layering
	}
	for i := 0; i+hops <= len(pool); i += hops {
		cell := pool[i : i+hops : i+hops]
		f.cells = append(f.cells, cell)
		f.flat = append(f.flat, cell...)
	}

	f.victims = view.Wealthy(rng, cfg.Victims, f.flat)
	f.armed = len(f.cells) > 0
}

func (f *FraudRing) Intensity(t obs.Tick) float64 {
	return ramp(t, f.cfg.StartTick, f.cfg.RampTicks, f.cfg.Duration)
}

func (f *FraudRing) Done(t obs.Tick) bool {
	return t >= f.cfg.StartTick+f.cfg.Duration
}

func (f *FraudRing) Members() []obs.AgentID {
	out := make([]obs.AgentID, 0, len(f.flat)+len(f.victims))
	out = append(out, f.flat...)
	out = append(out, f.victims...)
	return out
}

func (f *FraudRing) Step(t obs.Tick, view WorldView) []Effect {
	if !f.armed {
		return nil
	}

	// Wind down: release everyone once the scenario ends.
	if f.Done(t) {
		if f.closed {
			return nil
		}
		f.closed = true
		return []Effect{Release{Agents: f.Members()}}
	}

	in := f.Intensity(t)
	if in <= 0 {
		return nil
	}

	var fx []Effect

	// Recruit mules up to the current intensity, cell by cell in a fixed
	// order, so growth is deterministic.
	wantMules := int(in * float64(len(f.flat)))
	if wantMules < f.cfg.Hops {
		wantMules = f.cfg.Hops // a cell only launders once it is complete
	}
	for f.activeMules < wantMules && f.activeMules < len(f.flat) {
		id := f.flat[f.activeMules]
		role := RoleMule
		// The last member of each cell extracts rather than forwards. Its
		// "next" wraps to the cell entry, closing the cycle — which is what
		// makes the structure detectable as one rather than as a path.
		for _, c := range f.cells {
			if c[len(c)-1] == id {
				role = RoleCashout
			}
		}
		fx = append(fx, Recruit{
			Agents: []obs.AgentID{id},
			Role:   role,
			Next:   f.next(id),
		})
		f.activeMules++
	}

	// Compromise victims progressively. They pay into a cell's entry point,
	// producing fan-in from accounts that have never paid it before.
	wantVictims := int(in * float64(len(f.victims)))
	for f.activeVictims < wantVictims && f.activeVictims < len(f.victims) {
		cell := f.cells[f.activeVictims%len(f.cells)]
		fx = append(fx, Recruit{
			Agents: []obs.AgentID{f.victims[f.activeVictims]},
			Role:   RoleTakeover,
			Next:   cell[0],
		})
		f.activeVictims++
	}

	return fx
}
