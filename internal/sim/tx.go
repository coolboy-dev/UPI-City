package sim

import (
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// Transaction is the labelled, engine-side view of a payment.
//
// It embeds obs.Event — the observable core — and adds ground truth alongside
// it. Embedding rather than duplicating fields means there is exactly ONE
// place the label could cross into the detection layer, and that place is
// Observe below, which copies a type that has no label field.
type Transaction struct {
	obs.Event

	Label     truth.Label
	Incident  truth.IncidentID
	Archetype truth.Archetype

	// Retried marks a payment that is itself a re-attempt, so it is not
	// queued again. Engine-side only — a monitoring system sees the repeat
	// attempt as an ordinary transaction, which is precisely why a retry
	// storm is hard to distinguish from a velocity attack.
	Retried bool
}

// Observe projects the transaction down to what a detector is allowed to see.
//
// This is the single bridge across the ground-truth firewall. Everything the
// detection layer knows about the network arrives through this one method.
func (t *Transaction) Observe() obs.Event { return t.Event }
