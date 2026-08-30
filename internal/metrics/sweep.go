package metrics

import (
	"sort"
)

// Operating is detector performance at one threshold.
type Operating struct {
	Tau float64 `json:"tau"`

	TP int `json:"tp"`
	FP int `json:"fp"`
	FN int `json:"fn"`
	TN int `json:"tn"`

	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`

	// FPPer1kLegit is the headline cost figure: how many legitimate
	// transactions are flagged per thousand.
	//
	// Reported instead of the false positive rate because at ~1% prevalence
	// true negatives dominate so heavily that FPR is a meaningless 0.003 no
	// matter how bad the detector is. "Three good customers blocked per
	// thousand" is a number a risk team can actually budget against.
	FPPer1kLegit float64 `json:"fp_per_1k_legit"`

	// FlagRate is the share of all traffic flagged — the operational load.
	FlagRate float64 `json:"flag_rate"`
}

// DefaultTaus returns the standard threshold grid.
func DefaultTaus() []float64 {
	taus := make([]float64, 0, 101)
	for i := 0; i <= 100; i++ {
		taus = append(taus, float64(i)/100)
	}
	return taus
}

// Sweep computes performance across a grid of thresholds.
//
// The sweep runs OFFLINE, over scores already recorded, so the curve describes
// exactly the run being demonstrated rather than a separate one. Re-running
// the simulation per threshold would be both slow and quietly dishonest: the
// curve and the demo would no longer be the same experiment.
func Sweep(rows Rows, taus []float64) []Operating {
	if len(rows) == 0 {
		return nil
	}

	// Sort by score descending once; every threshold is then a prefix.
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		return rows[idx[a]].Score > rows[idx[b]].Score
	})

	totalPos := rows.Positives()
	totalNeg := len(rows) - totalPos

	out := make([]Operating, 0, len(taus))
	pos := 0 // cumulative true positives in the prefix
	seen := 0
	cursor := 0

	// Walk thresholds from high to low so the prefix only ever grows.
	sortedTaus := append([]float64(nil), taus...)
	sort.Sort(sort.Reverse(sort.Float64Slice(sortedTaus)))

	for _, tau := range sortedTaus {
		for cursor < len(idx) && rows[idx[cursor]].Score >= tau {
			if rows[idx[cursor]].Fraudulent() {
				pos++
			}
			seen++
			cursor++
		}

		tp, fp := pos, seen-pos
		fn, tn := totalPos-tp, totalNeg-fp

		op := Operating{Tau: tau, TP: tp, FP: fp, FN: fn, TN: tn}
		if tp+fp > 0 {
			op.Precision = float64(tp) / float64(tp+fp)
		}
		if totalPos > 0 {
			op.Recall = float64(tp) / float64(totalPos)
		}
		if op.Precision+op.Recall > 0 {
			op.F1 = 2 * op.Precision * op.Recall / (op.Precision + op.Recall)
		}
		if totalNeg > 0 {
			op.FPPer1kLegit = 1000 * float64(fp) / float64(totalNeg)
		}
		op.FlagRate = float64(seen) / float64(len(rows))
		out = append(out, op)
	}

	// Return in ascending tau order, which is how a curve is read.
	sort.Slice(out, func(a, b int) bool { return out[a].Tau < out[b].Tau })
	return out
}

// AUCPR is average precision: the step-wise area under the precision/recall
// curve, sum of (ΔRecall × Precision).
//
// Chosen over ROC-AUC deliberately. At ~1% prevalence ROC-AUC is dominated by
// the enormous true-negative mass and stays flatteringly high for detectors
// that are useless in practice; precision/recall keeps the focus on the rare
// class that actually matters.
//
// Step-wise rather than trapezoidal, for two reasons. Trapezoid interpolation
// between operating points is known to be optimistically biased on PR curves —
// it invents achievable performance between thresholds that may not exist. And
// the trapezoid form is degenerate for coarse scorers: a binary 0/1 classifier
// has a single operating point, so every ΔRecall is zero and the area comes
// out as 0.0 for a scorer that is in fact PERFECT. That failure was found by
// asserting a known-perfect scorer must score 1.0.
func AUCPR(ops []Operating) float64 {
	if len(ops) == 0 {
		return 0
	}
	pts := append([]Operating(nil), ops...)
	sort.Slice(pts, func(a, b int) bool {
		if pts[a].Recall != pts[b].Recall {
			return pts[a].Recall < pts[b].Recall
		}
		return pts[a].Precision > pts[b].Precision
	})

	// Collapse ties in recall, keeping the best precision achievable there.
	best := make([]Operating, 0, len(pts))
	for _, p := range pts {
		if n := len(best); n > 0 && best[n-1].Recall == p.Recall {
			if p.Precision > best[n-1].Precision {
				best[n-1] = p
			}
			continue
		}
		best = append(best, p)
	}

	var ap, prevRecall float64
	for _, p := range best {
		if p.TP+p.FP == 0 {
			continue // nothing flagged: precision undefined, not zero
		}
		if d := p.Recall - prevRecall; d > 0 {
			ap += d * p.Precision
		}
		prevRecall = p.Recall
	}
	return ap
}

// BestF1 returns the operating point with the highest F1.
func BestF1(ops []Operating) Operating {
	var best Operating
	for _, o := range ops {
		if o.F1 > best.F1 {
			best = o
		}
	}
	return best
}

// AtReviewBudget returns the highest-recall operating point whose flag rate
// stays within a budget — the way a risk team actually picks a threshold,
// since the review queue is staffed by humans.
func AtReviewBudget(ops []Operating, budget float64) Operating {
	var best Operating
	found := false
	for _, o := range ops {
		if o.FlagRate <= budget && (!found || o.Recall > best.Recall) {
			best, found = o, true
		}
	}
	return best
}
