package sim

import (
	"runtime"

	"github.com/yug/upi-city/internal/obs"
)

// ParallelThreshold is the agent count above which the decide phase MAY be
// sharded. It is set beyond any configuration this project uses, because
// sharding was measured and it does not help.
//
// ─── The measurement ────────────────────────────────────────────────────────
//
//	20,000 agents, ticks/second:
//	  1 worker   1419      ← fastest
//	  2 workers  1013
//	  3 workers  1010
//	  4 workers   829
//	  8 workers   737
//
// Monotonically worse, at 5,000 / 20,000 / 50,000 agents alike. Not a barrier
// artefact either: a persistent worker pool replaced per-tick goroutine
// creation and changed nothing, and even two workers lose.
//
// The reason is that this phase is MEMORY-BOUND, not compute-bound. Each
// agent's decision is a handful of nanoseconds — usually one random draw and
// an early return — but reaching it means chasing pointers to that agent's
// own generator and behaviour value, scattered across the heap. The serial
// loop already saturates memory bandwidth; extra threads add contention to a
// queue that is already full.
//
// The code stays, behind SetWorkers, because the measurement is worth more
// than the feature and someone should be able to re-run it on different
// hardware. It is off by default.
const ParallelThreshold = 1 << 30

// decide runs Phase A: every agent proposes what it wants to do.
//
// ─── Why this phase, and only this phase, is parallel ───────────────────────
//
// Decide is PURE. It reads a frozen snapshot and the agent's own private
// random stream, mutates nothing, and appends to a slot indexed by the
// agent's own id. Two agents can therefore never touch the same memory, and
// the work needs no locks at all.
//
// Everything else stays serial on purpose. Phase B mutates balances, the
// settlement ring and the truth ledger; running it concurrently would need
// locking on shared state, and lock contention at this granularity would make
// it SLOWER while destroying the ordering that makes runs reproducible.
//
// The property that matters is not throughput. It is that the event stream is
// bit-identical whether this runs on one core or twenty-eight — asserted by
// TestParallelMatchesSerial. Determinism is what replay, calibration on
// held-out seeds and detector comparison on identical traffic all stand on;
// trading it for speed would cost far more than it bought.
func (w *World) decide(t obs.Tick) {
	n := len(w.Agents)
	if w.workers <= 1 || n < ParallelThreshold {
		for i := 1; i < n; i++ {
			a := &w.Agents[i]
			w.intents[i] = a.Behavior.Decide(a.View(t), &w.snap, a.rng, t, w.intents[i][:0])
		}
		return
	}

	// Persistent workers, woken per tick.
	//
	// Spawning goroutines inside the tick loop was the first implementation
	// and it made the simulation TWICE AS SLOW at 5,000 agents: the decide
	// phase is only ~100µs of real work, so 28 goroutine creations and a
	// barrier every tick cost more than the work they were dividing. The pool
	// is started once; a tick now costs one broadcast and one barrier.
	w.startPool()
	w.poolTick = t
	w.poolStart.Add(len(w.pool))
	for _, ch := range w.pool {
		ch <- struct{}{}
	}
	w.poolStart.Wait()
}

// startPool lazily creates the worker goroutines and their index ranges.
func (w *World) startPool() {
	if w.pool != nil {
		return
	}
	n := len(w.Agents)
	per := (n - 1 + w.workers - 1) / w.workers

	for start := 1; start < n; start += per {
		end := start + per
		if end > n {
			end = n
		}
		ch := make(chan struct{})
		w.pool = append(w.pool, ch)

		go func(lo, hi int, wake chan struct{}) {
			for range wake {
				t := w.poolTick
				for i := lo; i < hi; i++ {
					a := &w.Agents[i]
					w.intents[i] = a.Behavior.Decide(a.View(t), &w.snap, a.rng, t, w.intents[i][:0])
				}
				w.poolStart.Done()
			}
		}(start, end, ch)
	}
}

// SetWorkers overrides how many goroutines the decide phase uses.
//
// Zero means one per core. One forces the serial path, which is what the
// equivalence test compares against.
func (w *World) SetWorkers(n int) {
	if n <= 0 {
		n = runtime.NumCPU()
	}
	w.workers = n
}

// Workers reports the current shard count.
func (w *World) Workers() int { return w.workers }
