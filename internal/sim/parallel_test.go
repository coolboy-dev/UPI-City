package sim

import (
	"testing"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/obs"
)

// TestParallelMatchesSerial is the whole justification for sharding the
// decide phase.
//
// The point of parallelism here is NOT speed. It is that scaling up must not
// cost anything else — and the thing it could easily cost is determinism,
// which every other claim in this project rests on. Replay, calibration fitted
// on held-out seeds, and comparing detector versions on identical traffic all
// assume a seed names exactly one run.
//
// So: same seed, same config, one worker versus many, and the event streams
// must agree bit for bit. If this ever fails, the parallel path must be
// deleted rather than debugged in production.
func TestParallelMatchesSerial(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NumAgents = 3000
	cfg.Ticks = 3000

	serial := New(cfg)
	serial.SetWorkers(1)
	want := serial.Run(cfg.Ticks)

	for _, workers := range []int{2, 4, 7, 16} {
		par := New(cfg)
		par.SetWorkers(workers)
		got := par.Run(cfg.Ticks)

		if got != want {
			t.Errorf("%d workers produced a different event stream: %016x != %016x",
				workers, uint64(got), uint64(want))
		}
	}
}

// TestParallelMatchesSerialUnderChaos repeats the check with a scenario
// running, since chaos mutates agent state mid-run and is exactly where a
// sharding bug would surface.
func TestParallelMatchesSerialUnderChaos(t *testing.T) {
	run := func(workers int) obs.StreamHash {
		cfg := DefaultConfig()
		cfg.NumAgents = 3000
		cfg.Ticks = 8000
		w := New(cfg)
		w.SetWorkers(workers)
		sc, ok := chaos.New("fraud-ring")
		if !ok {
			t.Fatal("fraud-ring scenario not registered")
		}
		w.SetScenario(sc, chaos.DefaultConfig())
		return w.Run(cfg.Ticks)
	}

	want := run(1)
	if got := run(12); got != want {
		t.Errorf("under chaos, 12 workers diverged: %016x != %016x", uint64(got), uint64(want))
	}
}

// TestShardingIsOffByDefault pins the conclusion of the scaling measurement.
//
// Sharding the decide phase was built, measured at 5,000 / 20,000 / 50,000
// agents, and is slower at every one of them — monotonically worse from two
// workers upward. The phase is memory-bound, so extra threads contend for
// bandwidth the serial loop already saturates. The code remains available via
// SetWorkers so the measurement can be repeated on other hardware, but no
// configuration this project ships turns it on.
func TestShardingIsOffByDefault(t *testing.T) {
	for _, n := range []int{300, 5000, 50000} {
		cfg := DefaultConfig()
		cfg.NumAgents = n
		if w := New(cfg); w.Workers() != 1 {
			t.Errorf("a %d-agent world used %d workers; sharding measured slower "+
				"at every scale and must stay off by default", n, w.Workers())
		}
	}
}

// TestDecidePhaseTouchesOnlyItsOwnSlot verifies the property that makes the
// sharding safe without locks: an agent's decision lands in its own slot and
// nowhere else.
func TestDecidePhaseTouchesOnlyItsOwnSlot(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NumAgents = 2000
	cfg.Ticks = 500
	w := New(cfg)
	w.SetWorkers(8)

	for i := obs.Tick(0); i < 500; i++ {
		w.Step()
	}

	// Every intent an agent produced must name that agent as the payer,
	// which is only true if no worker wrote outside its range.
	w.snap.Tick = w.Now
	w.snap.Surge = w.Surge(w.Now)
	w.decide(w.Now)

	for i := 1; i < len(w.Agents); i++ {
		for _, in := range w.intents[i] {
			if in.To == obs.AgentID(i) {
				t.Fatalf("agent %d proposed paying itself; slots have been crossed", i)
			}
		}
	}
}
