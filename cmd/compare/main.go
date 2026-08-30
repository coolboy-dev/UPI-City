// Command compare evaluates several detector configurations against
// BYTE-IDENTICAL traffic.
//
// This is the project's premise demonstrated rather than asserted. The claim
// is that this is a testbed for measuring detection; the way to show that is
// to change one thing, hold the world fixed by seed, and report what moved.
// Without the determinism gate none of this would mean anything — the
// configurations would be facing different traffic and any difference could
// be the traffic rather than the change.
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
	"github.com/yug/upi-city/internal/risk"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/sim"
)

// config is one detector setup under test.
type config struct {
	Name      string
	Detectors []string
	Fusion    *risk.Fusion
	Calibrate bool
}

func configs() []config {
	max := risk.MaxFusion()
	all := []string{"velocity", "fanout", "cycle"}
	return []config{
		{Name: "velocity only", Detectors: []string{"velocity"}},
		{Name: "fanout only", Detectors: []string{"fanout"}},
		{Name: "cycle only", Detectors: []string{"cycle"}},
		{Name: "all three, max-wins", Detectors: all, Fusion: &max},
		{Name: "all three, weighted", Detectors: all},
		{Name: "all three, weighted+calibrated", Detectors: all, Calibrate: true},
	}
}

// result is one configuration's performance, aggregated over seeds.
type result struct {
	cfg      config
	aucpr    []float64
	prec     []float64
	rec      []float64
	fp1k     []float64
	latency  []float64
	missed   int
	total    int
	ece      float64
	reliable []risk.ReliabilityBin
}

func main() {
	scfg := sim.DefaultConfig()
	ccfg := chaos.DefaultConfig()

	seeds := flag.Int("seeds", 10, "seeds per configuration")
	fitSeeds := flag.Int("fit-seeds", 5, "how many leading seeds are reserved for fitting calibration")
	ticks := flag.Uint64("ticks", 40000, "ticks per run")
	scenario := flag.String("scenario", "fraud-ring", "chaos scenario")
	out := flag.String("out", "results", "output directory")
	flag.Parse()

	scfg.Ticks = obs.Tick(*ticks)

	if *fitSeeds >= *seeds {
		fmt.Fprintln(os.Stderr, "fit-seeds must be fewer than seeds: calibration has to be reported on data it never saw")
		os.Exit(2)
	}

	fmt.Printf("%d configurations x %d seeds x %d ticks, identical traffic per seed\n",
		len(configs()), *seeds, *ticks)
	fmt.Printf("calibration fitted on seeds 1-%d, reported on seeds %d-%d\n\n",
		*fitSeeds, *fitSeeds+1, *seeds)

	run := func(cfg config, seed uint64, cal *risk.Calibrator) (runner.Result, metrics.Report) {
		s := scfg
		s.Seed = seed
		res, err := runner.Run(runner.Options{
			Sim: s, Chaos: ccfg, Scenario: *scenario,
			Detectors: cfg.Detectors, Fusion: cfg.Fusion, Calibrator: cal,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "run:", err)
			os.Exit(1)
		}
		rep := metrics.Evaluate(res.Rows, res.Incidents, seed, s.MsPerTick)
		return res, rep
	}

	results := make([]result, len(configs()))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())

	for ci, cfg := range configs() {
		wg.Add(1)
		go func(ci int, cfg config) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r := result{cfg: cfg}

			// Fit calibration on the reserved seeds only.
			var cal *risk.Calibrator
			if cfg.Calibrate {
				var raw []float64
				var fraud []bool
				for s := 1; s <= *fitSeeds; s++ {
					res, _ := run(cfg, uint64(s), nil)
					for _, row := range res.Rows {
						raw = append(raw, row.Raw)
						fraud = append(fraud, row.Fraudulent())
					}
				}
				cal = risk.FitIsotonic(raw, fraud, 200)
				for s := 1; s <= *fitSeeds; s++ {
					cal.FitSeeds = append(cal.FitSeeds, uint64(s))
				}
			}

			// Evaluate on the held-out seeds. Every configuration is scored
			// on the SAME seeds, so the comparison is like for like.
			var calScores []float64
			var calFraud []bool
			for s := *fitSeeds + 1; s <= *seeds; s++ {
				res, rep := run(cfg, uint64(s), cal)
				r.aucpr = append(r.aucpr, rep.AUCPR)
				// Reported at a fixed 1% review budget rather than at best
				// F1. Best-F1 DEGENERATES for a weak detector: when a scorer
				// produces almost no non-zero scores, "flag everything"
				// genuinely maximises F1, and the row then reads recall 1.000
				// with 1000 false positives per 1000 legitimate transactions.
				// That is arithmetically correct and operationally absurd.
				// A fixed review budget cannot degenerate and puts every
				// configuration on the same operational footing.
				r.prec = append(r.prec, rep.AtBudget1pct.Precision)
				r.rec = append(r.rec, rep.AtBudget1pct.Recall)
				r.fp1k = append(r.fp1k, rep.AtBudget1pct.FPPer1kLegit)
				if rep.Latency.Total > rep.Latency.NeverDetected {
					r.latency = append(r.latency, rep.Latency.MedianSeconds)
				}
				r.missed += rep.Latency.NeverDetected
				r.total += rep.Latency.Total
				if cfg.Calibrate {
					for _, row := range res.Rows {
						calScores = append(calScores, row.Score)
						calFraud = append(calFraud, row.Fraudulent())
					}
				}
			}
			if cfg.Calibrate {
				r.reliable = risk.Reliability(calScores, calFraud, 10)
				r.ece = risk.ECE(r.reliable)
			}
			results[ci] = r
		}(ci, cfg)
	}
	wg.Wait()

	fmt.Print(table(results))

	for _, r := range results {
		if r.cfg.Calibrate && len(r.reliable) > 0 {
			fmt.Print(reliabilityTable(r))
		}
	}

	if *out != "" {
		var rel []risk.ReliabilityBin
		for _, r := range results {
			if r.cfg.Calibrate {
				rel = r.reliable
			}
		}
		if len(rel) > 0 {
			if err := os.WriteFile(*out+"/figures/reliability.svg",
				[]byte(plot.Reliability(rel)), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "figure:", err)
			}
		}
		if err := record.WriteJSON(*out, "comparison.json", jsonOf(results)); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Printf("\nwrote %s/comparison.json and %s/figures/reliability.svg\n", *out, *out)
	}
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func spread(v []float64) (lo, hi float64) {
	if len(v) == 0 {
		return 0, 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[0], c[len(c)-1]
}

func table(rs []result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%54s   %s\n", "", "─── at a 1% review budget ───")
	fmt.Fprintf(&b, "%-32s %18s %10s %8s %12s %9s %7s\n",
		"configuration", "AUC-PR", "precision", "recall", "FP/1k legit", "latency", "missed")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("─", 104))

	var bestAUC float64
	for _, r := range rs {
		if m := mean(r.aucpr); m > bestAUC {
			bestAUC = m
		}
	}
	for _, r := range rs {
		m := mean(r.aucpr)
		lo, hi := spread(r.aucpr)
		mark := "  "
		if m == bestAUC {
			mark = "★ "
		}
		fmt.Fprintf(&b, "%s%-30s %6.3f (%.3f-%.3f) %10.3f %8.3f %12.2f %8.1fs %7d\n",
			mark, r.cfg.Name, m, lo, hi,
			mean(r.prec), mean(r.rec), mean(r.fp1k), mean(r.latency), r.missed)
	}
	return b.String()
}

