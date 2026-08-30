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
		Peak:          make([]float64, o.Sim.NumAgents+1),
		Population:    w.Describe(),
		DetectorNames: pipe.Names(),
	}
	res.Rows = make(metrics.Rows, 0, o.Sim.NumAgents*40)

	hash := obs.NewStreamHash()
	buf := make([]detect.Finding, 0, 16)
	start := time.Now()

	// Per-agent risk, carried between propagation passes.
	agentRisk := make([]float64, o.Sim.NumAgents+1)
	propagated := make([]float64, o.Sim.NumAgents+1)
	propEvery := o.PropagateEvery
	if propEvery == 0 {
		propEvery = 100
	}
	var lastProp obs.Tick

	for i := obs.Tick(0); i < o.Sim.Ticks; i++ {
		for _, e := range w.Step() {
			hash = hash.Add(e)
			res.Events++
			if evRec != nil {
				if err := evRec.Write(record.RowOf(e)); err != nil {
					return Result{}, err
				}
			}

			// Detection sees only the observable event.
			buf = pipe.Observe(e, buf[:0])

			// Fuse the detectors' findings into one score for this
			// transaction, keeping each detector's contribution.
			var bestDet string
			var bestRaw float64
			for _, f := range buf {
				res.Findings++
				if int(f.Agent) < len(res.Peak) && f.Score > res.Peak[f.Agent] {
					res.Peak[f.Agent] = f.Score
				}
				if f.Score > bestRaw {
					bestRaw, bestDet = f.Score, f.Detector
				}
				if fndRec != nil {
					if err := fndRec.Write(f); err != nil {
						return Result{}, err
					}
				}
			}
			rs := fuse.ScoreWith(buf)
			score := rs.Raw

			if o.Propagator != nil {
				o.Propagator.Observe(e)
				if int(e.From) < len(agentRisk) && rs.Raw > agentRisk[e.From] {
					agentRisk[e.From] = rs.Raw
				}
				// The pass is periodic, not per-transaction: running a
				// two-round graph walk on every payment would dominate the
				// loop and buy nothing, since the graph barely moves between
				// consecutive transactions.
				if e.SettleTick-lastProp >= propEvery {
					propagated = o.Propagator.Adjust(agentRisk, e.SettleTick)
					lastProp = e.SettleTick
				}
				if int(e.From) < len(propagated) && propagated[e.From] > score {
					score = propagated[e.From]
				}
			}

			if o.Calibrator != nil {
				score = o.Calibrator.Apply(score)
			}

			// EVERY transaction becomes a row, including the vast majority
			// nothing flagged. Evaluating only flagged transactions would make
			// recall unmeasurable and precision meaningless.
			// Attack strength at the moment the payment was initiated, not
			// when it settled — the ramp is a property of the attacker.
			var intensity float64
			if sc := w.Scenario(); sc != nil {
				intensity = sc.Intensity(e.Tick)
			}

			if lblRec != nil {
				if err := lblRec.Write(record.LabelRow{
					TxID:      e.TxID,
					Label:     w.Truth.Label(e.TxID),
					Incident:  w.Truth.IncidentOf(e.TxID),
					Archetype: w.Truth.Archetype(e.From),
					Intensity: intensity,
				}); err != nil {
					return Result{}, err
				}
			}

			res.Rows = append(res.Rows, metrics.ScoredRow{
				TxID:          e.TxID,
				Tick:          e.SettleTick,
				Intensity:     intensity,
				From:          e.From,
				AmountP:       e.AmountP,
				Failed:        e.Status != obs.StatusSuccess,
				Score:         score,
				Raw:           rs.Raw,
				Detector:      bestDet,
				Contributions: rs.Contributions,
				Label:         w.Truth.Label(e.TxID),
				Archetype:     w.Truth.Archetype(e.From),
				Incident:      w.Truth.IncidentOf(e.TxID),
			})
		}
	}

	res.Elapsed = time.Since(start)
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
	res.Hash = hash
	res.Stats = w.Stats
	res.Incidents = w.Truth.Incidents()
	return res, nil
}
