package risk

import (
	"sort"

	"github.com/yug/upi-city/internal/obs"
)

// Propagator spreads suspicion along the transaction graph.
//
// The intuition is sound and old: an account that deals almost exclusively
// with accounts you already suspect is itself worth a second look. Laundering
// rings are, by construction, densely connected to themselves.
//
// ─── The trap this walks into ───────────────────────────────────────────────
//
// Guilt by association is catastrophic in a payment network unless it is
// normalised, and the reason is structural rather than statistical. A large
// merchant transacts with hundreds of distinct people. Under naive
// propagation it absorbs a share of risk from EVERY fraudster who ever bought
// something from it, becomes suspicious purely by being popular, and then
// radiates that manufactured suspicion back onto every ordinary customer it
// has ever served.
//
// So propagation is implemented both ways here — naive and normalised — and
// the difference is measured rather than asserted. See `just propagation`.
type Propagator struct {
	// Alpha damps how much of a neighbour's risk transfers per round.
	Alpha float64
	// Rounds is how far suspicion travels. Two hops is plenty: beyond that
	// almost every account in a connected network is reachable, and the
	// signal becomes a measure of connectivity rather than of risk.
	Rounds int
	// Window bounds which edges count as recent.
	Window obs.Tick

	// Normalise divides a neighbour's contribution by degree, so an account's
	// propagated risk is the AVERAGE suspicion of its counterparties rather
	// than the SUM. Without this, degree alone drives the score.
	Normalise bool
	// HubDegree is the point past which an account is treated as
	// infrastructure rather than as a peer. Hubs stop accumulating risk from
	// their counterparties: a merchant is not complicit because some of its
	// customers are criminals.
	HubDegree int

	// adjacency, bounded per agent.
	neighbours []map[obs.AgentID]obs.Tick
	maxPeers   int
}

// NewPropagator returns the normalised, hub-damped configuration.
func NewPropagator() *Propagator {
	return &Propagator{
		Alpha: 0.15, Rounds: 2, Window: 600,
		Normalise: true, HubDegree: 40, maxPeers: 256,
	}
}

// NewNaivePropagator returns the unnormalised version — the one that fails.
// Kept so the failure can be measured instead of described.
func NewNaivePropagator() *Propagator {
	return &Propagator{
		Alpha: 0.15, Rounds: 2, Window: 600,
		Normalise: false, HubDegree: 0, maxPeers: 256,
	}
}

// Observe records an edge.
func (p *Propagator) Observe(ev obs.Event) {
	p.grow(ev.From)
	p.grow(ev.To)
	// Undirected: for association, who paid whom matters less than that the
	// two accounts are connected at all.
	p.link(ev.From, ev.To, ev.SettleTick)
	p.link(ev.To, ev.From, ev.SettleTick)
}

func (p *Propagator) grow(id obs.AgentID) {
	for int(id) >= len(p.neighbours) {
		p.neighbours = append(p.neighbours, nil)
	}
	if p.neighbours[id] == nil {
		p.neighbours[id] = make(map[obs.AgentID]obs.Tick, 8)
	}
}

func (p *Propagator) link(a, b obs.AgentID, t obs.Tick) {
	m := p.neighbours[a]
	if len(m) >= p.maxPeers {
		if _, known := m[b]; !known {
			// Bounded memory: a hub would otherwise accumulate an unbounded
			// adjacency map at 5,000 agents.
			return
		}
	}
	m[b] = t
}

// Degree returns how many recent counterparties an account has.
func (p *Propagator) Degree(id obs.AgentID, now obs.Tick) int {
	if int(id) >= len(p.neighbours) || p.neighbours[id] == nil {
		return 0
	}
	var n int
	for _, t := range p.neighbours[id] {
		if now-t <= p.Window {
			n++
		}
	}
	return n
}

// Adjust returns propagated risk scores.
//
// scores is indexed by agent id and is not modified. The result is a new
// slice, so an ablation can compare the two directly.
func (p *Propagator) Adjust(scores []float64, now obs.Tick) []float64 {
	cur := make([]float64, len(scores))
	copy(cur, scores)

	for r := 0; r < p.Rounds; r++ {
		next := make([]float64, len(cur))
		copy(next, cur)

		for id := 1; id < len(cur) && id < len(p.neighbours); id++ {
			m := p.neighbours[id]
			if len(m) == 0 {
				continue
			}

			// Iterate neighbours in sorted order: floating-point addition is
			// not associative, and Go randomises map iteration, so an
			// unsorted sum would differ in its last bits between runs and
			// break the determinism gate.
			peers := make([]obs.AgentID, 0, len(m))
			for peer, t := range m {
				if now-t <= p.Window {
					peers = append(peers, peer)
				}
			}
			if len(peers) == 0 {
				continue
			}
			sort.Slice(peers, func(a, b int) bool { return peers[a] < peers[b] })

			// A hub is infrastructure, not a peer. It does not accumulate
			// suspicion from the people who happen to transact with it.
			if p.HubDegree > 0 && len(peers) > p.HubDegree {
				continue
			}

			var sum float64
			for _, peer := range peers {
				if int(peer) < len(cur) {
					sum += cur[peer]
				}
			}
			if p.Normalise {
				sum /= float64(len(peers))
			}

			v := cur[id] + p.Alpha*sum
			if v > 1 {
				v = 1
			}
			next[id] = v
		}
		cur = next
	}
	return cur
}

// Reset clears the graph.
func (p *Propagator) Reset() { p.neighbours = nil }
