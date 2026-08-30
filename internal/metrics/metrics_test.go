package metrics

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

func rows(n int, prevalence float64, scorer func(fraud bool, rng *rand.Rand) float64) Rows {
	rng := rand.New(rand.NewPCG(1, 2))
	out := make(Rows, 0, n)
	for i := 0; i < n; i++ {
		fraud := rng.Float64() < prevalence
		lb := truth.LabelNormal
		if fraud {
			lb = truth.LabelRingMule
		}
		out = append(out, ScoredRow{
			TxID: obs.TxID(i + 1), Tick: obs.Tick(i),
			AmountP: 1000, Label: lb, Score: scorer(fraud, rng),
		})
	}
	return out
}

// TestPerfectSeparationScoresOne is the upper anchor: a scorer that separates
// the classes completely must reach AUC-PR 1.
func TestPerfectSeparationScoresOne(t *testing.T) {
	rs := rows(5000, 0.02, func(fraud bool, rng *rand.Rand) float64 {
		if fraud {
			return 0.9 + 0.1*rng.Float64()
		}
		return 0.4 * rng.Float64()
	})
	auc := AUCPR(Sweep(rs, DefaultTaus()))
	if auc < 0.95 {
		t.Errorf("perfect separation gave AUC-PR %.3f, want ~1.0", auc)
	}
}

// TestRandomScorerEqualsPrevalence is the lower anchor, and it is the number
// every reported figure must be read against: a coin flip achieves AUC-PR
// equal to the base rate, so anything near prevalence has learned nothing.
func TestRandomScorerEqualsPrevalence(t *testing.T) {
	const prev = 0.02
	rs := rows(20000, prev, func(_ bool, rng *rand.Rand) float64 { return rng.Float64() })
	auc := AUCPR(Sweep(rs, DefaultTaus()))
	if math.Abs(auc-prev) > 0.02 {
		t.Errorf("random scorer gave AUC-PR %.4f, want ≈ prevalence %.4f", auc, prev)
	}
}

// TestSweepCountsAreConsistent checks the confusion matrix is internally
// coherent at every threshold — the arithmetic every other metric rests on.
func TestSweepCountsAreConsistent(t *testing.T) {
	rs := rows(3000, 0.05, func(fraud bool, rng *rand.Rand) float64 {
		if fraud {
			return rng.Float64()*0.6 + 0.3
		}
		return rng.Float64() * 0.7
	})
	total := len(rs)
	pos := rs.Positives()

	for _, op := range Sweep(rs, DefaultTaus()) {
		if op.TP+op.FP+op.FN+op.TN != total {
			t.Fatalf("tau=%.2f: confusion matrix sums to %d, want %d",
				op.Tau, op.TP+op.FP+op.FN+op.TN, total)
		}
		if op.TP+op.FN != pos {
			t.Fatalf("tau=%.2f: TP+FN = %d, want %d positives", op.Tau, op.TP+op.FN, pos)
		}
		if op.Precision < 0 || op.Precision > 1 || op.Recall < 0 || op.Recall > 1 {
			t.Fatalf("tau=%.2f: precision/recall out of range: %.3f / %.3f",
				op.Tau, op.Precision, op.Recall)
		}
	}
}

// TestRecallIsMonotonic guards the sweep's prefix logic: lowering the
// threshold can only ever flag more, so recall must not decrease.
func TestRecallIsMonotonic(t *testing.T) {
	rs := rows(4000, 0.03, func(fraud bool, rng *rand.Rand) float64 {
		if fraud {
			return rng.Float64()
		}
		return rng.Float64() * 0.5
	})
	ops := Sweep(rs, DefaultTaus())
	for i := 1; i < len(ops); i++ {
		if ops[i].Recall > ops[i-1].Recall+1e-9 {
			t.Fatalf("recall rose as tau rose: tau %.2f→%.2f gave recall %.3f→%.3f",
				ops[i-1].Tau, ops[i].Tau, ops[i-1].Recall, ops[i].Recall)
		}
	}
}

