package metrics

import "math/rand/v2"

// PermutationControl is the result of scoring against shuffled labels.
type PermutationControl struct {
	Trials     int     `json:"trials"`
	Prevalence float64 `json:"prevalence"`
	RealAUCPR  float64 `json:"real_aucpr"`

	MeanAUCPR float64 `json:"permuted_mean_aucpr"`
	MaxAUCPR  float64 `json:"permuted_max_aucpr"`

	// Passed is true when permuted performance collapses to chance, which is
	// what it must do if the detectors truly never saw the labels.
	Passed bool   `json:"passed"`
	Note   string `json:"note"`
}

// Permute is the negative control on the EVALUATION, not on the detectors.
//
// Be precise about what this does and does not establish, because the
// tempting overclaim is wrong:
//
//	IT DOES establish that the reported figure genuinely depends on the
//	labels. Keep every score exactly as computed, shuffle the labels among
//	the transactions, and re-run the evaluation. Destroying the
//	correspondence must collapse AUC-PR to the base rate. If it does not,
//	the number is not measuring an association at all — a sorting bug, an
//	off-by-one counting everything as a true positive, a metric that ignores
//	its inputs — and the report is worthless for reasons that have nothing
//	to do with the detectors.
//
//	IT DOES NOT establish that the detectors never saw the labels. A
//	detector that simply read the answer would show a strong association,
//	and permuting afterwards would destroy that association exactly as it
//	destroys a legitimate one. This control cannot tell those apart.
//
// The claim it cannot make is made elsewhere, structurally:
// internal/detect/boundary_test.go walks the dependency graph and fails the
// build if the detection package can so much as name the label type. The two
// checks are complementary and neither is sufficient alone — one constrains
// what the detectors can see, the other constrains what the arithmetic can
// claim.
func Permute(rows Rows, taus []float64, trials int, seed uint64) PermutationControl {
	real := AUCPR(Sweep(rows, taus))
	prev := rows.Prevalence()

	pc := PermutationControl{
		Trials: trials, Prevalence: prev, RealAUCPR: real,
	}
	if len(rows) == 0 || trials <= 0 {
		return pc
	}

	labels := make([]bool, len(rows))
	for i, r := range rows {
		labels[i] = r.Fraudulent()
	}

	cp := make(Rows, len(rows))
	copy(cp, rows)

	rng := rand.New(rand.NewPCG(seed^0xDEADBEEF, seed*0x9E3779B97F4A7C15|1))
	var sum float64
	for tr := 0; tr < trials; tr++ {
		// Fisher-Yates over the labels only. Scores stay attached to the
		// transactions that produced them.
		perm := append([]bool(nil), labels...)
		for i := len(perm) - 1; i > 0; i-- {
			j := rng.IntN(i + 1)
			perm[i], perm[j] = perm[j], perm[i]
		}
		for i := range cp {
			if perm[i] {
				cp[i].Label = 2 // any fraudulent label; only Fraudulent() is read
			} else {
				cp[i].Label = 0
			}
		}
		a := AUCPR(Sweep(cp, taus))
		sum += a
		if a > pc.MaxAUCPR {
			pc.MaxAUCPR = a
		}
	}
	pc.MeanAUCPR = sum / float64(trials)

	// Permuted performance must land at chance, within tolerance.
	tol := 0.02 + 0.5*prev
	pc.Passed = pc.MeanAUCPR <= prev+tol && real > pc.MaxAUCPR
	if pc.Passed {
		pc.Note = "permuted AUC-PR collapses to prevalence: the reported figure genuinely " +
			"depends on the labels (this validates the metric, not the detectors' independence)"
	} else {
		pc.Note = "PERMUTED PERFORMANCE DID NOT COLLAPSE — the evaluation is not measuring " +
			"an association between scores and labels; treat every figure in this report as suspect"
	}
	return pc
}
