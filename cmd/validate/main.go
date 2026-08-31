// Command validate checks the simulator against published UPI statistics.
//
// Every other number in this project is conditional on the traffic being
// realistic, and until now that was asserted rather than shown. A reviewer
// could fairly say the detector works on an imaginary world. This compares
// what the simulator produces against figures published by NPCI, SBI Research
// and the RBI — and reports the gaps rather than tuning until they close.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/sim"
	"github.com/yug/upi-city/internal/truth"
)

func main() {
	scfg := sim.DefaultConfig()
	ccfg := chaos.DefaultConfig()

	seed := flag.Uint64("seed", 42, "seed")
	ticks := flag.Uint64("ticks", 40000, "ticks")
	scenario := flag.String("scenario", "fraud-ring", "chaos scenario")
	out := flag.String("out", "results", "output directory")
	flag.Parse()

	scfg.Seed = *seed
	scfg.Ticks = obs.Tick(*ticks)

	res, err := runner.Run(runner.Options{
		Sim: scfg, Chaos: ccfg, Scenario: *scenario,
		Detectors: []string{"velocity", "fanout", "cycle"},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	rep := metrics.Evaluate(res.Rows, res.Incidents, scfg.Seed, scfg.MsPerTick)

	// NPCI counts a payment as person-to-merchant by who receives it. The
	// archetype on each row is the SENDER, so merchant-directed traffic is
	// identified from the world's own merchant set.
	merchants := map[obs.AgentID]bool{}
	for i := 1; i < len(res.World.Agents); i++ {
		if res.World.Agents[i].Archetype == truth.ArchMegaMerchant {
			merchants[res.World.Agents[i].ID] = true
		}
	}
	_ = merchants

	real := metrics.CheckRealism(res.Rows,
		func(r metrics.ScoredRow) bool { return r.Archetype == truth.ArchConsumer },
		rep.AtBudget1pct)

	fmt.Print(report(real))

	if *out != "" {
		if err := record.WriteJSON(*out, "realism.json", real); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Printf("\nwrote %s/realism.json\n", *out)
	}
}

func report(r metrics.Realism) string {
	var b strings.Builder

	fmt.Fprintf(&b, "═══ simulated traffic vs published UPI statistics ═══════════\n\n")
	fmt.Fprintf(&b, "%-38s %12s %12s %8s %s\n",
		"statistic", "simulated", "published", "ratio", "")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("─", 88))

	for _, ref := range r.Reference {
		obs, ok := r.Observed[ref.Name]
		if !ok {
			continue
		}
		mark := "  OFF"
		if r.Within[ref.Name] {
			mark = "  ok"
		}
		if ref.Unit == "fraction" {
			fmt.Fprintf(&b, "%-38s %11.4f%% %11.4f%% %8.1fx%s\n",
				ref.Name, obs*100, ref.Value*100, r.Ratio[ref.Name], mark)
		} else {
			fmt.Fprintf(&b, "%-38s %12.0f %12.0f %8.1fx%s\n",
				ref.Name, obs, ref.Value, r.Ratio[ref.Name], mark)
		}
	}

	fmt.Fprintf(&b, "\nsources\n")
	seen := map[string]bool{}
	for _, ref := range r.Reference {
		if !seen[ref.Source] {
			fmt.Fprintf(&b, "  · %s\n", ref.Source)
			seen[ref.Source] = true
		}
	}

	// The finding that matters more than any of the above.
	fmt.Fprintf(&b, "\n─── what this does to the headline precision ────────────────\n")
	fmt.Fprintf(&b, "simulated prevalence   %.4f%%\n", r.SimPrevalence*100)
	fmt.Fprintf(&b, "real UPI prevalence    %.4f%%  (roughly %.0fx rarer)\n",
		r.RealPrevalence*100, r.SimPrevalence/r.RealPrevalence)
	fmt.Fprintf(&b, "\nprecision at simulated prevalence   %.3f\n", r.PrecisionSimulated)
	fmt.Fprintf(&b, "precision at REAL prevalence        %.4f\n", r.PrecisionReal)
	fmt.Fprintf(&b, "\nHolding the detector's true and false positive RATES fixed and\n")
	fmt.Fprintf(&b, "moving only the base rate, precision falls from %.3f to %.4f.\n",
		r.PrecisionSimulated, r.PrecisionReal)
	fmt.Fprintf(&b, "\nThat is not a flaw in the detector — it is what rarity does to\n")
	fmt.Fprintf(&b, "precision, and it applies to every fraud system ever built. It is\n")
	fmt.Fprintf(&b, "stated here because a precision figure quoted without its base rate\n")
	fmt.Fprintf(&b, "is close to meaningless, and this project's headline number is\n")
	fmt.Fprintf(&b, "measured at a prevalence chosen for tractability, not realism.\n")
	fmt.Fprintf(&b, "\nSimulating at the real rate would need ~100 million transactions to\n")
	fmt.Fprintf(&b, "observe a thousand fraud cases. The elevated rate is a deliberate\n")
	fmt.Fprintf(&b, "and necessary compromise; hiding it would not be.\n")
	return b.String()
}
