// Command bench runs many seeds in parallel and reports the spread.
//
// A single run is an anecdote. Variance across seeds is the operational
// definition of "not cherry-picked": if precision swings from 0.4 to 0.9
// depending on which world you happened to simulate, then quoting the 0.9 is
// a choice about presentation rather than a measurement.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/plot"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/sim"
)

type seedResult struct {
	seed   uint64
	report metrics.Report
	err    error
}

func main() {
	scfg := sim.DefaultConfig()
	ccfg := chaos.DefaultConfig()

	seeds := flag.Int("seeds", 10, "number of seeds")
	ticks := flag.Uint64("ticks", 40000, "ticks per run")
	agents := flag.Int("agents", scfg.NumAgents, "agents per run")
	scenario := flag.String("scenario", "fraud-ring", "chaos scenario")
	detectors := flag.String("detectors", "velocity,fanout,cycle", "detector set")
	out := flag.String("out", "results", "directory for per-seed metrics.json")
	flag.Parse()

	scfg.Ticks = obs.Tick(*ticks)
	scfg.NumAgents = *agents

	fmt.Printf("%d seeds x %d ticks, scenario=%s, detectors=%s, %d cores\n\n",
		*seeds, *ticks, *scenario, *detectors, runtime.NumCPU())

	results := make([]seedResult, *seeds)
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())

	for i := 0; i < *seeds; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cfg := scfg
			cfg.Seed = uint64(i + 1)

			res, err := runner.Run(runner.Options{
				Sim: cfg, Chaos: ccfg,
				Scenario:  *scenario,
				Detectors: strings.Split(*detectors, ","),
			})
			if err != nil {
				results[i] = seedResult{seed: cfg.Seed, err: err}
				return
			}
			rep := metrics.Evaluate(res.Rows, res.Incidents, cfg.Seed, cfg.MsPerTick)
			rep.Ticks, rep.Agents = *ticks, *agents
			rep.Scenario, rep.Detectors = *scenario, res.DetectorNames
			results[i] = seedResult{seed: cfg.Seed, report: rep}
		}(i)
	}
	wg.Wait()

	var reports []metrics.Report
	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "seed %d: %v\n", r.seed, r.err)
			os.Exit(1)
		}
		reports = append(reports, r.report)
		if *out != "" {
			dir := fmt.Sprintf("%s/seed-%02d", *out, r.seed)
			if err := record.WriteJSON(dir, "metrics.json", r.report); err != nil {
				fmt.Fprintln(os.Stderr, "write:", err)
				os.Exit(1)
			}
		}
	}

	fmt.Print(perSeed(reports))
	fmt.Print(aggregate(reports))

	if *out != "" {
		if err := record.WriteJSON(*out, "summary.json", metrics.Summarise(reports)); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		if err := plot.WriteAll(*out+"/figures", reports); err != nil {
			fmt.Fprintln(os.Stderr, "figures:", err)
			os.Exit(1)
		}
		fmt.Printf("\nwrote %s/seed-NN/metrics.json, %s/summary.json, %s/figures/*.svg\n", *out, *out, *out)
	}
}

func perSeed(reps []metrics.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %7s %8s %9s %7s %11s %9s %8s\n",
		"seed", "prev%", "AUC-PR", "precision", "recall", "FP/1k legit", "latency", "missed")
	for _, r := range reps {
		lat := "never"
		if r.Latency.Total > r.Latency.NeverDetected {
			lat = fmt.Sprintf("%.1fs", r.Latency.MedianSeconds)
		}
		fmt.Fprintf(&b, "%-6d %7.2f %8.3f %9.3f %7.3f %11.2f %9s %8d\n",
			r.Seed, r.Prevalence*100, r.AUCPR,
			r.BestF1.Precision, r.BestF1.Recall, r.BestF1.FPPer1kLegit,
			lat, r.Latency.NeverDetected)
	}
	return b.String()
}

func aggregate(reps []metrics.Report) string {
	s := metrics.Summarise(reps)
	var b strings.Builder

	fmt.Fprintf(&b, "\n═══ across %d seeds ═════════════════════════════════════════\n", s.Seeds)
	line := func(name string, st metrics.Stat, scale float64, unit string) {
		fmt.Fprintf(&b, "%-18s %8.3f%s   (min %.3f, max %.3f)\n",
			name, st.Mean*scale, unit, st.Min*scale, st.Max*scale)
	}
	line("prevalence", s.Prevalence, 100, "%")
	line("AUC-PR", s.AUCPR, 1, "")
	line("precision", s.Precision, 1, "")
	line("recall", s.Recall, 1, "")
	line("FP per 1k legit", s.FPPer1k, 1, "")
	line("latency (s)", s.LatencySec, 1, "")

	fmt.Fprintf(&b, "\nbaselines (AUC-PR, mean across seeds)\n")
	names := make([]string, 0, len(s.BaselineAUCPR))
	for n := range s.BaselineAUCPR {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st := s.BaselineAUCPR[n]
		fmt.Fprintf(&b, "  %-14s %.3f   (min %.3f, max %.3f)\n", n, st.Mean, st.Min, st.Max)
	}

	fmt.Fprintf(&b, "\nincidents          %d total, %d NEVER DETECTED\n",
		s.IncidentsTotal, s.NeverDetected)
	fmt.Fprintf(&b, "negative control   %d/%d seeds passed label permutation\n",
		s.PermutationPassed, s.Seeds)
	return b.String()
}
