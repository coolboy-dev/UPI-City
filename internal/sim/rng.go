package sim

import (
	"math"
	"math/rand/v2"
)

// Seeds derives independent, reproducible random streams from one master seed.
//
// Three rules hold the determinism gate up, and all three are easy to break
// by accident:
//
//  1. NEVER use the package-level math/rand functions. They share global
//     state, so any change in call order anywhere perturbs every stream.
//  2. Every subsystem gets its own stream, named. Adding a new consumer must
//     not shift the numbers an existing one sees.
//  3. Per-agent streams are derived from the agent's ID, so inserting an
//     agent does not perturb any other agent's sequence. Without this,
//     changing the population size reshuffles the entire run and makes
//     cross-configuration comparison meaningless.
type Seeds struct{ Master uint64 }

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
	// golden ratio, for stream decorrelation
	phi64 = 0x9E3779B97F4A7C15
)

func fnv1a(s string) uint64 {
	h := uint64(fnvOffset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}

// For returns the named random stream.
func (s Seeds) For(name string) *rand.Rand {
	h := fnv1a(name) ^ s.Master
	return rand.New(rand.NewPCG(h, h*phi64|1))
}

// ForAgent returns an agent's private stream. Derived from the ID, not from
// draw order, so populations can grow without disturbing existing agents.
func (s Seeds) ForAgent(id uint32) *rand.Rand {
	h := (fnv1a("agent") ^ s.Master) + uint64(id)*phi64
	return rand.New(rand.NewPCG(h, h*phi64|1))
}

// lognormalPaise draws a positive amount with a long right tail, which is how
// real payment amounts are distributed. medianRupees sets the median; sigma
// sets the spread.
func lognormalPaise(rng *rand.Rand, medianRupees float64, sigma float64) int64 {
	v := medianRupees * expApprox(rng.NormFloat64()*sigma)
	p := int64(v * 100)
	if p < 100 {
		p = 100 // one rupee floor
	}
	return p
}

// expApprox clamps before exponentiating, so the lognormal tail cannot
// produce a single outlier large enough to dominate every volume statistic in
// the run.
func expApprox(x float64) float64 {
	if x > 4 {
		x = 4
	}
	if x < -4 {
		x = -4
	}
	return math.Exp(x)
}

// ---------------------------------------------------------------------------
// Amount distribution parameters, fitted to published UPI figures
// ---------------------------------------------------------------------------
//
// These were magic numbers until the simulator was checked against reality
// (cmd/validate). They are now fitted, and the fit is shown rather than
// asserted.
//
// The two published figures measure DIFFERENT populations, which is the whole
// subtlety of the fit:
//
//   - "86% of payments are under ₹500"  — SBI Research, and P2M only.
//   - "average ticket size ₹1,348"      — NPCI, and ALL UPI including P2P
//                                         and large business transfers.
//
// So the consumer→merchant draw is fitted to the first, and the second is
// allowed to fall out of the archetype mixture rather than being fitted
// directly. Fitting one distribution to both targets at once would have
// required a median near ₹23, which is not a payment anyone makes.
//
// For a lognormal with median m and shape σ,  P(X < 500) = Φ(ln(500/m)/σ).
// Holding σ at 1.1 and solving for the published 86%:
//
//	ln(500/m)/1.1 = Φ⁻¹(0.86) = 1.0803   →   m = 500·e^(-1.188) ≈ ₹152
//
// which rounds to ₹150 — and independently agrees with reported real P2M
// median ticket sizes, so the fit is not merely a curve dragged onto a target.
const (
	// consumerMedianRupees is the fitted P2M median. Was ₹300, which produced
	// 67.9% under ₹500 against a published 86%.
	consumerMedianRupees = 150
	consumerSigma        = 1.1

	// merchantMedianRupees covers business-to-business settlement between a
	// merchant and its suppliers. NOT fitted to the P2M figure: these are not
	// P2M payments and including them in that comparison would be wrong.
	merchantMedianRupees = 700
	merchantSigma        = 1.2
)
