package detect

import (
	"testing"

	"github.com/yug/upi-city/internal/obs"
)

// feed pushes n transactions from agent `from` over `span` ticks, starting at
// `start`, and returns the highest score any of them produced.
func feed(d Detector, from obs.AgentID, start obs.Tick, n int, span obs.Tick, amt int64) float64 {
	var best float64
	buf := make([]Finding, 0, 8)
	for i := 0; i < n; i++ {
		t := start + obs.Tick(i)*span/obs.Tick(max(1, n))
		buf = d.Observe(obs.Event{
			Tick: t, SettleTick: t, TxID: obs.TxID(i + 1),
			From: from, To: obs.AgentID(1000 + i%50),
			AmountP: amt, Status: obs.StatusSuccess,
		}, buf[:0])
		for _, f := range buf {
			if f.Score > best {
				best = f.Score
			}
		}
	}
	return best
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestBaselineSurvivesASustainedAttack is a regression guard on the most
// expensive bug this project has had.
//
// Per-agent baselines are what let a busy merchant go unflagged, but a
// baseline that keeps updating during an attack will absorb the attack. The
// original implementation folded the current rate in unconditionally, and the
// effect was measured rather than theorised: recall against the fraud ring was
// 72% while it was ramping and 7% once it reached full strength. The mules had
// not become subtler — they had retrained the detector. Across ten seeds the
// fix moved AUC-PR from 0.053 to 0.471.
//
// The property asserted here is that a burst which is anomalous when it starts
// is STILL anomalous after it has run for a long time.
func TestBaselineSurvivesASustainedAttack(t *testing.T) {
	v := NewVelocity()
	const agent obs.AgentID = 7

	// A long, quiet history: this agent's normal is roughly 4 payments per
	// 300-tick window.
	feed(v, agent, 0, 220, 16000, 5_000)

	// The attack begins: a sustained high rate.
	early := feed(v, agent, 16000, 400, 2000, 5_000)
	if early <= 0 {
		t.Fatalf("detector did not react to the start of a sustained burst (score %.3f)", early)
	}

	// It continues at the same rate for a long time. If the baseline is
	// absorbing it, the score decays toward nothing.
	late := feed(v, agent, 18000, 1200, 6000, 5_000)

	if late < early*0.5 {
		t.Errorf("baseline poisoned: a burst scored %.3f when it began and only %.3f "+
			"after running for a while. A sustained attack must not train the detector "+
			"to accept it.", early, late)
	}
}

// TestBaselineStillAdaptsToLegitimateGrowth is the other half, and the reason
// freezing must be conditional rather than permanent.
//
// A business that genuinely grows should be flagged briefly and then accepted.
// If the baseline froze forever at the first sign of change, every expanding
// merchant would become a permanent false positive — trading one failure mode
// for a worse one.
func TestBaselineStillAdaptsToLegitimateGrowth(t *testing.T) {
	v := NewVelocity()
	const agent obs.AgentID = 9

	feed(v, agent, 0, 200, 14000, 5_000)

	// Step up to a new, sustained level and stay there for a very long time,
	// with the new rate becoming this agent's ordinary life.
	feed(v, agent, 14000, 300, 3000, 5_000)
	settled := feed(v, agent, 60000, 300, 20000, 5_000)

	if settled > 0.9 {
		t.Errorf("a business that grew and then held steady is still scoring %.3f; "+
			"the baseline never re-adapted and this agent is a permanent false positive",
			settled)
	}
}