func reliabilityTable(r result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n─── calibration, on seeds the calibrator never saw ──────────\n")
	fmt.Fprintf(&b, "expected calibration error %.4f  (0 = perfectly on the diagonal)\n\n", r.ece)
	fmt.Fprintf(&b, "%-14s %8s %10s %10s   %s\n", "score bucket", "n", "predicted", "observed", "")
	for _, bin := range r.reliable {
		if bin.N == 0 {
			continue
		}
		gap := bin.Observed - bin.Predicted
		flag := ""
		if gap < -0.05 {
			flag = "over-confident"
		} else if gap > 0.05 {
			flag = "under-confident"
		}
		fmt.Fprintf(&b, "%.2f - %.2f    %8d %10.3f %10.3f   %s\n",
			bin.Lo, bin.Hi, bin.N, bin.Predicted, bin.Observed, flag)
	}
	return b.String()
}

func jsonOf(rs []result) any {
	type row struct {
		Name      string                `json:"name"`
		Detectors []string              `json:"detectors"`
		AUCPR     float64               `json:"aucpr_mean"`
		AUCPRMin  float64               `json:"aucpr_min"`
		AUCPRMax  float64               `json:"aucpr_max"`
		Precision float64               `json:"precision_mean"`
		Recall    float64               `json:"recall_mean"`
		FPPer1k   float64               `json:"fp_per_1k_legit_mean"`
		LatencyS  float64               `json:"latency_seconds_mean"`
		Missed    int                   `json:"incidents_never_detected"`
		Total     int                   `json:"incidents_total"`
		ECE       float64               `json:"expected_calibration_error,omitempty"`
		Bins      []risk.ReliabilityBin `json:"reliability,omitempty"`
	}
	out := make([]row, 0, len(rs))
	for _, r := range rs {
		lo, hi := spread(r.aucpr)
		out = append(out, row{
			Name: r.cfg.Name, Detectors: r.cfg.Detectors,
			AUCPR: mean(r.aucpr), AUCPRMin: lo, AUCPRMax: hi,
			Precision: mean(r.prec), Recall: mean(r.rec),
			FPPer1k: mean(r.fp1k), LatencyS: mean(r.latency),
			Missed: r.missed, Total: r.total,
			ECE: r.ece, Bins: r.reliable,
		})
	}
	return out
}
