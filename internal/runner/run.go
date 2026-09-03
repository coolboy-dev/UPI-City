// Package runner executes one complete simulation-plus-detection run.
//
// It exists so that the single-run command and the multi-seed benchmark drive
// exactly the same code path. Two copies of this loop would eventually differ
// in some small way — a warmup, a threshold, an ordering — and the headline
// numbers would stop describing the run being demonstrated.
package runner

import (
	"fmt"
	"time"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/detect"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/risk"
	"github.com/yug/upi-city/internal/sim"
	"github.com/yug/upi-city/internal/truth"
)

// Options configures a run.
type Options struct {
	Sim       sim.Config
	Chaos     chaos.Config
	Scenario  string
	Detectors []string
	// Fusion combines the detectors' scores. Zero value means the default
	// weighted combination; pass risk.MaxFusion() for loudest-wins.
	Fusion *risk.Fusion
	// Calibrator, when set, maps fused scores to probabilities. It must have
	// been fitted on DIFFERENT seeds than this run.
	Calibrator *risk.Calibrator
	// Propagator, when set, spreads suspicion along the transaction graph
	// after fusion. Nil disables it entirely, which is the control arm of the
	// ablation rather than a disabled feature.
	Propagator *risk.Propagator
	// PropagateEvery is how often the propagation pass runs, in ticks.
	PropagateEvery obs.Tick

	// Workers shards the decide phase. 0 means one per core, 1 forces serial.
	Workers int

	// RecordDir, when set, receives events.jsonl and findings.jsonl.
	RecordDir string
}

// Result is everything a run produced.
type Result struct {
	Rows      metrics.Rows
	Incidents []truth.Incident
	Stats     sim.Stats
	Hash      obs.StreamHash
	Elapsed   time.Duration

	Events        int
	Findings      int
	Peak          []float64 // highest score per agent, for the separation table
	Population    string
	DetectorNames []string

	// World is retained after the run for post-hoc inspection — the
	// flow-balance report in particular. Read-only by convention: stepping it
	// further would invalidate every row already scored.
	World *sim.World
}

// Run executes the simulation, scores it, and joins the scores against ground
// truth into evaluable rows.
func Run(o Options) (Result, error) {
	w := sim.New(o.Sim)
	if o.Workers != 0 {
		w.SetWorkers(o.Workers)
	}

	if o.Scenario != "" {
		s, ok := chaos.New(o.Scenario)
		if !ok {
			return Result{}, fmt.Errorf("unknown scenario %q", o.Scenario)
		}
		w.SetScenario(s, o.Chaos)
	}

	pipe := detect.NewNamed(o.Detectors)

	fuse := risk.DefaultFusion()
	if o.Fusion != nil {
		fuse = *o.Fusion
	}

	var evRec, fndRec, lblRec *record.Writer
	if o.RecordDir != "" {
		var err error
		if evRec, err = record.Create(o.RecordDir, "events.jsonl"); err != nil {
			return Result{}, err
		}
		defer evRec.Close()
		if fndRec, err = record.Create(o.RecordDir, "findings.jsonl"); err != nil {
			return Result{}, err
		}
		defer fndRec.Close()
		// Ground truth goes in its own file, never mixed into the events a
		// detector reads.
		if lblRec, err = record.Create(o.RecordDir, "labels.jsonl"); err != nil {
			return Result{}, err
		}
		defer lblRec.Close()
	}

	res := Result{
		Population:    w.Describe(),
		DetectorNames: pipe.Names(),
	}

	// The scoring body lives in Scorer, shared verbatim with the real-data
	// path. Only the iteration differs: here the world is stepped tick by
	// tick, there a file is walked.
	sc := NewScorer(ScoreConfig{
		Pipeline:       pipe,
		Fusion:         fuse,
		Calibrator:     o.Calibrator,
		Propagator:     o.Propagator,
		PropagateEvery: o.PropagateEvery,
		AgentCap:       o.Sim.NumAgents + 1,
		EventW:         evRec,
		FindingW:       fndRec,
		LabelW:         lblRec,
	})
	lb := worldLabels{w}

	start := time.Now()
	for i := obs.Tick(0); i < o.Sim.Ticks; i++ {
		for _, e := range w.Step() {
			if err := sc.Observe(e, lb); err != nil {
				return Result{}, err
			}
		}
	}

	res.Elapsed = time.Since(start)
	res.Rows = sc.Rows()
	res.Peak = sc.Peak()
	res.Events = sc.Events()
	res.Findings = sc.Findings()
	w.Finish()
	res.World = w

	if o.RecordDir != "" {
		wf := record.WorldFile{
			Seed: o.Sim.Seed, Agents: o.Sim.NumAgents,
			Ticks: uint64(o.Sim.Ticks), MsPerTick: o.Sim.MsPerTick,
			Scenario: o.Scenario, Incidents: w.Truth.Incidents(),
		}
		for _, b := range w.Banks {
			wf.Banks = append(wf.Banks, b.Name)
		}
		for i := 1; i < len(w.Agents); i++ {
			a := &w.Agents[i]
			wf.Nodes = append(wf.Nodes, record.NodeRow{
				ID: a.ID, X: a.X, Y: a.Y,
				Kind: a.Archetype.String(), Bank: uint8(a.Bank),
			})
		}
		if err := record.WriteJSON(o.RecordDir, "world.json", wf); err != nil {
			return Result{}, err
		}
	}
	res.Hash = sc.Hash()
	res.Stats = w.Stats
	res.Incidents = w.Truth.Incidents()
	return res, nil
}

// worldLabels adapts the simulator's ledger to the Labels interface.
//
// The simulator is the only source that can answer Intensity meaningfully: it
// knows how far an injected attack had ramped at a given tick because it did
// the ramping. External data has no equivalent and reports 0.
type worldLabels struct{ w *sim.World }

func (l worldLabels) Label(id obs.TxID) truth.Label { return l.w.Truth.Label(id) }

func (l worldLabels) Incident(id obs.TxID) truth.IncidentID { return l.w.Truth.IncidentOf(id) }

func (l worldLabels) Archetype(id obs.AgentID) truth.Archetype { return l.w.Truth.Archetype(id) }

func (l worldLabels) Intensity(t obs.Tick) float64 {
	if sc := l.w.Scenario(); sc != nil {
		return sc.Intensity(t)
	}
	return 0
}
