// Package decide turns a calibrated risk score into an action.
//
// Like internal/detect and internal/risk, this package must never import
// internal/truth or internal/sim — a policy runs live, against traffic whose
// labels nobody knows yet.
package decide

// Decision is what happens to a transaction.
//
// Three outcomes, not two. A binary flag/allow model hides the fact that the
// two ways of being wrong have wildly different costs: wrongly BLOCKING a
// customer's rent payment is a support call, a chargeback and possibly a lost
// customer, while wrongly REVIEWING it costs a few seconds of an analyst's
// time and the customer never knows. Collapsing those into one number is the
// single most misleading thing a fraud benchmark can do.
type Decision uint8

const (
	// Allow lets the payment through untouched.
	Allow Decision = iota
	// Review holds it for a human. Cheap, but the queue is staffed by real
	// people, so its size is a hard operational budget.
	Review
	// Block stops it. Expensive and customer-visible: precision here matters
	// far more than recall.
	Block
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Review:
		return "review"
	case Block:
		return "block"
	}
	return "unknown"
}

// Policy is a two-threshold decision rule.
//
// Below TauReview the payment goes through. Between the thresholds it queues
// for a human. At or above TauBlock it is stopped outright.
type Policy struct {
	TauReview float64 `json:"tau_review"`
	TauBlock  float64 `json:"tau_block"`
}

// Decide applies the policy to one score.
func (p Policy) Decide(score float64) Decision {
	switch {
	case score >= p.TauBlock:
		return Block
	case score >= p.TauReview:
		return Review
	}
	return Allow
}

// Valid reports whether the thresholds are ordered sensibly. A block
// threshold below the review threshold would mean transactions are blocked
// without ever being reviewable, which is a configuration error rather than a
// strict policy.
func (p Policy) Valid() bool {
	return p.TauBlock >= p.TauReview && p.TauReview >= 0 && p.TauBlock <= 1
}
