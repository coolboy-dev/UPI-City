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

// ---------------------------------------------------------------------------
// Difficulty
// ---------------------------------------------------------------------------

// Difficulty presets turn the severity surface into a dial a testbed can sweep.
//
// ─── Why a benchmark needs one ──────────────────────────────────────────────
//
// A single fraud scenario produces a single score, and a single score cannot
// tell you whether a detector is good or whether the fraud was easy. Those are
// indistinguishable from one number, and it is the failure mode every fraud
// benchmark has: a detector reports 0.9 on the traffic its authors chose, and
// nobody can say what that would become against an attacker trying harder.
//
// Sweeping difficulty replaces the number with a curve, and a curve is
// falsifiable. A detector that holds up as the attack gets quieter has earned
// something; one that collapses between two adjacent levels has told you
// exactly which assumption it was resting on.
//
// ─── Each level defeats a DIFFERENT detector, by construction ───────────────
//
// The levels are not "more of the same". Each one turns a knob that is known
// to attack a specific signal, so the resulting curve localises a failure
// rather than merely recording one:
//
//	Hops             ⇒ defeats CYCLE. The cycle search is bounded at depth 4,
//	                   so a chain longer than that is not scored low — it is
//	                   structurally invisible. Measured: a 3-hop ring scores
//	                   0.961 and a 6-hop ring scores 0.000.
//	MuleRate         ⇒ defeats VELOCITY. The detector needs a burst that is a
//	                   multiple of an account's own normal. A mule that
//	                   forwards rarely never produces one.
//	MuleAmountRupees ⇒ defeats AMOUNT ranking. Payments sized like ordinary
//	                   traffic cannot be separated by size. This is what real
//	                   structuring does.
//	RingSize         ⇒ defeats FANOUT. Fan-in concentration needs enough
//	                   distinct payers arriving at one account to look unlike
//	                   a merchant.
//	RampTicks        ⇒ attacks everything at once, by arriving slowly enough
//	                   that per-agent baselines absorb the change as normal.
//
// So the honest claim a sweep supports is not "this detector scores X". It is
// "this detector survives until the ring exceeds the search depth, and then it
// does not" — which is a statement about a mechanism, and can be checked.
var difficulties = map[string]Config{
	// Loud and obvious: what a benchmark looks like when it wants to be passed.
	// Large payments, frequent forwarding, a short chain and a fast ramp.
	"easy": {
		StartTick: 4000, RampTicks: 400, Duration: 6000,
		RingSize: 20, Hops: 3, Victims: 20, AmountScale: 1.0,
		MuleRate: 0.25, MuleAmountRupees: 8000, TakeoverRate: 0.20,
		OutageFailRate: 0.40, OutageExtraMs: 2000, OutageBank: -1, BotCount: 6,
	},
	// The project's own default, and the setting every published number in the
	// README was measured at.
	"standard": {
		StartTick: 4000, RampTicks: 2000, Duration: 6000,
		RingSize: 12, Hops: 3, Victims: 12, AmountScale: 1.0,
		MuleRate: 0.085, MuleAmountRupees: 1500, TakeoverRate: 0.06,
		OutageFailRate: 0.40, OutageExtraMs: 2000, OutageBank: -1, BotCount: 6,
	},
	// One hop past the cycle detector's search ceiling, forwarding half as
	// often, in payments small enough to sit inside ordinary traffic.
	"hard": {
		StartTick: 4000, RampTicks: 4000, Duration: 6000,
		RingSize: 8, Hops: 5, Victims: 8, AmountScale: 1.0,
		MuleRate: 0.04, MuleAmountRupees: 600, TakeoverRate: 0.03,
		OutageFailRate: 0.40, OutageExtraMs: 2000, OutageBank: -1, BotCount: 6,
	},
	// Every knob against the defence at once. This is not a fair fight and is
	// not meant to be: it is the floor of the curve, and a detector that still
	// scores here has found something the others did not.
	"brutal": {
		StartTick: 4000, RampTicks: 6000, Duration: 6000,
		RingSize: 6, Hops: 6, Victims: 6, AmountScale: 1.0,
		MuleRate: 0.02, MuleAmountRupees: 300, TakeoverRate: 0.015,
		OutageFailRate: 0.40, OutageExtraMs: 2000, OutageBank: -1, BotCount: 6,
	},
}

// Difficulty returns a named preset.
func Difficulty(name string) (Config, bool) {
	c, ok := difficulties[name]
	return c, ok
}

// DifficultyNames lists the presets from easiest to hardest.
//
// Deliberately ordered rather than sorted: "brutal" before "easy"
// alphabetically would put the curve backwards in every table that ranges over
// it, and a difficulty axis that does not increase is worse than none.
func DifficultyNames() []string { return []string{"easy", "standard", "hard", "brutal"} }

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
