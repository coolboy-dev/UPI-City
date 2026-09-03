package metrics

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// amountRows builds rows whose amounts are drawn per class, so the amount
// baseline has something real to rank.
func amountRows(n int, prevalence float64, scale int64, amt func(fraud bool, rng *rand.Rand) int64) Rows {
	rng := rand.New(rand.NewPCG(7, 11))
	out := make(Rows, 0, n)
	for i := 0; i < n; i++ {
		fraud := rng.Float64() < prevalence
		lb := truth.LabelNormal
		if fraud {
			lb = truth.LabelRingMule
		}
		out = append(out, ScoredRow{
			TxID: obs.TxID(i + 1), Tick: obs.Tick(i),
			AmountP: amt(fraud, rng) * scale, Label: lb,
		})
	}
	return out
}

// TestAmountBaselineIsCurrencyIndependent is the property the old fixed
// ₹50,000 threshold did not have.
//
// Multiplying every amount by a constant is a change of units, not a change of
// information: the ranking is identical, so the score must be identical. The
// previous implementation failed this outright — scaling amounts down by 100
// moved every row below the constant and collapsed the baseline to always-flag.
// That matters because the real-data path scores US-dollar cents through this
// same function.
func TestAmountBaselineIsCurrencyIndependent(t *testing.T) {
	// The amounts must STRADDLE the old ₹50,000 constant, or this test is
	// vacuous: with everything below it the old code scored every row zero in
	// both unit systems and passed for the wrong reason. Straddling, the old
	// implementation moves by 0.0074 between the two scalings.
	draw := func(fraud bool, rng *rand.Rand) int64 {
		if fraud {
			return 100_000 + rng.Int64N(4_000_000)
		}
		return 50_000 + rng.Int64N(9_000_000)
	}
	taus := DefaultTaus()

	rupees := bigAmountBaseline(amountRows(20000, 0.02, 1, draw), taus)
	cents := bigAmountBaseline(amountRows(20000, 0.02, 137, draw), taus)

	if math.Abs(rupees.AUCPR-cents.AUCPR) > 1e-12 {
		t.Fatalf("baseline moved under a change of units: %.12f vs %.12f",
			rupees.AUCPR, cents.AUCPR)
	}
}

// TestAmountBaselineSeparatesWhenAmountIsSignal is the upper anchor.
//
// If fraud really is the large payments, the baseline must say so loudly. The
// old threshold form could report a near-chance number here purely because the
// constant sat above the whole distribution, which is how a baseline that is
// maximally WRONG becomes indistinguishable from one that is switched OFF.
func TestAmountBaselineSeparatesWhenAmountIsSignal(t *testing.T) {
	// Fraud is strictly larger than every legitimate payment.
	rows := amountRows(20000, 0.02, 1, func(fraud bool, rng *rand.Rand) int64 {
		if fraud {
			return 1_000_000 + rng.Int64N(1000)
		}
		return 100 + rng.Int64N(1000)
	})
	bl := bigAmountBaseline(rows, DefaultTaus())
	if bl.AUCPR < 0.95 {
		t.Fatalf("amount perfectly predicts fraud but baseline scored %.3f", bl.AUCPR)
	}
}

// TestAmountBaselineIsChanceWhenAmountIsNoise is the lower anchor, and it is
// the case this simulator actually lives in: fraud hides in ordinary-sized
// payments, so ranking by amount must land at the base rate rather than above
// it. Confirmed independently by real card data, where the median fraud is
// $75 against a median legitimate payment of $69.
func TestAmountBaselineIsChanceWhenAmountIsNoise(t *testing.T) {
	rows := amountRows(20000, 0.02, 1, func(fraud bool, rng *rand.Rand) int64 {
		return 100 + rng.Int64N(9000) // identical distribution for both classes
	})
	bl := bigAmountBaseline(rows, DefaultTaus())
	prev := rows.Prevalence()
	if math.Abs(bl.AUCPR-prev) > 0.02 {
		t.Fatalf("amount carries no signal but baseline scored %.4f against prevalence %.4f",
			bl.AUCPR, prev)
	}
}