// TestNeverDetectedIsCounted is the anti-dishonesty test.
//
// Reporting "median latency 3s" while quietly dropping the incidents that
// were never caught is the classic way to make a weak detector look strong.
// An undetected incident must be counted, and must not contribute a zero to
// the median.
func TestNeverDetectedIsCounted(t *testing.T) {
	incidents := []truth.Incident{
		{ID: 1, Kind: "caught", StartTick: 0},
		{ID: 2, Kind: "missed", StartTick: 0},
	}
	rs := Rows{
		{TxID: 1, Tick: 10, Label: truth.LabelRingMule, Incident: 1, Score: 0.9},
		{TxID: 2, Tick: 20, Label: truth.LabelRingMule, Incident: 2, Score: 0.1},
	}

	rep := Latency(rs, incidents, 0.5, 100)
	if rep.NeverDetected != 1 {
		t.Errorf("NeverDetected = %d, want 1", rep.NeverDetected)
	}
	if rep.Total != 2 {
		t.Errorf("Total = %d, want 2", rep.Total)
	}
	if rep.MedianTicks != 10 {
		t.Errorf("median = %d ticks, want 10 — the undetected incident must not "+
			"contribute a zero and drag the median down", rep.MedianTicks)
	}
}

// TestPermutationControlValidatesTheMetric pins down what the control
// actually establishes, and — just as importantly — what it does not.
//
// It establishes that the reported figure depends on the labels: shuffle them
// and performance must collapse to the base rate. It does NOT establish that
// the detectors never saw the labels, because a detector that read the answer
// outright would show a strong association and permuting would destroy that
// association exactly as it destroys a legitimate one. Both cases are asserted
// here so the distinction cannot quietly rot into the stronger claim.
func TestPermutationControlValidatesTheMetric(t *testing.T) {
	// A strong, honest scorer: real performance far above its permutations.
	strong := rows(8000, 0.03, func(fraud bool, rng *rand.Rand) float64 {
		if fraud {
			return 0.7 + 0.3*rng.Float64()
		}
		return 0.3 * rng.Float64()
	})
	pc := Permute(strong, DefaultTaus(), 3, 7)
	if pc.MeanAUCPR > pc.Prevalence+0.05 {
		t.Errorf("permuted AUC-PR %.3f did not collapse to prevalence %.3f",
			pc.MeanAUCPR, pc.Prevalence)
	}
	if !pc.Passed {
		t.Errorf("a strong scorer should pass the control; got %q", pc.Note)
	}

	// A scorer that reads the label outright ALSO passes. This is the
	// control's blind spot, asserted deliberately: only the dependency-graph
	// test in internal/detect can rule this case out.
	leaky := rows(8000, 0.03, func(fraud bool, _ *rand.Rand) float64 {
		if fraud {
			return 1
		}
		return 0
	})
	if lp := Permute(leaky, DefaultTaus(), 3, 7); !lp.Passed {
		t.Errorf("expected the permutation control to be blind to a label-reading scorer; "+
			"if this now fails the control has become stronger than documented: %q", lp.Note)
	}

	// Pure noise must show no advantage over its own permutations.
	noise := rows(8000, 0.03, func(_ bool, rng *rand.Rand) float64 { return rng.Float64() })
	npc := Permute(noise, DefaultTaus(), 3, 7)
	if npc.RealAUCPR > npc.MaxAUCPR+0.05 {
		t.Errorf("a pure-noise scorer beat its own permutations: %.3f vs %.3f",
			npc.RealAUCPR, npc.MaxAUCPR)
	}
}

// TestReviewBudgetRespectsTheBudget checks the operating point a risk team
// would actually pick: the most recall available without exceeding the share
// of traffic humans can review.
func TestReviewBudgetRespectsTheBudget(t *testing.T) {
	rs := rows(10000, 0.02, func(fraud bool, rng *rand.Rand) float64 {
		if fraud {
			return 0.5 + 0.5*rng.Float64()
		}
		return rng.Float64() * 0.6
	})
	ops := Sweep(rs, DefaultTaus())
	const budget = 0.01
	op := AtReviewBudget(ops, budget)
	if op.FlagRate > budget+1e-9 {
		t.Errorf("chosen operating point flags %.3f%% of traffic, over the %.1f%% budget",
			op.FlagRate*100, budget*100)
	}
}
