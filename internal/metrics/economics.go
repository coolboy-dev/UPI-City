package metrics

import (
	"sort"

	"github.com/yug/upi-city/internal/decide"
)

// CostModel converts detector decisions into money.
//
// ─── Why counts were never enough ───────────────────────────────────────────
//
// Everything else in this project reports counts: transactions flagged, false
// positives per thousand, share of fraud caught. Those are the right units for
// comparing detectors, and the wrong units for deciding whether to deploy one.
// A risk team approves a policy on expected loss — rupees of fraud prevented,
// minus revenue destroyed by blocking real customers, minus what the review
// queue costs to staff.
//
// Every ingredient was already on hand: each transaction carries its amount.
// Not computing this was the largest gap between what the benchmark measured
// and what the decision actually turns on.
//
// ─── These numbers are assumptions, and are treated as such ─────────────────
//
// The three costs below cannot be derived from the simulation; they come from
// how a business is run. They are therefore exposed as parameters, defaulted
// to figures a payments risk team would recognise, and swept for sensitivity
// rather than quoted once. A single net-benefit figure resting on an
// unexamined cost assumption would be worse than reporting no money at all.
type CostModel struct {
	// ReviewRupees is the loaded analyst cost of looking at one queued
	// transaction. A few minutes of a person's time.
	ReviewRupees float64 `json:"review_rupees"`

	// FalseBlockRupees is what it costs to wrongly stop one legitimate
	// payment: a support contact, the operational handling, and a share of
	// the lifetime value of a customer who does not come back. This is the
	// most uncertain of the three and the one the sensitivity sweep exists
	// for.
	FalseBlockRupees float64 `json:"false_block_rupees"`

	// Recovery is the fraction of a blocked fraudulent payment's value that
	// is genuinely saved. Below 1.0 because stopping one payment in a
	// laundering chain does not always stop the loss.
	Recovery float64 `json:"recovery_rate"`

	// ReviewCatchRate is the share of fraud reaching a human that the human
	// actually catches. Reviewers are good, not perfect, and assuming 1.0
	// would quietly credit the detector with work it did not do.
	ReviewCatchRate float64 `json:"review_catch_rate"`
}

// DefaultCostModel returns figures a payments risk team would recognise.
func DefaultCostModel() CostModel {
	return CostModel{
		ReviewRupees:     35,
		FalseBlockRupees: 900,
		Recovery:         0.85,
		ReviewCatchRate:  0.90,
	}
}

// Economics is one policy's effect on the books.
type Economics struct {
	Policy decide.Policy `json:"policy"`
	Cost   CostModel     `json:"cost_model"`

	ProcessedRupees int64 `json:"processed_rupees"`
	FraudRupees     int64 `json:"fraud_rupees_total"`

	FraudBlockedRupees  int64 `json:"fraud_rupees_blocked"`
	FraudReviewedRupees int64 `json:"fraud_rupees_reviewed"`
	FraudAllowedRupees  int64 `json:"fraud_rupees_allowed"`
	LegitBlockedRupees  int64 `json:"legit_rupees_blocked"`

	Reviews     int `json:"reviews"`
	FalseBlocks int `json:"false_blocks"`

	// The four lines that make up the answer.
	Saved          float64 `json:"saved_rupees"`
	FalseBlockCost float64 `json:"false_block_cost_rupees"`
	ReviewCost     float64 `json:"review_cost_rupees"`
	Net            float64 `json:"net_rupees"`

	// NetPerCrore normalises by volume, which is how a payments business
	// compares policies across periods of different size.
	NetPerCrore float64 `json:"net_rupees_per_crore_processed"`

	// LossRateBefore and LossRateAfter are fraud losses as a fraction of
	// volume, with no detector and with this policy.
	LossRateBefore float64 `json:"loss_rate_no_detector"`
	LossRateAfter  float64 `json:"loss_rate_with_policy"`
}

