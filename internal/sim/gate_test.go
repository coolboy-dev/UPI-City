package sim_test

import (
	"sort"
	"testing"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/detect"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/sim"
	"github.com/yug/upi-city/internal/truth"
)

// run executes a scenario and returns each agent's peak detector score.
func run(t *testing.T, scenario string, ticks obs.Tick) (*sim.World, []float64) {
	t.Helper()
	cfg := sim.DefaultConfig()
	cfg.Ticks = ticks
	w := sim.New(cfg)

	if scenario != "" {
		s, ok := chaos.New(scenario)
		if !ok {
			t.Fatalf("unknown scenario %q", scenario)
		}
		w.SetScenario(s, chaos.DefaultConfig())
	}

	pipe := detect.NewDefault()
	peak := make([]float64, cfg.NumAgents+1)
	buf := make([]detect.Finding, 0, 16)

	for i := obs.Tick(0); i < ticks; i++ {
		for _, e := range w.Step() {
			buf = pipe.Observe(e, buf[:0])
			for _, f := range buf {
				if int(f.Agent) < len(peak) && f.Score > peak[f.Agent] {
					peak[f.Agent] = f.Score
				}
			}
		}
	}
	w.Finish()
	return w, peak
}

// TestIncidentAlwaysClosed guards against an incident carrying its sentinel
// end tick into the metrics layer.
//
// A run can stop mid-scenario, and the sentinel is ^0 — a perfectly valid
// uint64 as far as arithmetic is concerned. An unclosed incident would not
// crash anything; it would quietly produce astronomical durations and poison
// every latency statistic derived from it.
func TestIncidentAlwaysClosed(t *testing.T) {
	// Deliberately shorter than the scenario's duration.
	w, _ := run(t, "fraud-ring", 5000)

	inc := w.Truth.Incidents()
	if len(inc) == 0 {
		t.Fatal("no incident recorded")
	}
	for _, in := range inc {
		if in.EndTick == ^obs.Tick(0) {
			t.Errorf("incident %d left unclosed: EndTick is the sentinel", in.ID)
		}
		if in.EndTick < in.StartTick {
			t.Errorf("incident %d ends (%d) before it starts (%d)", in.ID, in.EndTick, in.StartTick)
		}
	}
}

// TestDormantBeforeInjection asserts that recruitable accounts produce no
// fraudulent traffic before the scenario starts.
//
// If they did, the population itself would leak ground truth: a detector
// could learn to recognise "the kind of account that will later be a mule"
// from behaviour that predates any crime.
func TestDormantBeforeInjection(t *testing.T) {
	cfg := sim.DefaultConfig()
	ccfg := chaos.DefaultConfig()
	cfg.Ticks = ccfg.StartTick
	w := sim.New(cfg)

	s, _ := chaos.New("fraud-ring")
	w.SetScenario(s, ccfg)

	for i := obs.Tick(0); i < cfg.Ticks; i++ {
		w.Step()
	}

	for id := obs.TxID(1); id <= obs.TxID(w.Truth.NumTx()); id++ {
		if w.Truth.Label(id).Fraudulent() {
			t.Fatalf("transaction %d labelled %s before the scenario started at tick %d",
				id, w.Truth.Label(id), ccfg.StartTick)
		}
	}
}

// TestRampIsGradual asserts the attack grows rather than switching on.
//
// This is what makes detection latency a real measurement. With instant-on
// fraud every detector trips in the same tick and the reported latency is an
// artefact of the polling interval, not a property of the detector.
func TestRampIsGradual(t *testing.T) {
	cfg := chaos.DefaultConfig()
	w := sim.New(sim.DefaultConfig())
	s, _ := chaos.New("fraud-ring")
	w.SetScenario(s, cfg) // arms against a real population

	at := func(frac float64) float64 {
		return s.Intensity(cfg.StartTick + obs.Tick(float64(cfg.RampTicks)*frac))
	}

	if got := s.Intensity(cfg.StartTick - 1); got != 0 {
		t.Errorf("intensity before start = %v, want 0", got)
	}
	quarter, half, full := at(0.25), at(0.5), at(1.0)
	if !(quarter < half && half < full) {
		t.Errorf("intensity is not monotonic across the ramp: %.2f, %.2f, %.2f", quarter, half, full)
	}
	if full < 0.99 {
		t.Errorf("intensity never reaches full: %v", full)
	}
	if got := s.Intensity(cfg.StartTick + cfg.Duration); got != 0 {
		t.Errorf("intensity after the scenario ends = %v, want 0", got)
	}
}

// TestHardNegativesAreNotTrivial is the Day 2 gate, as a test.
//
// It asserts the thing that is easy to get wrong in the flattering direction:
// that legitimate-but-suspicious traffic scores ABOVE zero. A run in which
// every merchant, payroll and supply chain scores exactly zero has not shown
// the detectors work — it has shown the traffic is too easy to be evidence of
// anything, and every precision figure computed from it would be unearned.
func TestHardNegativesAreNotTrivial(t *testing.T) {
	w, peak := run(t, "fraud-ring", 40000)

	scored := map[truth.Archetype]int{}
	total := map[truth.Archetype]int{}
	for id := 1; id < len(peak); id++ {
		a := w.Truth.Archetype(obs.AgentID(id))
		if !a.HardNegative() {
			continue
		}
		total[a]++
		if peak[id] > 0 {
			scored[a]++
		}
	}

	for _, a := range []truth.Archetype{
		truth.ArchMegaMerchant, truth.ArchPayroll, truth.ArchSupplyPair,
	} {
		if total[a] == 0 {
			t.Fatalf("no %s agents in the population", a)
		}
		if scored[a] == 0 {
			t.Errorf("no %s agent ever scored above zero (%d present).\n"+
				"Hard negatives that are trivially separable make the false-positive "+
				"rate structurally zero and the benchmark meaningless.", a, total[a])
		}
	}
}

// TestFraudSeparatesFromNormal asserts the detectors are actually useful.
//
// The first version of this pipeline scored ordinary consumers HIGHER than
// the fraud ring while still catching the ring — which looks like success if
// you only check that fraud scores highly. Both directions are asserted here.
func TestFraudSeparatesFromNormal(t *testing.T) {
	w, peak := run(t, "fraud-ring", 40000)

	member := map[obs.AgentID]bool{}
	for _, in := range w.Truth.Incidents() {
		for _, m := range in.Members {
			member[m] = true
		}
	}

	var ring, normal []float64
	for id := 1; id < len(peak); id++ {
		aid := obs.AgentID(id)
		switch {
		case member[aid]:
			ring = append(ring, peak[id])
		case w.Truth.Archetype(aid) == truth.ArchConsumer:
			normal = append(normal, peak[id])
		}
	}
	if len(ring) == 0 || len(normal) == 0 {
		t.Fatal("population lacks ring members or ordinary consumers")
	}
	sort.Float64s(ring)
	sort.Float64s(normal)

	ringP90 := ring[int(0.90*float64(len(ring)-1))]
	normP90 := normal[int(0.90*float64(len(normal)-1))]

	if ringP90 <= normP90 {
		t.Errorf("ring p90 (%.3f) does not exceed ordinary-consumer p90 (%.3f): "+
			"the detectors are not separating fraud from background traffic", ringP90, normP90)
	}
}
