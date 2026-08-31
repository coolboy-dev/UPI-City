// Package truth holds ground truth: what actually happened, as opposed to
// what a detector believed.
//
// ┌───────────────────────────────────────────────────────────────────────┐
// │ internal/detect MUST NOT import this package, directly or             │
// │ transitively. internal/detect/boundary_test.go enforces that by       │
// │ walking the dependency graph and failing the build on violation.      │
// └───────────────────────────────────────────────────────────────────────┘
//
// Only the metrics layer is permitted to join detector output against this
// package, and only after the run has completed.
package truth

import "github.com/yug/upi-city/internal/obs"

// Label is the true nature of a transaction.
type Label uint8

const (
	LabelNormal Label = iota
	// LabelTakeover marks a compromised legitimate account pushing funds
	// into a ring. This is where a laundering ring's money actually comes
	// from: real balances belonging to real victims, not value minted for
	// the scenario's convenience.
	LabelTakeover
	LabelRingMule
	LabelRingCashout
	LabelBotBurst
	LabelStructuring
	// LabelScam is a social-engineering payout: the victim authorised it, so
	// no account was compromised and no chain exists to trace.
	LabelScam
)

func (l Label) String() string {
	switch l {
	case LabelNormal:
		return "normal"
	case LabelTakeover:
		return "takeover"
	case LabelRingMule:
		return "ring_mule"
	case LabelRingCashout:
		return "ring_cashout"
	case LabelBotBurst:
		return "bot_burst"
	case LabelStructuring:
		return "structuring"
	case LabelScam:
		return "scam_payout"
	}
	return "unknown"
}

// Fraudulent reports whether the label counts as a positive when computing
// precision and recall.
func (l Label) Fraudulent() bool { return l != LabelNormal }

// Archetype is an agent's behavioural class.
//
// The three hard negatives — MegaMerchant, Payroll, SupplyPair — exist so the
// false-positive rate is structurally non-zero. Each is deliberately built as
// a near-twin of a fraud archetype, differing in exactly one discriminating
// feature. A detector that scores them at zero is not a good detector; it is
// a detector facing traffic too easy to be evidence of anything.
type Archetype uint8

const (
	ArchConsumer Archetype = iota
	ArchMegaMerchant
	ArchPayroll
	ArchSupplyPair
	ArchMule
	ArchBot
)

func (a Archetype) String() string {
	switch a {
	case ArchConsumer:
		return "consumer"
	case ArchMegaMerchant:
		return "mega_merchant"
	case ArchPayroll:
		return "payroll"
	case ArchSupplyPair:
		return "supply_pair"
	case ArchMule:
		return "mule"
	case ArchBot:
		return "bot"
	}
	return "unknown"
}

// HardNegative reports whether this archetype is legitimate traffic that is
// deliberately designed to look suspicious. Reported as its own line in the
// false-positive breakdown.
func (a Archetype) HardNegative() bool {
	return a == ArchMegaMerchant || a == ArchPayroll || a == ArchSupplyPair
}

// IncidentID identifies one injected chaos episode. Zero means "none".
type IncidentID uint32

// Incident is the ground-truth record of one chaos injection, written by the
// engine at the moment of injection.
//
// StartTick is the latency baseline: detection latency is measured from here
// to the first flag raised on any member. Because the engine writes it and
// scenario authors cannot, the baseline cannot be fudged — accidentally or
// otherwise.
type Incident struct {
	ID        IncidentID    `json:"id"`
	Kind      string        `json:"kind"`
	StartTick obs.Tick      `json:"start_tick"`
	EndTick   obs.Tick      `json:"end_tick"`
	Members   []obs.AgentID `json:"members"`
	TxIDs     []obs.TxID    `json:"tx_ids"`
}
