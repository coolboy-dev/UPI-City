package sim

import "math"

// assignLayout computes a frozen display position for every agent.
//
// Positions are computed ONCE, server-side, from the seed and the static
// topology. Agents do not move, so a continuously re-simulated force layout
// would burn a core to produce a graph that jitters differently on every
// reload — and every take of the demo video. Concentric rings clustered by
// bank give a stable, readable picture that is identical across runs and
// works unchanged in the aggregated 5,000-agent view.
func (w *World) assignLayout() {
	rng := w.seeds.For("layout")

	// Count per bank so each bank's ring is evenly populated.
	perBank := make([]int, len(w.Banks))
	for i := 1; i < len(w.Agents); i++ {
		perBank[w.Agents[i].Bank]++
	}
	placed := make([]int, len(w.Banks))

	nb := float64(len(w.Banks))
	for i := 1; i < len(w.Agents); i++ {
		a := &w.Agents[i]
		b := int(a.Bank)

		// Each bank owns an angular sector; agents fan out within it.
		sector := 2 * math.Pi / nb
		base := sector * float64(b)
		frac := float64(placed[b]) / math.Max(1, float64(perBank[b]))
		angle := base + sector*frac

		// Large merchants sit toward the centre, retail toward the rim, so
		// the hubs are visually obvious without needing a legend.
		radius := 0.45 + 0.5*frac
		switch a.Archetype {
		case 1: // mega merchant
			radius = 0.18 + 0.08*rng.Float64()
		case 2: // payroll
			radius = 0.30 + 0.06*rng.Float64()
		}
		radius += (rng.Float64() - 0.5) * 0.04
		angle += (rng.Float64() - 0.5) * sector * 0.15

		a.X = float32(math.Cos(angle) * radius)
		a.Y = float32(math.Sin(angle) * radius)
		placed[b]++
	}
}
