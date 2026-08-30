package metrics

import "math/rand/v2"

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

// bigAmountBaseline is the rule a reasonable person writes first: flag large
// payments. It is the honest bar to clear, because if four stateful detectors
// cannot beat "amount > ₹50,000" then they are not earning their complexity.
func bigAmountBaseline(rows Rows, taus []float64) Baseline {
	const thresholdP = 5_000_000 // ₹50,000
	cp := make(Rows, len(rows))
	copy(cp, rows)
	var maxAmt int64 = 1
	for _, r := range rows {
		if r.AmountP > maxAmt {
			maxAmt = r.AmountP
		}
	}
	for i := range cp {
		if cp[i].AmountP >= thresholdP {
			// Rank larger amounts higher so the curve has a shape rather than
			// collapsing to a single point.
			cp[i].Score = 0.5 + 0.5*float64(cp[i].AmountP)/float64(maxAmt)
		} else {
			cp[i].Score = 0
		}
	}
	ops := Sweep(cp, taus)
	return Baseline{
		Name:  "amount>50k",
		Why:   "the obvious first rule; stateful detectors must beat it to justify themselves",
		AUCPR: AUCPR(ops), Best: BestF1(ops), Ops: ops,
	}
}
