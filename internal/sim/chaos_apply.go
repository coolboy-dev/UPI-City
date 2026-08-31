package sim

import (
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// SetScenario arms a chaos scenario.
//
// It may be called mid-run, which is what live injection from the dashboard
// does. Any incident still open is closed first and the incident pointer is
// cleared, so the next effect opens a fresh record rather than silently
// attributing new activity to the previous episode — which would corrupt both
// the member list and the detection-latency baseline.
func (w *World) SetScenario(s chaos.Scenario, cfg chaos.Config) {
	w.Finish()
	w.incident, w.incidentClosed = 0, false

	w.scenario = s
	w.chaosCfg = cfg
	// Seeded by name AND by the tick of arming, so injecting the same
	// scenario twice in one session does not replay an identical ring.
	s.Arm(cfg, w, w.seeds.For(fmt.Sprintf("chaos:%s:%d", s.Name(), w.Now)))
}

// Scenario returns the armed scenario, if any.
func (w *World) Scenario() chaos.Scenario { return w.scenario }

// ---------------------------------------------------------------------------
// chaos.WorldView
// ---------------------------------------------------------------------------

func (w *World) Tick() obs.Tick { return w.Now }
func (w *World) NumAgents() int { return len(w.Agents) - 1 }
func (w *World) NumBanks() int  { return len(w.Banks) }

// Recruitable returns dormant accounts a scenario can activate.
//
// These are ordinary consumers in every observable respect until this moment:
// same spend rate, same amounts, same salary, same solvency. Nothing about
// them is visible in the event stream before recruitment.
func (w *World) Recruitable(rng *rand.Rand, n int) []obs.AgentID {
	var pool []obs.AgentID
	for i := 1; i < len(w.Agents); i++ {
		a := &w.Agents[i]
		if a.Fraud != FraudNone {
			continue
		}
		if a.Archetype == truth.ArchMule || a.Archetype == truth.ArchBot {
			pool = append(pool, a.ID)
		}
	}
	return sampleAgents(pool, rng, n)
}

// Wealthy returns ordinary accounts with a balance worth stealing.
func (w *World) Wealthy(rng *rand.Rand, n int, exclude []obs.AgentID) []obs.AgentID {
	skip := make(map[obs.AgentID]bool, len(exclude))
	for _, e := range exclude {
		skip[e] = true
	}
	var pool []obs.AgentID
	for _, id := range w.civilians {
		a := &w.Agents[id]
		if skip[id] || a.Fraud != FraudNone || a.BalanceP < 500_000 {
			continue
		}
		pool = append(pool, id)
	}
	return sampleAgents(pool, rng, n)
}

// sampleAgents draws n distinct ids without replacement, returning them
// sorted so membership never depends on draw order.
func sampleAgents(pool []obs.AgentID, rng *rand.Rand, n int) []obs.AgentID {
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	if n > len(pool) {
		n = len(pool)
	}
	cp := make([]obs.AgentID, len(pool))
	copy(cp, pool)
	// Partial Fisher-Yates: deterministic given the stream.
	for i := 0; i < n; i++ {
		j := i + rng.IntN(len(cp)-i)
		cp[i], cp[j] = cp[j], cp[i]
	}
	out := cp[:n:n]
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

// ---------------------------------------------------------------------------
// Effect application — the ONLY mutation path for chaos
// ---------------------------------------------------------------------------

// stepChaos advances the armed scenario and applies whatever it asks for.
//
// The incident record is opened here, by the engine, stamped with the tick at
// which the first effect landed. A scenario author cannot open one, cannot
// forget to, and cannot influence the timestamp — which means the detection
// latency baseline cannot be fudged, accidentally or otherwise.
func (w *World) stepChaos(t obs.Tick) {
	if w.scenario == nil {
		return
	}
	fx := w.scenario.Step(t, w)
	if len(fx) > 0 && w.incident == 0 {
		w.incident = w.Truth.OpenIncident(w.scenario.Name(), t, w.scenario.Members())
	}
	for _, e := range fx {
		w.applyEffect(e, t)
	}
	if w.incident != 0 && !w.incidentClosed && w.scenario.Done(t) {
		w.Truth.CloseIncident(w.incident, t)
		w.incidentClosed = true
	}
}

// Finish closes any incident still open at the end of a run.
//
// A run can stop mid-scenario — a short run, or a duration longer than the
// tick budget — leaving EndTick at its sentinel. That sentinel is a real
// number as far as arithmetic is concerned, so an unclosed incident would
// silently poison every latency and duration statistic computed from it.
// Callers must invoke this once the last tick has been stepped.
func (w *World) Finish() {
	if w.incident != 0 && !w.incidentClosed {
		w.Truth.CloseIncident(w.incident, w.Now)
		w.incidentClosed = true
	}
}

func (w *World) applyEffect(e chaos.Effect, t obs.Tick) {
	switch v := e.(type) {
	case chaos.Recruit:
		st := stateOf(v.Role)
		for _, id := range v.Agents {
			if int(id) >= len(w.Agents) {
				continue
			}
			a := &w.Agents[id]
			a.Fraud = st
			a.RingNext = v.Next
			// Attribution is automatic: every transaction this agent
			// originates from now on is labelled and tied to the incident.
			a.Incident = w.incident
		}

	case chaos.Release:
		for _, id := range v.Agents {
			if int(id) >= len(w.Agents) {
				continue
			}
			a := &w.Agents[id]
			a.Fraud = FraudNone
			a.RingNext = 0
			a.Incident = 0
		}

	case chaos.DegradeBank:
		if int(v.Bank) < len(w.Banks) {
			b := &w.Banks[v.Bank]
			b.OutageFailRate = v.FailRate
			b.OutageExtraMs = v.ExtraMs
			b.OutageUntil = v.Until
		}

	case chaos.RestoreBank:
		if int(v.Bank) < len(w.Banks) {
			b := &w.Banks[v.Bank]
			b.OutageFailRate = 0
			b.OutageExtraMs = 0
			b.OutageUntil = 0
		}
	}
}

func stateOf(r chaos.Role) FraudState {
	switch r {
	case chaos.RoleTakeover:
		return FraudTakeover
	case chaos.RoleMule:
		return FraudMule
	case chaos.RoleCashout:
		return FraudCashout
	case chaos.RoleBot:
		return FraudBot
	case chaos.RoleScamVictim:
		return FraudScamVictim
	}
	return FraudNone
}
