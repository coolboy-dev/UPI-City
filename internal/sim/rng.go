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
