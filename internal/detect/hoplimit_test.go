package detect

import (
	"testing"

	"github.com/yug/upi-city/internal/obs"
)

// ring pushes value around a closed chain of n agents, quickly, and returns
// the highest cycle score any hop produced.
func ring(n int, amount int64) float64 {
	c := NewCycle()
	var best float64
	buf := make([]Finding, 0, 4)

	// Build each agent an ordinary history. The throughput baseline only
	// samples every 50 ticks and needs a dozen samples before it will judge
	// anything, so the warmup has to SPAN time rather than merely contain
	// events.
	tick := obs.Tick(0)
	for r := 0; r < 30; r++ {
		tick += 60
		for i := 1; i <= n; i++ {
			buf = c.Observe(obs.Event{
				Tick: tick, SettleTick: tick, TxID: obs.TxID(tick*100 + obs.Tick(i)),
				From: obs.AgentID(i), To: obs.AgentID(500 + i),
				AmountP: 5_000, Status: obs.StatusSuccess,
			}, buf[:0])
		}
	}

	// Now launder: 1 -> 2 -> ... -> n -> 1, fast and large.
	for lap := 0; lap < 6; lap++ {
		for i := 1; i <= n; i++ {
			to := obs.AgentID(i + 1)
			if i == n {
				to = 1
			}
			tick += 3
			buf = c.Observe(obs.Event{
				Tick: tick, SettleTick: tick, TxID: obs.TxID(10_000 + tick),
				From: obs.AgentID(i), To: to,
				AmountP: amount, Status: obs.StatusSuccess,
			}, buf[:0])
			for _, f := range buf {
				if f.Score > best {
					best = f.Score
				}
			}
		}
	}
	return best
}

// TestCycleDetectorHasAHopCeiling documents a real vulnerability, found by the
// adversarial search rather than by reading the code.
//
// The cycle search closes paths up to maxHops. A laundering cell one hop
// longer than that produces a cycle the detector cannot see AT ALL — not
// scored low, but structurally invisible. Searching the attacker's parameter
// space turned this up on its own: the three best-performing attacks it found
// all used five hops against a four-hop search.
//
// Raising the ceiling is not free. The search is bounded because cost grows
// with fan-out to the power of depth, and an unbounded walk over a 5,000-node
// graph is not viable in a tick loop. The honest position is that this is a
// known blind spot with a stated cost of closing it, not an oversight.
func TestCycleDetectorHasAHopCeiling(t *testing.T) {
	const amount = 400_000

	short := ring(3, amount)
	if short <= 0 {
		t.Fatalf("a 3-hop laundering ring scored %.3f; the detector should see this", short)
	}

	long := ring(6, amount)
	if long > 0 {
		t.Logf("a 6-hop ring scored %.3f — the ceiling may have been raised", long)
	}

	if long >= short {
		t.Errorf("expected a ring longer than the hop ceiling to be harder to see: "+
			"3-hop scored %.3f, 6-hop scored %.3f", short, long)
	}
	t.Logf("3-hop ring: %.3f   6-hop ring: %.3f (ceiling is %d hops)",
		short, long, NewCycle().maxHops)
}
