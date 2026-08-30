package detect

import (
	"math"

	"github.com/yug/upi-city/internal/obs"
)

// Cycle detects money returning to where it started.
//
// ─── The central idea of this project ───────────────────────────────────────
//
// CYCLE EXISTENCE IS NOT A FRAUD SIGNAL. A supplier and a distributor settling
// invoices in both directions form a permanent 2-cycle in the transaction
// graph. So does a layering ring. A detector keyed on topology flags both, and
// the legitimate one is larger and more frequent.
//
// What actually separates them is dynamics:
//
//	                  layering ring        supply chain
//	traversal time    seconds to minutes   days
//	value retention   ~90% per hop          value returns; net ≈ 0
//	purpose           move value away       settle mutual obligations
//
// So the score is f(traversal speed, value retention) and topology only
// decides whether there is anything to score at all.
//
// ─── Why this is cheap enough to run in the tick loop ───────────────────────
//
// Enumerating cycles in a 5,000-node graph per event is not viable. Two
// bounds make it so, and both are in place from the start rather than being
// retrofitted when the agent count rises:
//
//  1. A PASS-THROUGH PRECONDITION. A search only starts when an agent
//     forwards, soon and largely intact, money it just received. That is the
//     definition of layering, it is O(1) to test, and it skips the
//     overwhelming majority of ordinary payments.
//  2. HARD CAPS on the search itself: at most 4 hops, at most 6 recent edges
//     per node, and only edges inside a recent window — so the worst case is
//     a few hundred steps on an event that already looks like layering.
type Cycle struct {
	lastIn []inbound
	edges  []*edgeRing

	// passWindow is how recently money must have arrived for an outgoing
	// payment to count as forwarding it.
	passWindow obs.Tick
	// passRetain is the fraction of the received amount that must be
	// forwarded.
	passRetain float64
	// cycleWindow bounds how far back the graph search may look.
	cycleWindow obs.Tick
	maxHops     int
	// minHops excludes 2-cycles.
	//
	// The first version of this detector flagged 228 of 231 ordinary
	// consumers, because "pay a merchant, receive a refund shortly after" is
	// a fast 2-cycle with high value retention — structurally identical to
	// laundering by every measure the detector had. Layering requires
	// INTERMEDIARIES; a direct there-and-back is a refund or a settlement.
	minHops int
	// amtSigma requires the agent's recent THROUGHPUT — total value moved in
	// a window — to be a large multiple of its own normal.
	//
	// Deliberately throughput rather than the size of a single payment. A
	// launderer's whole purpose in structuring is to make every individual
	// transfer look ordinary, so any per-payment size test is exactly the one
	// they have already defeated. The total value passing through an account
	// is much harder to disguise: splitting a sum into twenty payments does
	// not reduce the sum.
	amtSigma float64
	volWin   []*obs.Window
	volBase  []obs.Baseline
	volLast  []obs.Tick
	// tau sets how fast the speed score decays with traversal time. A cycle
	// closed in tau ticks scores about 0.37 on speed alone.
	tau float64
}

type inbound struct {
	tick obs.Tick
	amt  int64
}

const edgeFanout = 6

// edgeRing is a fixed-size ring of an agent's most recent outgoing payments.
// Fixed size keeps memory flat at 5,000 agents and bounds the search.
type edgeRing struct {
	to   [edgeFanout]obs.AgentID
	amt  [edgeFanout]int64
	tick [edgeFanout]obs.Tick
	n    int
	idx  int
}

func (r *edgeRing) add(to obs.AgentID, amt int64, t obs.Tick) {
	r.to[r.idx] = to
	r.amt[r.idx] = amt
	r.tick[r.idx] = t
	r.idx = (r.idx + 1) % edgeFanout
	if r.n < edgeFanout {
		r.n++
	}
}

// NewCycle returns the default cycle detector.
func NewCycle() *Cycle {
	return &Cycle{
		// Wide enough to admit slow legitimate cycles into scoring.
		//
		// A tight window would exclude supply-chain triangles before the score
		// was ever computed, which would make the speed discrimination look
		// like insight when it was really just a precondition nothing
		// legitimate ever reached. Letting them in and having the speed term
		// rank them near zero is the same outcome earned honestly — and it is
		// what puts a real legitimate cycle in the overlap region.
		passWindow:  300,
		passRetain:  0.45,
		cycleWindow: 600,
		maxHops:     4,
		minHops:     3,
		amtSigma:    4.0,
		tau:         150,
	}
}

func (c *Cycle) Name() string { return "cycle" }

func (c *Cycle) Reset() {
	c.lastIn, c.edges = nil, nil
	c.volWin, c.volBase, c.volLast = nil, nil, nil
}

