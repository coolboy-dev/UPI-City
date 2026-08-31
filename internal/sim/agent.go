package sim

import (
	"math/rand/v2"

	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// FraudState is what a chaos scenario turns on. It is the ONLY input to
// transaction labelling: whatever an agent does while this is non-zero is
// labelled and attributed automatically by the engine.
//
// Scenario authors set state; they never write labels. That inversion is what
// makes it impossible to inject an unlabelled incident.
type FraudState uint8

const (
	FraudNone FraudState = iota
	// FraudTakeover is a compromised legitimate account draining to a ring
	// entry point. Rings are funded from real victim balances rather than
	// from value created for the scenario — laundering moves money that
	// already exists, and a simulator that mints it instead would make the
	// resulting flow statistics meaningless.
	FraudTakeover
	FraudMule
	FraudCashout
	FraudBot
	// FraudScamVictim is a social-engineering victim: one large voluntary
	// payment to a fraudster. No chain, no cycle, no repetition.
	FraudScamVictim
)

// Label maps fraud state to the ground-truth label for transactions
// originated in that state.
func (f FraudState) Label() truth.Label {
	switch f {
	case FraudTakeover:
		return truth.LabelTakeover
	case FraudMule:
		return truth.LabelRingMule
	case FraudCashout:
		return truth.LabelRingCashout
	case FraudBot:
		return truth.LabelBotBurst
	case FraudScamVictim:
		return truth.LabelScam
	}
	return truth.LabelNormal
}

// Agent is one participant in the network.
type Agent struct {
	ID        obs.AgentID
	Bank      obs.BankID
	Archetype truth.Archetype
	BalanceP  int64
	DeviceID  uint32
	BirthTick obs.Tick
	// PriorAge is how old the account already was when the simulation began.
	//
	// Without it every founding account is "new" for the first
	// NewAccountTicks, so Event.IsNewFrom is uniformly true early in the run
	// and carries no information at all — while a genuinely fresh account
	// opened by a fraud scenario mid-run would be indistinguishable from the
	// entire founding population. Staggering vintages makes account age a
	// real signal from tick zero.
	PriorAge obs.Tick

	// Behavior is per-agent, holding that agent's own recurring counterparty
	// sets. Behaviours are constructed once and then only read.
	Behavior Behavior

	// Set by chaos effects, read by the engine when labelling.
	Fraud    FraudState
	Incident truth.IncidentID
	// RingNext is the next hop for a recruited mule.
	RingNext obs.AgentID

	rng *rand.Rand

	X, Y float32 // frozen display position, computed once
}

// AgentView is the read-only projection a behaviour is given of itself.
// Passing a value rather than a pointer makes the read-only contract
// structural rather than a comment.
type AgentView struct {
	ID        obs.AgentID
	Bank      obs.BankID
	BalanceP  int64
	Archetype truth.Archetype
	Fraud     FraudState
	RingNext  obs.AgentID
	Age       obs.Tick
}

// View projects the agent for the decide phase.
func (a *Agent) View(now obs.Tick) AgentView {
	return AgentView{
		ID:        a.ID,
		Bank:      a.Bank,
		BalanceP:  a.BalanceP,
		Archetype: a.Archetype,
		Fraud:     a.Fraud,
		RingNext:  a.RingNext,
		Age:       a.Age(now),
	}
}

// Age is the account's total age, including time before the run started.
func (a *Agent) Age(now obs.Tick) obs.Tick { return a.PriorAge + (now - a.BirthTick) }

// Intent is a proposed transaction. Phase A produces intents without touching
// world state; Phase B is the only thing that may turn one into a payment.
type Intent struct {
	To      obs.AgentID
	AmountP int64
}

// Snapshot is the frozen, read-only view of the world handed to every
// behaviour during the decide phase.
//
// Because Decide may only read this, the whole phase is pure, which is what
// makes it safe to shard across cores later while still producing a
// bit-identical event stream.
// Attack carries the adversary's current tuning into the decide phase.
//
// Read-only during Phase A like everything else on the snapshot, so an
// adversarial search can vary these between runs without touching the tick
// loop or breaking determinism within a run.
type Attack struct {
	MuleRate         float64
	MuleAmountRupees float64
	TakeoverRate     float64
}

type Snapshot struct {
	Tick   obs.Tick
	Surge  float64
	Attack Attack

	// Target pools, held as sorted dense slices. Never maps: Go randomises
	// map iteration order, and one range-over-map in this path would silently
	// destroy reproducibility.
	Merchants []obs.AgentID
	Consumers []obs.AgentID
}

// PickMerchant returns a random merchant to pay.
func (s *Snapshot) PickMerchant(rng *rand.Rand) obs.AgentID {
	if len(s.Merchants) == 0 {
		return 0
	}
	return s.Merchants[rng.IntN(len(s.Merchants))]
}

// PickConsumer returns a random consumer.
func (s *Snapshot) PickConsumer(rng *rand.Rand) obs.AgentID {
	if len(s.Consumers) == 0 {
		return 0
	}
	return s.Consumers[rng.IntN(len(s.Consumers))]
}

// Behavior decides what an agent attempts each tick.
//
// Decide MUST be pure: it reads self and the snapshot, mutates nothing, and
// appends to dst. Appending to a caller-owned buffer rather than returning a
// fresh slice keeps the tick loop allocation-free, which matters at 5,000
// agents on a memory-constrained machine.
type Behavior interface {
	Name() string
	Decide(self AgentView, w *Snapshot, rng *rand.Rand, t obs.Tick, dst []Intent) []Intent
}
