// Command propagation measures whether spreading suspicion along the
// transaction graph actually helps.
//
// Three arms on byte-identical traffic: no propagation, naive propagation,
// and degree-normalised propagation with hub damping. The result is reported
// whichever way it comes out — an intervention that does not pay for itself
// is a finding, not a failure, and quietly dropping it would be the dishonest
// move.
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
	"github.com/yug/upi-city/internal/truth"
)

type arm struct {
	name string
	make func() *risk.Propagator
}

func arms() []arm {
	return []arm{
		{"off (control)", func() *risk.Propagator { return nil }},
		{"naive", risk.NewNaivePropagator},
		{"normalised + hub-damped", risk.NewPropagator},
	}
}

type result struct {
	name   string
	aucpr  []float64
	prec   []float64
	rec    []float64
	fp1k   []float64
	caught []float64
	// merchantFP is the number the trap predicts will explode: false
	// positives landing on large merchants, who are guilty of nothing but
	// popularity.
	merchantFP []float64
}

func main() {
	scfg := sim.DefaultConfig()
	ccfg := chaos.DefaultConfig()

	seeds := flag.Int("seeds", 6, "seeds per arm")
	ticks := flag.Uint64("ticks", 40000, "ticks per run")
	scenario := flag.String("scenario", "fraud-ring", "chaos scenario")
	out := flag.String("out", "results", "output directory")
	flag.Parse()

	scfg.Ticks = obs.Tick(*ticks)

	fmt.Printf("%d arms x %d seeds x %d ticks, identical traffic per seed\n\n",
		len(arms()), *seeds, *ticks)

	results := make([]result, len(arms()))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())

	for ai, a := range arms() {
		wg.Add(1)
		go func(ai int, a arm) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r := result{name: a.name}
			for s := 1; s <= *seeds; s++ {
				cfg := scfg
				cfg.Seed = uint64(s)

				res, err := runner.Run(runner.Options{
					Sim: cfg, Chaos: ccfg, Scenario: *scenario,
					Detectors:  []string{"velocity", "fanout", "cycle"},
					Propagator: a.make(),
				})
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				rep := metrics.Evaluate(res.Rows, res.Incidents, cfg.Seed, cfg.MsPerTick)

				r.aucpr = append(r.aucpr, rep.AUCPR)
				r.prec = append(r.prec, rep.AtBudget1pct.Precision)
				r.rec = append(r.rec, rep.AtBudget1pct.Recall)
				r.fp1k = append(r.fp1k, rep.AtBudget1pct.FPPer1kLegit)
				r.caught = append(r.caught, rep.Deployed.Caught)
				r.merchantFP = append(r.merchantFP, merchantFPRate(rep))
			}
			results[ai] = r
		}(ai, a)
	}
	wg.Wait()

	fmt.Print(table(results))
	fmt.Print(verdict(results))

	if *out != "" {
		if err := record.WriteJSON(*out, "propagation.json", jsonOf(results)); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		var pa []plot.PropagationArm
		for _, r := range results {
			pa = append(pa, plot.PropagationArm{
				Name: r.name, AUCPR: mean(r.aucpr), MerchantFP1k: mean(r.merchantFP),
			})
		}
		_ = os.MkdirAll(*out+"/figures", 0o755)
		if err := os.WriteFile(*out+"/figures/propagation.svg",
			[]byte(plot.Propagation(pa)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "figure:", err)
		}
		fmt.Printf("\nwrote %s/propagation.json\n", *out)
	}
}

// merchantFPRate is false positives per 1,000 legitimate transactions landing
// specifically on large merchants — the population the trap predicts will be
// harmed by naive propagation.
func merchantFPRate(r metrics.Report) float64 {
	for _, a := range r.FPByArch {
		if a.Archetype == truth.ArchMegaMerchant.String() {
			return a.FPPer1kThis
		}
	}
	return 0
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

func spread(v []float64) (float64, float64) {
	if len(v) == 0 {
		return 0, 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[0], c[len(c)-1]
}

func table(rs []result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%54s   %s\n", "", "── at a 1%% review budget ──")
	fmt.Fprintf(&b, "%-26s %20s %10s %8s %12s %14s\n",
		"propagation", "AUC-PR", "precision", "recall", "FP/1k legit", "merchant FP/1k")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("─", 96))
	for _, r := range rs {
		lo, hi := spread(r.aucpr)
		fmt.Fprintf(&b, "%-26s %7.3f (%.3f-%.3f) %10.3f %8.3f %12.2f %14.2f\n",
			r.name, mean(r.aucpr), lo, hi,
			mean(r.prec), mean(r.rec), mean(r.fp1k), mean(r.merchantFP))
	}
	return b.String()
}

// verdict states plainly whether the intervention earned its place.
func verdict(rs []result) string {
	if len(rs) < 3 {
		return ""
	}
	off, naive, norm := rs[0], rs[1], rs[2]
	var b strings.Builder

	fmt.Fprintf(&b, "\n─── verdict ─────────────────────────────────────────────────\n")

	dNaive := mean(naive.aucpr) - mean(off.aucpr)
	dNorm := mean(norm.aucpr) - mean(off.aucpr)

	fmt.Fprintf(&b, "naive propagation        AUC-PR %+.3f vs control, merchant FP/1k %.2f -> %.2f\n",
		dNaive, mean(off.merchantFP), mean(naive.merchantFP))
	fmt.Fprintf(&b, "normalised + hub-damped  AUC-PR %+.3f vs control, merchant FP/1k %.2f -> %.2f\n",
		dNorm, mean(off.merchantFP), mean(norm.merchantFP))

	fmt.Fprintf(&b, "\n")
	switch {
	case dNorm > 0.01:
		fmt.Fprintf(&b, "KEEP IT: normalised propagation improves detection on identical traffic.\n")
	case dNorm < -0.01:
		fmt.Fprintf(&b, "DROP IT: propagation makes detection WORSE even when normalised.\n"+
			"That is the honest result. Guilt by association is intuitive and, on this\n"+
			"traffic, actively harmful — reporting it as a null result is worth more\n"+
			"than shipping it because it sounded clever.\n")
	default:
		fmt.Fprintf(&b, "NO EFFECT: propagation neither helps nor hurts measurably here.\n"+
			"It stays behind a flag, off by default. An intervention that changes\n"+
			"nothing is complexity without benefit.\n")
	}
	return b.String()
}

func jsonOf(rs []result) any {
	type row struct {
		Name         string  `json:"name"`
		AUCPR        float64 `json:"aucpr_mean"`
		AUCPRMin     float64 `json:"aucpr_min"`
		AUCPRMax     float64 `json:"aucpr_max"`
		Precision    float64 `json:"precision_mean"`
		Recall       float64 `json:"recall_mean"`
		FPPer1k      float64 `json:"fp_per_1k_legit"`
		MerchantFP1k float64 `json:"merchant_fp_per_1k"`
		Caught       float64 `json:"caught_at_budget"`
	}
	out := make([]row, 0, len(rs))
	for _, r := range rs {
		lo, hi := spread(r.aucpr)
		out = append(out, row{
			Name: r.name, AUCPR: mean(r.aucpr), AUCPRMin: lo, AUCPRMax: hi,
			Precision: mean(r.prec), Recall: mean(r.rec),
			FPPer1k: mean(r.fp1k), MerchantFP1k: mean(r.merchantFP),
			Caught: mean(r.caught),
		})
	}
	return out
}