// Evaluate computes the money for one policy over a run.
func (c CostModel) Evaluate(rows Rows, p decide.Policy) Economics {
	e := Economics{Policy: p, Cost: c}

	for _, r := range rows {
		rupees := r.AmountP / 100
		e.ProcessedRupees += rupees
		fraud := r.Fraudulent()
		if fraud {
			e.FraudRupees += rupees
		}

		switch p.Decide(r.Score) {
		case decide.Block:
			if fraud {
				e.FraudBlockedRupees += rupees
			} else {
				e.LegitBlockedRupees += rupees
				e.FalseBlocks++
			}
		case decide.Review:
			e.Reviews++
			if fraud {
				e.FraudReviewedRupees += rupees
			}
		default:
			if fraud {
				e.FraudAllowedRupees += rupees
			}
		}
	}

	// Blocked fraud is saved outright, discounted by recovery. Reviewed fraud
	// is saved only to the extent a human catches it — crediting the full
	// amount would hand the detector work the reviewer did.
	e.Saved = c.Recovery * (float64(e.FraudBlockedRupees) +
		c.ReviewCatchRate*float64(e.FraudReviewedRupees))

	e.FalseBlockCost = float64(e.FalseBlocks) * c.FalseBlockRupees
	e.ReviewCost = float64(e.Reviews) * c.ReviewRupees
	e.Net = e.Saved - e.FalseBlockCost - e.ReviewCost

	if e.ProcessedRupees > 0 {
		e.NetPerCrore = e.Net / (float64(e.ProcessedRupees) / 1e7)
		e.LossRateBefore = float64(e.FraudRupees) / float64(e.ProcessedRupees)

		missed := float64(e.FraudAllowedRupees) +
			(1-c.ReviewCatchRate)*float64(e.FraudReviewedRupees) +
			(1-c.Recovery)*float64(e.FraudBlockedRupees)
		e.LossRateAfter = missed / float64(e.ProcessedRupees)
	}
	return e
}

// valueCum is a prefix table over scores, carrying money as well as counts.
//
// The naive search rescanned every transaction for every pair of thresholds:
// 101 x 101 policies over a quarter of a million rows is 1.3 billion row
// visits per report, which turned a five-second benchmark into a five-minute
// one. Sorting once and answering each threshold from a prefix makes every
// policy an O(1) lookup, exactly as SweepDecisions already does for counts.
type valueCum struct {
	taus        []float64
	n           []int   // transactions at or above this threshold
	fraudN      []int   // of those, fraudulent
	fraudRupees []int64 // of those, fraudulent value
	legitN      []int   // of those, legitimate
	total       int
	totalFraudR int64
	processedR  int64
}

func newValueCum(rows Rows, taus []float64) valueCum {
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return rows[idx[a]].Score > rows[idx[b]].Score })

	desc := append([]float64(nil), taus...)
	sort.Sort(sort.Reverse(sort.Float64Slice(desc)))

	v := valueCum{total: len(rows)}
	for _, r := range rows {
		rupees := r.AmountP / 100
		v.processedR += rupees
		if r.Fraudulent() {
			v.totalFraudR += rupees
		}
	}

	cursor, seen, fraudN, legitN := 0, 0, 0, 0
	var fraudR int64
	for _, tau := range desc {
		for cursor < len(idx) && rows[idx[cursor]].Score >= tau {
			r := rows[idx[cursor]]
			if r.Fraudulent() {
				fraudN++
				fraudR += r.AmountP / 100
			} else {
				legitN++
			}
			seen++
			cursor++
		}
		v.taus = append(v.taus, tau)
		v.n = append(v.n, seen)
		v.fraudN = append(v.fraudN, fraudN)
		v.fraudRupees = append(v.fraudRupees, fraudR)
		v.legitN = append(v.legitN, legitN)
	}
	return v
}

