package metrics

import (
	"math/rand/v2"
	"testing"

	"github.com/yug/upi-city/internal/decide"
	"github.com/yug/upi-city/internal/truth"
)

func decisionRows(n int, prevalence float64) Rows {
	rng := rand.New(rand.NewPCG(31, 32))
	out := make(Rows, 0, n)
	for i := 0; i < n; i++ {
		fraud := rng.Float64() < prevalence
		lb := truth.LabelNormal
		s := rng.Float64() * 0.5
		if fraud {
			lb = truth.LabelRingMule
			s = 0.4 + rng.Float64()*0.6
		}
		out = append(out, ScoredRow{Label: lb, Score: s})
	}
	return out
}

// TestDecisionVolumesAreExhaustive checks the three actions partition the
// traffic. Every transaction gets exactly one outcome; if they did not sum,
// some payments would be silently unaccounted for.
func TestDecisionVolumesAreExhaustive(t *testing.T) {
	rows := decisionRows(20000, 0.02)
	total, fraud := len(rows), rows.Positives()

	for _, p := range SweepDecisions(rows, DefaultTaus()) {
		if p.Blocked+p.Reviewed+p.Allowed != total {
			t.Fatalf("policy %+v: volumes sum to %d, want %d",
				p.Policy, p.Blocked+p.Reviewed+p.Allowed, total)
		}
		if p.FraudBlocked+p.FraudReviewed+p.FraudAllowed != fraud {
			t.Fatalf("policy %+v: fraud sums to %d, want %d",
				p.Policy, p.FraudBlocked+p.FraudReviewed+p.FraudAllowed, fraud)
		}
		if p.Caught+p.Missed < 0.999 || p.Caught+p.Missed > 1.001 {
			t.Fatalf("policy %+v: caught+missed = %.4f, want 1", p.Policy, p.Caught+p.Missed)
		}
	}
}

// TestBlockThresholdNeverBelowReview guards the ordering invariant. A block
// threshold under the review threshold would block transactions that were
// never suspicious enough to look at.
func TestBlockThresholdNeverBelowReview(t *testing.T) {
	for _, p := range SweepDecisions(decisionRows(3000, 0.03), DefaultTaus()) {
		if !p.Policy.Valid() {
			t.Fatalf("swept an invalid policy: review=%.2f block=%.2f",
				p.Policy.TauReview, p.Policy.TauBlock)
		}
	}
}

// TestBudgetIsRespected asserts the review queue never exceeds what was asked
// for. The queue is staffed by humans, so this is a hard constraint rather
// than a preference.
func TestBudgetIsRespected(t *testing.T) {
	pts := SweepDecisions(decisionRows(20000, 0.02), DefaultTaus())
	for _, budget := range DefaultBudgets() {
		p, ok := BestUnderBudget(pts, budget, 0.9)
		if !ok {
			continue
		}
		if p.ReviewRate > budget+1e-9 {
			t.Errorf("budget %.3f: chosen policy reviews %.4f of traffic", budget, p.ReviewRate)
		}
		if p.Blocked > 0 && p.BlockPrecision < 0.9 {
			t.Errorf("budget %.3f: block precision %.3f is below the floor", budget, p.BlockPrecision)
		}
	}
}

// TestBiggerBudgetNeverCatchesLess checks monotonicity. More reviewer capacity
// is strictly more optionality, so the achievable catch rate must not fall.
func TestBiggerBudgetNeverCatchesLess(t *testing.T) {
	pts := SweepDecisions(decisionRows(20000, 0.02), DefaultTaus())
	curve := Budgets(pts, DefaultBudgets(), 0.9)

	var prev float64
	for _, c := range curve {
		if !c.Feasible {
			continue
		}
		if c.Caught < prev-1e-9 {
			t.Errorf("catch rate fell from %.4f to %.4f as the review budget grew to %.3f",
				prev, c.Caught, c.Budget)
		}
		prev = c.Caught
	}
}

// TestInfeasibilityIsReportedNotHidden checks that an impossible constraint
// produces an explicit "no safe policy" rather than a silently relaxed one.
//
// When a detector cannot block safely, the honest output is to say so. Quietly
// returning the least-bad policy would present an unsafe configuration as a
// recommendation.
func TestInfeasibilityIsReportedNotHidden(t *testing.T) {
	// Scores carry no signal at all, so no threshold can block precisely.
	rng := rand.New(rand.NewPCG(5, 6))
	rows := make(Rows, 0, 5000)
	for i := 0; i < 5000; i++ {
		lb := truth.LabelNormal
		if rng.Float64() < 0.02 {
			lb = truth.LabelRingMule
		}
		rows = append(rows, ScoredRow{Label: lb, Score: rng.Float64()})
	}

	pts := SweepDecisions(rows, DefaultTaus())
	if p, ok := BestUnderBudget(pts, 0.01, 0.99); ok && p.Blocked > 0 {
		t.Errorf("returned a blocking policy at %.3f precision against a 0.99 floor",
			p.BlockPrecision)
	}
}

// TestPolicyDecideMatchesThresholds pins the mapping itself.
func TestPolicyDecideMatchesThresholds(t *testing.T) {
	p := decide.Policy{TauReview: 0.2, TauBlock: 0.8}
	cases := []struct {
		score float64
		want  decide.Decision
	}{
		{0.00, decide.Allow}, {0.19, decide.Allow},
		{0.20, decide.Review}, {0.79, decide.Review},
		{0.80, decide.Block}, {1.00, decide.Block},
	}
	for _, c := range cases {
		if got := p.Decide(c.score); got != c.want {
			t.Errorf("score %.2f: got %v, want %v", c.score, got, c.want)
		}
	}
}

// TestRecallByIntensityIgnoresLegitimateTraffic checks the curve is computed
// over fraud only. Including legitimate transactions would dilute every
// bucket with rows that have no attack intensity at all.
func TestRecallByIntensityIgnoresLegitimateTraffic(t *testing.T) {
	rows := Rows{
		{Label: truth.LabelNormal, Intensity: 0, Score: 0.9},
		{Label: truth.LabelRingMule, Intensity: 0.15, Score: 0.9},
		{Label: truth.LabelRingMule, Intensity: 0.15, Score: 0.1},
		{Label: truth.LabelRingMule, Intensity: 0.95, Score: 0.9},
	}
	bs := RecallByIntensity(rows, 0.5, 10)

	var counted int
	for _, b := range bs {
		counted += b.Fraud
	}
	if counted != 3 {
		t.Errorf("counted %d fraudulent transactions across buckets, want 3", counted)
	}
	if bs[1].Fraud != 2 || bs[1].Caught != 1 || bs[1].Recall != 0.5 {
		t.Errorf("bucket 0.1-0.2: got fraud=%d caught=%d recall=%.2f, want 2/1/0.50",
			bs[1].Fraud, bs[1].Caught, bs[1].Recall)
	}
}
