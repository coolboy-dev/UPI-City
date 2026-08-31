// Package metrics joins detector output against ground truth.
//
// This is the ONLY package permitted to see both sides. It runs after a run
// has completed, never during one, so nothing it knows can reach a detector.
//
// The numbers here are the submission's actual contribution. Anyone can build
// a simulator that generates obvious fraud and a detector that catches it;
// what is hard, and what production data cannot give you at all, is an honest
// account of what the detector costs when it is wrong.
package metrics

import (
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// ScoredRow is one transaction, its score, and its true label.
//
// Every transaction in the run produces a row, including the overwhelming
// majority that no detector ever flagged. Evaluating only the flagged ones
// would make recall unmeasurable and precision meaningless — the classic way
// to report a fraud detector that looks perfect.
type ScoredRow struct {
	TxID    obs.TxID    `json:"tx"`
	Tick    obs.Tick    `json:"t"`
	From    obs.AgentID `json:"f"`
	// To is the receiver. Carried because several statistics are defined by
	// who RECEIVES a payment, not who sends it — NPCI counts a transaction as
	// person-to-merchant by its destination, and fan-in concentration is a
	// property of the receiving account.
	To      obs.AgentID `json:"to"`
	AmountP int64       `json:"a"`
	// Failed marks a transaction the bank did not settle. Needed to describe
	// an infrastructure incident, where the story is failures and retries
	// rather than fraud.
	Failed bool `json:"failed,omitempty"`

	// Score is the highest score any detector gave this transaction, and
	// Detector names which one. Zero means nothing flagged it.
	Score float64 `json:"s"`
	// Raw is the fused score before calibration. Kept alongside Score so a
	// calibrated report can still be compared against an uncalibrated one.
	Raw      float64 `json:"raw,omitempty"`
	Detector string  `json:"d,omitempty"`

	// Contributions is each detector's signed push on the fused score — the
	// evidence behind the number, carried through to the explainability panel.
	Contributions map[string]float64 `json:"contrib,omitempty"`

	Label     truth.Label      `json:"l"`
	Archetype truth.Archetype  `json:"ar"`
	Incident  truth.IncidentID `json:"inc,omitempty"`

	// Intensity is how far the attack had ramped when this transaction was
	// created, in [0,1]. Ground truth, recorded so recall can be reported
	// against attack strength rather than averaged over the whole episode.
	Intensity float64 `json:"int,omitempty"`
}

// Fraudulent reports whether this row is a true positive when flagged.
func (r ScoredRow) Fraudulent() bool { return r.Label.Fraudulent() }

// Rows is a full run's worth of scored transactions.
type Rows []ScoredRow

// Prevalence is the fraction of transactions that were actually fraudulent.
func (rs Rows) Prevalence() float64 {
	if len(rs) == 0 {
		return 0
	}
	var n int
	for _, r := range rs {
		if r.Fraudulent() {
			n++
		}
	}
	return float64(n) / float64(len(rs))
}

// Positives counts fraudulent transactions.
func (rs Rows) Positives() int {
	var n int
	for _, r := range rs {
		if r.Fraudulent() {
			n++
		}
	}
	return n
}