func (c *Cycle) Observe(ev obs.Event, dst []Finding) []Finding {
	// Only settled payments move value, and only moved value can be layered.
	if ev.Status != obs.StatusSuccess {
		return dst
	}
	t := ev.SettleTick

	c.lastIn = grow(c.lastIn, ev.To)
	c.edges = grow(c.edges, ev.From)
	c.lastIn = grow(c.lastIn, ev.From)
	c.edges = grow(c.edges, ev.To)
	c.volWin = grow(c.volWin, ev.From)
	c.volBase = grow(c.volBase, ev.From)
	c.volLast = grow(c.volLast, ev.From)

	if c.edges[ev.From] == nil {
		c.edges[ev.From] = &edgeRing{}
		c.volWin[ev.From] = obs.NewWindow(300, 10)
		c.volBase[ev.From] = obs.NewBaseline(0.02)
	}

	// Test the pass-through precondition BEFORE recording this edge, so an
	// agent cannot satisfy it against its own current payment.
	in := c.lastIn[ev.From]
	isPassThrough := in.amt > 0 &&
		t >= in.tick &&
		t-in.tick <= c.passWindow &&
		float64(ev.AmountP) >= c.passRetain*float64(in.amt)

	// Is the agent's recent throughput large by its own standards?
	vw := c.volWin[ev.From]
	vw.Add(t, ev.AmountP)
	vol := float64(vw.Sum())
	vb := &c.volBase[ev.From]
	isLarge := vb.Ready(12) && vb.Mean > 0 && vol >= c.amtSigma*vb.Mean

	// Frozen while throughput is anomalous, for the same reason as the
	// velocity detector: a sustained laundering run would otherwise raise
	// this agent's own notion of normal until moving that much money stopped
	// being remarkable.
	if t-c.volLast[ev.From] >= 50 {
		if !isLarge {
			vb.Observe(vol)
		}
		c.volLast[ev.From] = t
	}

	c.edges[ev.From].add(ev.To, ev.AmountP, t)
	c.lastIn[ev.To] = inbound{tick: t, amt: ev.AmountP}

	if !isPassThrough || !isLarge {
		return dst
	}

	// Look for a path from the recipient back to the sender.
	lo := obs.Tick(0)
	if t > c.cycleWindow {
		lo = t - c.cycleWindow
	}
	best := c.search(ev.To, ev.From, lo, t, ev.AmountP, ev.AmountP, t, c.maxHops-1, 0)
	if best.found == 0 || best.hops < c.minHops {
		return dst
	}

	span := float64(t - best.minTick)
	speed := math.Exp(-span / c.tau)
	retention := float64(best.minAmt) / math.Max(1, float64(best.maxAmt))
	score := speed * retention
	if score < ScoreFloor {
		return dst
	}

	return append(dst, Finding{
		Tick: t, TxID: ev.TxID, Agent: ev.From,
		Detector: c.Name(), Score: score,
		Evidence: map[string]float64{
			"hops":            float64(best.hops),
			"traversal_ticks": span,
			"value_retention": retention,
			"speed":           speed,
		},
	})
}

type cycleResult struct {
	found   int
	hops    int
	minTick obs.Tick
	minAmt  int64
	maxAmt  int64
}

// search walks from cur toward target through recent edges, in STRICTLY
// INCREASING time order bounded above by hi.
//
// The time ordering is the point. The event under consideration is the one
// that closes the loop — the last hop, returning value to where it started —
// so the earlier hops must run forward in time toward it. Money cannot be
// forwarded before it arrives, and a set of edges that merely happens to form
// a cycle in the graph, without a consistent chronology, is not a flow of
// funds at all. Searching in the wrong direction finds those phantom cycles
// and misses every real one.
func (c *Cycle) search(cur, target obs.AgentID, lo, hi obs.Tick, minAmt, maxAmt int64, minTick obs.Tick, hops int, depth int) cycleResult {
	if hops <= 0 || int(cur) >= len(c.edges) || c.edges[cur] == nil {
		return cycleResult{}
	}
	r := c.edges[cur]
	if hi > c.cycleWindow && lo < hi-c.cycleWindow {
		lo = hi - c.cycleWindow
	}

	var best cycleResult
	for i := 0; i < r.n; i++ {
		et, ea, eto := r.tick[i], r.amt[i], r.to[i]
		if et < lo || et > hi {
			continue
		}
		nMin, nMax, nTick := minAmt, maxAmt, minTick
		if ea < nMin {
			nMin = ea
		}
		if ea > nMax {
			nMax = ea
		}
		if et < nTick {
			nTick = et
		}

		if eto == target {
			cand := cycleResult{found: 1, hops: depth + 2, minTick: nTick, minAmt: nMin, maxAmt: nMax}
			if best.found == 0 || cand.minTick > best.minTick {
				best = cand // prefer the tightest (fastest) cycle
			}
			continue
		}
		if sub := c.search(eto, target, et, hi, nMin, nMax, nTick, hops-1, depth+1); sub.found != 0 {
			if best.found == 0 || sub.minTick > best.minTick {
				best = sub
			}
		}
	}
	return best
}
