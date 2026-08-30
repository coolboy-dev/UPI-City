package risk

import (
	"testing"

	"github.com/yug/upi-city/internal/obs"
)

// hubGraph builds a star: one merchant transacting with n customers, a few of
// whom are suspicious. This is the shape that breaks naive propagation.
func hubGraph(p *Propagator, hub obs.AgentID, n int, suspicious int) []float64 {
	size := n + 2
	if int(hub) >= size {
		size = int(hub) + 1
	}
	scores := make([]float64, size)
	for i := 1; i <= n; i++ {
		cust := obs.AgentID(i)
		if cust == hub {
			continue
		}
		p.Observe(obs.Event{From: cust, To: hub, SettleTick: 10})
		if i <= suspicious {
			scores[cust] = 0.8
		}
	}
	return scores
}

// TestNaivePropagationCondemnsThePopular reproduces the failure deliberately.
//
// A merchant is connected to everyone, so under an unnormalised sum it
// inherits a share of risk from every fraudster that ever bought something
// from it. It becomes maximally suspicious purely by being popular. Measured
// over six seeds this drove false positives on large merchants from 22 to 930
// per thousand — 93% of their legitimate traffic.
//
// The test asserts the failure still happens, because the normalised version
// is only meaningful as a fix if the thing it fixes is real.
func TestNaivePropagationCondemnsThePopular(t *testing.T) {
	const hub obs.AgentID = 500
	p := NewNaivePropagator()
	scores := hubGraph(p, hub, 200, 10)

	out := p.Adjust(scores, 100)

	if out[hub] < 0.9 {
		t.Errorf("naive propagation scored the hub %.3f; expected it to be condemned "+
			"by association. If this now passes, the failure mode being demonstrated "+
			"no longer exists and the comparison is meaningless.", out[hub])
	}
	if scores[hub] != 0 {
		t.Fatal("the hub started with non-zero risk; the test proves nothing")
	}
}

// TestNormalisationProtectsTheHub is the fix.
//
// Averaging rather than summing means an account's propagated risk reflects
// the typical suspicion of its counterparties, not how many it has. Hub
// damping then exempts high-degree accounts entirely: a merchant is not
// complicit because some of its customers are criminals.
func TestNormalisationProtectsTheHub(t *testing.T) {
	const hub obs.AgentID = 500
	p := NewPropagator()
	scores := hubGraph(p, hub, 200, 10)

	out := p.Adjust(scores, 100)

	if out[hub] > 0.05 {
		t.Errorf("normalised propagation still scored the hub %.3f; degree normalisation "+
			"and hub damping are not protecting popular accounts", out[hub])
	}
}

// TestPropagationIsDeterministic guards the determinism gate.
//
// Neighbours live in a map, Go randomises map iteration, and floating-point
// addition is not associative — so an unsorted neighbour sum would differ in
// its last bits between runs and silently break every comparison built on
// identical traffic.
func TestPropagationIsDeterministic(t *testing.T) {
	build := func() ([]float64, *Propagator) {
		p := NewPropagator()
		scores := make([]float64, 60)
		for i := 1; i < 50; i++ {
			for j := i + 1; j < i+6 && j < 50; j++ {
				p.Observe(obs.Event{From: obs.AgentID(i), To: obs.AgentID(j), SettleTick: 5})
			}
			scores[i] = float64(i%7) / 10
		}
		return scores, p
	}

	s1, p1 := build()
	s2, p2 := build()
	a := p1.Adjust(s1, 50)
	b := p2.Adjust(s2, 50)

	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("agent %d: %.17g vs %.17g — propagation is not deterministic", i, a[i], b[i])
		}
	}
}

// TestPropagationRespectsTheWindow checks stale edges stop counting. Without
// this every account eventually connects to every other and propagated risk
// becomes a measure of how long the run has been going.
func TestPropagationRespectsTheWindow(t *testing.T) {
	p := NewPropagator()
	p.Observe(obs.Event{From: 1, To: 2, SettleTick: 10})

	scores := make([]float64, 5)
	scores[2] = 0.9

	fresh := p.Adjust(scores, 100)
	stale := p.Adjust(scores, 10_000)

	if fresh[1] <= scores[1] {
		t.Error("a recent edge did not propagate any risk")
	}
	if stale[1] != scores[1] {
		t.Errorf("an edge far outside the window still propagated: %.3f", stale[1])
	}
}

// TestPropagationNeverExceedsOne keeps scores in range, since everything
// downstream — thresholds, calibration, the decision layer — assumes [0,1].
func TestPropagationNeverExceedsOne(t *testing.T) {
	p := NewNaivePropagator()
	scores := hubGraph(p, 500, 200, 200)
	for i, v := range p.Adjust(scores, 100) {
		if v > 1 || v < 0 {
			t.Fatalf("agent %d scored %.3f, outside [0,1]", i, v)
		}
	}
}
