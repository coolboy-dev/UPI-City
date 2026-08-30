package metrics

import (
	"sort"

	"github.com/yug/upi-city/internal/decide"
)

// DecisionPoint is the full cost of one allow/review/block policy.
type DecisionPoint struct {
	Policy decide.Policy `json:"policy"`

	// Volumes.
	Blocked  int `json:"blocked"`
	Reviewed int `json:"reviewed"`
	Allowed  int `json:"allowed"`

	// Correctness of each action.
	FraudBlocked  int `json:"fraud_blocked"`
	FraudReviewed int `json:"fraud_reviewed"`
	FraudAllowed  int `json:"fraud_allowed"`

	// BlockPrecision is the share of blocked transactions that really were
	// fraud. This is the number that protects customers: every point of
	// imprecision here is a real person whose payment was stopped.
	BlockPrecision float64 `json:"block_precision"`
	// FalseBlockPer1k is the same fact stated as harm done: legitimate
	// transactions blocked per thousand legitimate transactions.
	FalseBlockPer1k float64 `json:"false_block_per_1k_legit"`

	// ReviewRate is the share of ALL traffic queued for a human. This is a
	// staffing budget, not a free parameter.
	ReviewRate float64 `json:"review_rate"`
	// ReviewPrecision is the hit rate an analyst experiences.
	ReviewPrecision float64 `json:"review_precision"`

	// Caught is the share of fraud stopped or queued — the headline
	// effectiveness number, since a reviewed fraud is a caught fraud.
	Caught float64 `json:"caught"`
	// Missed is fraud that sailed straight through.
	Missed float64 `json:"missed"`
}

// cumulative lets any threshold be answered in constant time.
//
// Rows are sorted by score once; a threshold then names a prefix. Without
// this the two-dimensional sweep would rescan every transaction for every
// pair of thresholds — hundreds of millions of comparisons to produce a
// surface that is really just arithmetic on prefix sums.
type cumulative struct {
	taus       []float64
	flagged    []int // rows with score >= taus[i]
	fraudAt    []int // of those, how many were fraudulent
	total      int
	totalFraud int
}

func newCumulative(rows Rows, taus []float64) cumulative {
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return rows[idx[a]].Score > rows[idx[b]].Score })

	sorted := append([]float64(nil), taus...)
	sort.Sort(sort.Reverse(sort.Float64Slice(sorted)))

	c := cumulative{
		taus:       make([]float64, 0, len(sorted)),
		flagged:    make([]int, 0, len(sorted)),
		fraudAt:    make([]int, 0, len(sorted)),
		total:      len(rows),
		totalFraud: rows.Positives(),
	}

	cursor, seen, fraud := 0, 0, 0
	for _, tau := range sorted {
		for cursor < len(idx) && rows[idx[cursor]].Score >= tau {
			if rows[idx[cursor]].Fraudulent() {
				fraud++
			}
			seen++
			cursor++
		}
		c.taus = append(c.taus, tau)
		c.flagged = append(c.flagged, seen)
		c.fraudAt = append(c.fraudAt, fraud)
	}
	return c
}

// at returns (flagged, fraudFlagged) at a threshold.
func (c cumulative) at(tau float64) (int, int) {
	// taus descend, so find the last entry at or above the query.
	best := 0
	for i, t := range c.taus {
		if t <= tau {
			best = i
			break
		}
		best = i
	}
	return c.flagged[best], c.fraudAt[best]
}

// SweepDecisions evaluates every ordered pair of thresholds.
func SweepDecisions(rows Rows, taus []float64) []DecisionPoint {
	if len(rows) == 0 {
		return nil
	}
	c := newCumulative(rows, taus)
	legit := c.total - c.totalFraud

	asc := append([]float64(nil), taus...)
	sort.Float64s(asc)

	out := make([]DecisionPoint, 0, len(asc)*len(asc)/2)
	for _, tr := range asc {
		for _, tb := range asc {
			if tb < tr {
				continue // a block threshold below review is a misconfiguration
			}
			flaggedR, fraudR := c.at(tr)
			flaggedB, fraudB := c.at(tb)

			p := DecisionPoint{
				Policy:        decide.Policy{TauReview: tr, TauBlock: tb},
				Blocked:       flaggedB,
				Reviewed:      flaggedR - flaggedB,
				Allowed:       c.total - flaggedR,
				FraudBlocked:  fraudB,
				FraudReviewed: fraudR - fraudB,
				FraudAllowed:  c.totalFraud - fraudR,
			}
			if p.Blocked > 0 {
				p.BlockPrecision = float64(p.FraudBlocked) / float64(p.Blocked)
			}
			if legit > 0 {
				p.FalseBlockPer1k = 1000 * float64(p.Blocked-p.FraudBlocked) / float64(legit)
			}
			if c.total > 0 {
				p.ReviewRate = float64(p.Reviewed) / float64(c.total)
			}
			if p.Reviewed > 0 {
				p.ReviewPrecision = float64(p.FraudReviewed) / float64(p.Reviewed)
			}
			if c.totalFraud > 0 {
				p.Caught = float64(p.FraudBlocked+p.FraudReviewed) / float64(c.totalFraud)
				p.Missed = float64(p.FraudAllowed) / float64(c.totalFraud)
			}
			out = append(out, p)
		}
	}
	return out
}

// BestUnderBudget picks the policy a risk team would actually deploy.
//
// Two hard constraints, then maximise what is caught:
//
//  1. The review queue must fit the budget — it is staffed by humans.
//  2. Block precision must clear a floor. Blocking is customer-visible and
//     expensive, so a policy that blocks aggressively at 30% precision is
//     not a better policy, it is a worse product.
//
// Only after both are satisfied does catching more fraud become the
// objective. Optimising a single blended score instead would happily trade
// away either constraint for a fraction of a point of recall.
func BestUnderBudget(pts []DecisionPoint, reviewBudget, minBlockPrecision float64) (DecisionPoint, bool) {
	var best DecisionPoint
	found := false
	for _, p := range pts {
		if p.ReviewRate > reviewBudget {
			continue
		}
		if p.Blocked > 0 && p.BlockPrecision < minBlockPrecision {
			continue
		}
		if !found || p.Caught > best.Caught {
			best, found = p, true
		}
	}
	return best, found
}

// BudgetCurve reports the best achievable catch rate at a range of review
// budgets — the trade a risk team is actually negotiating.
type BudgetCurve struct {
	Budget          float64 `json:"budget"`
	Caught          float64 `json:"caught"`
	BlockPrecision  float64 `json:"block_precision"`
	FalseBlockPer1k float64 `json:"false_block_per_1k"`
	TauReview       float64 `json:"tau_review"`
	TauBlock        float64 `json:"tau_block"`
	Feasible        bool    `json:"feasible"`
}

// Budgets evaluates a set of review budgets against a block-precision floor.
func Budgets(pts []DecisionPoint, budgets []float64, minBlockPrecision float64) []BudgetCurve {
	out := make([]BudgetCurve, 0, len(budgets))
	for _, b := range budgets {
		c := BudgetCurve{Budget: b}
		if p, ok := BestUnderBudget(pts, b, minBlockPrecision); ok {
			c.Feasible = true
			c.Caught = p.Caught
			c.BlockPrecision = p.BlockPrecision
			c.FalseBlockPer1k = p.FalseBlockPer1k
			c.TauReview = p.Policy.TauReview
			c.TauBlock = p.Policy.TauBlock
		}
		out = append(out, c)
	}
	return out
}

// DefaultBudgets is the range a payments risk team would consider.
func DefaultBudgets() []float64 {
	return []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.10}
}
