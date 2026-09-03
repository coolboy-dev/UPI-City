package metrics

import (
	"fmt"
	"math/rand/v2"
	"slices"
)

// Baseline is a trivial scorer used as a reference point.
//
// A precision/recall curve on its own is decoration: without something to
// compare against, an AUC-PR of 0.34 could be excellent or worthless and the
// reader has no way to tell. These three answer that.
type Baseline struct {
	Name  string      `json:"name"`
	Why   string      `json:"why"`
	AUCPR float64     `json:"aucpr"`
	Best  Operating   `json:"best_f1"`
	Ops   []Operating `json:"-"`
}

// Baselines scores the same transactions three trivial ways.
func Baselines(rows Rows, taus []float64, seed uint64) []Baseline {
	return []Baseline{
		randomBaseline(rows, taus, seed),
		alwaysFlagBaseline(rows, taus),
		bigAmountBaseline(rows, taus),
	}
}

// randomBaseline is the floor. Its AUC-PR equals the prevalence, which is the
// number any real detector must beat to have done anything at all.
func randomBaseline(rows Rows, taus []float64, seed uint64) Baseline {
	rng := rand.New(rand.NewPCG(seed, seed*0x9E3779B97F4A7C15|1))
	cp := make(Rows, len(rows))
	copy(cp, rows)
	for i := range cp {
		cp[i].Score = rng.Float64()
	}
	ops := Sweep(cp, taus)
	return Baseline{
		Name:  "random",
		Why:   "AUC-PR equals prevalence; anything at or below this has learned nothing",
		AUCPR: AUCPR(ops), Best: BestF1(ops), Ops: ops,
	}
}

// alwaysFlagBaseline flags everything: perfect recall, precision equal to
// prevalence. It exists because "we catch 100% of fraud" is a claim that
// sounds impressive until the review queue is the whole payment network.
func alwaysFlagBaseline(rows Rows, taus []float64) Baseline {
	cp := make(Rows, len(rows))
	copy(cp, rows)
	for i := range cp {
		cp[i].Score = 1
	}
	ops := Sweep(cp, taus)
	return Baseline{
		Name:  "always-flag",
		Why:   "perfect recall at the cost of reviewing every transaction in the network",
		AUCPR: AUCPR(ops), Best: BestF1(ops), Ops: ops,
	}
}

// bigAmountBaseline is the rule a reasonable person writes first: big payments
// are suspicious. It is the honest bar to clear, because if three stateful
// detectors cannot beat "sort by amount" then they are not earning their
// complexity.
//
// It scores each transaction by where its amount falls in the run's OWN amount
// distribution, so tau=0.99 is exactly "flag the largest 1%".
//
// ─── Why a rank and not a fixed threshold ───────────────────────────────────
//
// This was `amount >= ₹50,000` and that was wrong twice over.
//
// It looked inert. Its AUC-PR came out bit-for-bit identical to always-flag's
// on all ten seeds, which reads like a rule that never fires. It fires 1,082
// times in a 20k-tick run — and is wrong on every single one. Everything above
// ₹50,000 in this simulator is payroll or supply-chain settlement; the largest
// FRAUDULENT payment is ₹48,504 against a largest legitimate one of ₹1.77M.
// So the rule had zero true positives at every threshold, and the only
// operating point it retained was "flag everything". A baseline that is
// maximally wrong and a baseline that is switched off produce the same
// headline number, and telling them apart matters.
//
// It was also fragile: ₹48,504 against ₹50,000 is a 3% margin. Any nudge to
// the fraud amount distribution moves the reported figure for reasons that
// have nothing to do with detection.
//
// And a rupee-denominated constant is meaningless the moment these metrics run
// against a dataset denominated in anything else, which the real-data path
// does.
//
// Ranking fixes all three. It cannot degenerate, it cannot be a coincidence of
// where one constant landed, and it carries across currencies unchanged.
func bigAmountBaseline(rows Rows, taus []float64) Baseline {
	sorted := make([]int64, len(rows))
	for i, r := range rows {
		sorted[i] = r.AmountP
	}
	slices.Sort(sorted)

	cp := make(Rows, len(rows))
	copy(cp, rows)
	n := float64(len(sorted))
	for i := range cp {
		// Equal amounts must score equally, so rank on the count of strictly
		// smaller amounts rather than on position in the sorted slice.
		below, _ := slices.BinarySearch(sorted, cp[i].AmountP)
		cp[i].Score = float64(below) / n
	}
	ops := Sweep(cp, taus)

	// Report where the top 1% actually begins, so the rule stays legible as a
	// threshold a person could have written even though it is scored as a rank.
	cut := sorted[len(sorted)*99/100]
	return Baseline{
		Name: "amount-rank",
		Why: fmt.Sprintf("sort by amount alone; the top 1%% here begins at ₹%d — "+
			"stateful detectors must beat this to justify themselves", cut/100),
		AUCPR: AUCPR(ops), Best: BestF1(ops), Ops: ops,
	}
}
