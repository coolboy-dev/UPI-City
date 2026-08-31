// Package chaos injects disruptions into a running simulation.
//
// It depends only on internal/obs, never on internal/sim. Scenarios describe
// WHAT should happen as declarative effects; the engine decides how to apply
// them. That inversion is what lets the core tick loop contain exactly one
// line about chaos, and what makes a new scenario one new file with an
// init() rather than an edit to the world.
package chaos

import (
	"math/rand/v2"
	"sort"

	"github.com/yug/upi-city/internal/obs"
)

// Role is what a recruited account does. The engine maps this onto its own
// internal fraud state; chaos deliberately knows nothing about that type.
type Role uint8

const (
	RoleTakeover Role = iota // compromised victim, drains into the ring
	RoleMule                 // layering hop
	RoleCashout              // terminal extraction point
	RoleBot                  // high-frequency low-value burst
	// RoleScamVictim is someone talked into paying a fraudster directly. One
	// large voluntary payment, no laundering chain behind it.
	RoleScamVictim
)

// Effect is a declarative change to the world.
type Effect interface{ isEffect() }

// Recruit activates accounts into a fraud role. Chain, when present, is the
// hop order: each agent's next hop is the following entry.
type Recruit struct {
	Agents []obs.AgentID
	Role   Role
	// Next is the forwarding target for every agent in Agents. Zero means
	// "use the chain position".
	Next  obs.AgentID
	Chain []obs.AgentID
}

// Release returns accounts to normal behaviour.
type Release struct{ Agents []obs.AgentID }

// DegradeBank puts a PSP into an outage until the given tick.
type DegradeBank struct {
	Bank     obs.BankID
	FailRate float64
	ExtraMs  uint16
	Until    obs.Tick
}

// RestoreBank ends an outage early.
type RestoreBank struct{ Bank obs.BankID }

func (Recruit) isEffect()     {}
func (Release) isEffect()     {}
func (DegradeBank) isEffect() {}
func (RestoreBank) isEffect() {}

// Config is the severity surface: every knob a demo slider or a harness flag
// can turn. Ramping in particular is not a flourish — see Scenario.Intensity.
type Config struct {
	StartTick obs.Tick
	// RampTicks is how long the attack takes to reach full intensity.
	RampTicks obs.Tick
	Duration  obs.Tick

	// Fraud ring.
	RingSize    int
	Hops        int
	Victims     int
	AmountScale float64

	// ─── Attacker tuning ───────────────────────────────────────────────
	//
	// Exposed so an adversary can search them. A ring with parameters fixed
	// by the defence is a straw opponent: the detector gets graded against
	// exactly the attack it was built for. Real launderers observe what gets
	// stopped and move, and the only honest way to report detection quality
	// is at the attacker's BEST RESPONSE rather than at whatever settings
	// happened to be committed.
	MuleRate         float64 // chance per tick an active mule forwards
	MuleAmountRupees float64 // median size of a forwarded payment
	TakeoverRate     float64 // chance per tick a compromised account pays in

	// Bank outage.
	OutageFailRate float64
	OutageExtraMs  uint16
	OutageBank     int // -1 picks one at random

	// Bot swarm.
	BotCount int
}

// DefaultConfig returns moderate severity for both scenarios.
func DefaultConfig() Config {
	return Config{
		StartTick:        4000,
		RampTicks:        2000,
		Duration:         6000,
		RingSize:         12,
		Hops:             3,
		Victims:          12,
		AmountScale:      1.0,
		MuleRate:         0.085,
		MuleAmountRupees: 1500,
		TakeoverRate:     0.06,
		OutageFailRate:   0.40,
		OutageExtraMs:    2000,
		OutageBank:       -1,
		BotCount:         6,
	}
}

// WorldView is the read-only window a scenario gets onto the world. Keeping
// it an interface, defined here, is what stops this package from importing
// the engine.
type WorldView interface {
	Tick() obs.Tick
	NumAgents() int
	NumBanks() int
	// Recruitable returns dormant accounts that can be activated. They are
	// ordinary consumers until this moment.
	Recruitable(rng *rand.Rand, n int) []obs.AgentID
	// Wealthy returns ordinary accounts with a balance worth stealing.
	Wealthy(rng *rand.Rand, n int, exclude []obs.AgentID) []obs.AgentID
}

// Scenario is one injectable disruption.
type Scenario interface {
	Name() string
	// Arm plans the scenario deterministically, once, before it starts.
	Arm(cfg Config, view WorldView, rng *rand.Rand)
	// Step returns the effects to apply at tick t.
	Step(t obs.Tick, view WorldView) []Effect
	Done(t obs.Tick) bool
	// Intensity is the ramp position in [0,1].
	//
	// This is load-bearing, not decoration. With instant-on fraud, detection
	// latency measures nothing but the detector's polling interval — every
	// scenario would be caught in the same tick and the number would be an
	// artefact. A gradual ramp makes latency a real quantity and lets the
	// honest question be asked: at what intensity does the detector actually
	// trip?
	Intensity(t obs.Tick) float64
	// Members are every agent the scenario touches, for the truth ledger.
	Members() []obs.AgentID
}

// ---------------------------------------------------------------------------
// Registry. Scenarios self-register in init(); nothing in the core loop ever
// names one.
// ---------------------------------------------------------------------------

var registry = map[string]func() Scenario{}

// Register adds a scenario constructor.
func Register(name string, ctor func() Scenario) { registry[name] = ctor }

// New builds a registered scenario.
func New(name string) (Scenario, bool) {
	c, ok := registry[name]
	if !ok {
		return nil, false
	}
	return c(), true
}

// Names lists registered scenarios in sorted order. Sorted, not map order:
// Go randomises map iteration, and one range-over-map reaching anything that
// affects the run would break reproducibility.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ramp is the shared intensity curve: zero before start, linear across the
// ramp, one until the scenario ends.
func ramp(t, start, rampTicks, duration obs.Tick) float64 {
	if t < start {
		return 0
	}
	if t >= start+duration {
		return 0
	}
	if rampTicks == 0 {
		return 1
	}
	d := t - start
	if d >= rampTicks {
		return 1
	}
	return float64(d) / float64(rampTicks)
}