// at returns the prefix index for a threshold. taus descend.
func (v valueCum) at(tau float64) int {
	best := 0
	for i, t := range v.taus {
		if t <= tau {
			return i
		}
		best = i
	}
	return best
}

// BestByNet finds the policy that maximises net rupees.
//
// Notably NOT the same policy that maximises F1 or recall. A threshold that
// catches more fraud while blocking more real customers can easily be worth
// less money, and this is the only place in the project where that trade is
// resolved in the units the business actually uses.
func (c CostModel) BestByNet(rows Rows, taus []float64) Economics {
	if len(rows) == 0 {
		return Economics{Cost: c}
	}
	v := newValueCum(rows, taus)

	asc := append([]float64(nil), taus...)
	sort.Float64s(asc)

	var best Economics
	found := false

	for _, tr := range asc {
		ir := v.at(tr)
		for _, tb := range asc {
			if tb < tr {
				continue
			}
			ib := v.at(tb)

			e := Economics{
				Policy: decide.Policy{TauReview: tr, TauBlock: tb}, Cost: c,
				ProcessedRupees:    v.processedR,
				FraudRupees:        v.totalFraudR,
				FraudBlockedRupees: v.fraudRupees[ib],
				FalseBlocks:        v.legitN[ib],
				Reviews:            v.n[ir] - v.n[ib],
			}
			e.FraudReviewedRupees = v.fraudRupees[ir] - v.fraudRupees[ib]
			e.FraudAllowedRupees = v.totalFraudR - v.fraudRupees[ir]

			e.Saved = c.Recovery * (float64(e.FraudBlockedRupees) +
				c.ReviewCatchRate*float64(e.FraudReviewedRupees))
			e.FalseBlockCost = float64(e.FalseBlocks) * c.FalseBlockRupees
			e.ReviewCost = float64(e.Reviews) * c.ReviewRupees
			e.Net = e.Saved - e.FalseBlockCost - e.ReviewCost

			if !found || e.Net > best.Net {
				best, found = e, true
			}
		}
	}

	if best.ProcessedRupees > 0 {
		best.NetPerCrore = best.Net / (float64(best.ProcessedRupees) / 1e7)
		best.LossRateBefore = float64(best.FraudRupees) / float64(best.ProcessedRupees)
		missed := float64(best.FraudAllowedRupees) +
			(1-c.ReviewCatchRate)*float64(best.FraudReviewedRupees) +
			(1-c.Recovery)*float64(best.FraudBlockedRupees)
		best.LossRateAfter = missed / float64(best.ProcessedRupees)
	}
	return best
}

// Sensitivity reports how the optimal policy moves as the least certain
// assumption — what a wrongly blocked customer costs — is varied.
//
// If the recommendation is stable across an order of magnitude, the cost
// model is not doing the deciding. If it swings, that has to be said out
// loud rather than buried under a single confident number.
type Sensitivity struct {
	FalseBlockRupees float64 `json:"false_block_rupees"`
	TauReview        float64 `json:"tau_review"`
	TauBlock         float64 `json:"tau_block"`
	NetPerCrore      float64 `json:"net_per_crore"`
	ReviewRate       float64 `json:"review_rate"`
}

// SweepFalseBlockCost varies the false-block cost and re-optimises.
func SweepFalseBlockCost(rows Rows, base CostModel, costs []float64, taus []float64) []Sensitivity {
	out := make([]Sensitivity, 0, len(costs))
	total := len(rows)
	for _, fb := range costs {
		c := base
		c.FalseBlockRupees = fb
		e := c.BestByNet(rows, taus)

		s := Sensitivity{
			FalseBlockRupees: fb,
			TauReview:        e.Policy.TauReview,
			TauBlock:         e.Policy.TauBlock,
			NetPerCrore:      e.NetPerCrore,
		}
		if total > 0 {
			s.ReviewRate = float64(e.Reviews) / float64(total)
		}
		out = append(out, s)
	}
	return out
}
