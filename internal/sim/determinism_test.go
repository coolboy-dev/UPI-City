package sim

import (
	"testing"

	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// TestDeterminism is the Day 1 exit gate.
//
// Every downstream claim depends on it. Replay, detector comparison on
// identical traffic, and calibration fitted on one seed set and evaluated on
// another are all meaningless if the same seed does not produce the same run.
// This test is cheap and it fails loudly, so a determinism regression is
// caught the moment it is introduced rather than on day 9.
func TestDeterminism(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Ticks = 5000

	a := New(cfg).Run(cfg.Ticks)
	b := New(cfg).Run(cfg.Ticks)

	if a != b {
		t.Fatalf("same seed produced different streams: %016x != %016x", uint64(a), uint64(b))
	}
	if a == obs.NewStreamHash() {
		t.Fatal("stream hash unchanged from the offset basis — the run emitted no events")
	}
}

// TestSeedsDiverge guards the opposite failure: a hash that is stable because
// the seed is not actually reaching the generators.
func TestSeedsDiverge(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Ticks = 2000

	cfg.Seed = 1
	a := New(cfg).Run(cfg.Ticks)
	cfg.Seed = 2
	b := New(cfg).Run(cfg.Ticks)

	if a == b {
		t.Fatalf("different seeds produced identical streams (%016x) — the seed is being ignored", uint64(a))
	}
}

// TestAgentStreamsAreIDDerived checks that adding agents does not perturb the
// behaviour of the agents that already existed.
//
// Without this, changing the population size reshuffles every random stream in
// the run, and comparing two configurations becomes impossible — you could
// never tell whether a metric moved because of the change under test or
// because the traffic itself was different.
func TestAgentStreamsAreIDDerived(t *testing.T) {
	s := Seeds{Master: 99}
	first := s.ForAgent(7).Uint64()

	// Draw for a different agent in between; agent 7's stream must not care.
	_ = s.ForAgent(4000).Uint64()
	again := s.ForAgent(7).Uint64()

	if first != again {
		t.Fatalf("agent 7's stream depends on draw order: %d != %d", first, again)
	}
}

// TestPhaseADoesNotMutate verifies the purity of the decide phase.
//
// Phase A is the only phase that will be sharded across cores at 5,000
// agents. It is safe to parallelise ONLY because it reads a frozen snapshot
// and mutates nothing; if a behaviour ever starts writing to world state, the
// parallel run stops matching the serial one and the scale-up quietly starts
// producing different numbers.
func TestPhaseADoesNotMutate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Ticks = 200
	w := New(cfg)

	// Advance so balances and baselines are non-trivial.
	for i := obs.Tick(0); i < 200; i++ {
		w.Step()
	}

	before := make([]int64, len(w.Agents))
	fraud := make([]FraudState, len(w.Agents))
	for i := range w.Agents {
		before[i] = w.Agents[i].BalanceP
		fraud[i] = w.Agents[i].Fraud
	}

	// Run the decide phase alone, exactly as Step does.
	w.snap.Tick = w.Now
	w.snap.Surge = w.Surge(w.Now)
	for i := 1; i < len(w.Agents); i++ {
		a := &w.Agents[i]
		w.intents[i] = a.Behavior.Decide(a.View(w.Now), &w.snap, a.rng, w.Now, w.intents[i][:0])
	}

	for i := range w.Agents {
		if w.Agents[i].BalanceP != before[i] {
			t.Fatalf("agent %d balance mutated during the decide phase: %d -> %d",
				i, before[i], w.Agents[i].BalanceP)
		}
		if w.Agents[i].Fraud != fraud[i] {
			t.Fatalf("agent %d fraud state mutated during the decide phase", i)
		}
	}
}

// TestHardNegativesExist asserts the population actually contains the
// legitimate-but-suspicious archetypes.
//
// If these are absent the false-positive rate is structurally zero, every
// precision number becomes a perfect 1.0, and the benchmark stops being
// evidence of anything at all.
func TestHardNegativesExist(t *testing.T) {
	w := New(DefaultConfig())

	counts := map[truth.Archetype]int{}
	for i := 1; i < len(w.Agents); i++ {
		counts[w.Agents[i].Archetype]++
	}

	for _, a := range []truth.Archetype{
		truth.ArchMegaMerchant, truth.ArchPayroll, truth.ArchSupplyPair,
	} {
		if counts[a] == 0 {
			t.Errorf("no %s agents in the population — the FP rate would be structurally zero", a)
		}
	}
	// Supply-chain agents only form a legitimate cycle in pairs.
	if counts[truth.ArchSupplyPair]%2 != 0 {
		t.Errorf("odd number of supply-pair agents (%d): one has no partner and forms no cycle",
			counts[truth.ArchSupplyPair])
	}
}

// TestSettlementIsNotInstant verifies that funds are held rather than moving
// atomically, which is what makes a bank outage able to strand value.
func TestSettlementIsNotInstant(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.SettleTicks == 0 {
		t.Skip("settlement configured as instant")
	}
	w := New(cfg)

	var firstEmit obs.Tick
	for i := obs.Tick(0); i < 500; i++ {
		ev := w.Step()
		if len(ev) > 0 && firstEmit == 0 {
			firstEmit = ev[0].SettleTick
			if ev[0].SettleTick <= ev[0].Tick {
				t.Fatalf("transaction settled in the same tick it was created (t=%d, s=%d)",
					ev[0].Tick, ev[0].SettleTick)
			}
			return
		}
	}
	t.Fatal("no events emitted in 500 ticks")
}
