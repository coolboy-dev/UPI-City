// Command harness runs the simulation headless, scores it, and reports
// detector performance against ground truth.
//
// The harness exists before the dashboard on purpose. The metrics ARE the
// deliverable, and only this path produces them; a run here is reproducible,
// scriptable across seeds, and independent of anything in a browser.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

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

	seed := flag.Uint64("seed", scfg.Seed, "master seed")
	ticks := flag.Uint64("ticks", uint64(scfg.Ticks), "ticks to simulate")
	agents := flag.Int("agents", scfg.NumAgents, "number of agents")
	out := flag.String("out", "", "output directory (empty = do not record)")
	quiet := flag.Bool("quiet", false, "print only the stream hash")
	diag := flag.Bool("diag", false, "print the per-archetype balance table")
	workers := flag.Int("workers", 0, "decide-phase shards (0 = one per core, 1 = serial)")

	scenario := flag.String("scenario", "", "chaos scenario: "+strings.Join(chaos.Names(), ", "))
	detectors := flag.String("detectors", "velocity,fanout,cycle", "detector set, comma separated")
	scores := flag.Bool("scores", false, "print peak-score separation by ground-truth group")
	evaluate := flag.Bool("metrics", true, "print the full detection-performance report")

	flag.Uint64Var((*uint64)(&ccfg.StartTick), "chaos-start", uint64(ccfg.StartTick), "tick at which chaos begins")
	flag.Uint64Var((*uint64)(&ccfg.RampTicks), "chaos-ramp", uint64(ccfg.RampTicks), "ticks to reach full intensity")
	flag.Uint64Var((*uint64)(&ccfg.Duration), "chaos-duration", uint64(ccfg.Duration), "how long chaos lasts")
	flag.IntVar(&ccfg.RingSize, "ring-size", ccfg.RingSize, "fraud ring member count")
	flag.IntVar(&ccfg.Victims, "victims", ccfg.Victims, "compromised accounts feeding the ring")
	flag.Float64Var(&ccfg.OutageFailRate, "outage-fail-rate", ccfg.OutageFailRate, "peak PSP failure rate during an outage")

	// ── The difficulty dial ────────────────────────────────────────────────
	//
	// -difficulty loads a preset; the individual knobs below still override it
	// afterwards, so a sweep can use named levels while an experiment can move
	// one variable at a time. See chaos.difficulties for what each knob is
	// known to defeat.
	difficulty := flag.String("difficulty", "",
		"fraud difficulty preset: "+strings.Join(chaos.DifficultyNames(), ", ")+
			" (empty keeps the standard defaults)")
	flag.IntVar(&ccfg.Hops, "hops", ccfg.Hops,
		"laundering chain length — beyond the cycle search depth of 4 a ring is structurally invisible")
	flag.Float64Var(&ccfg.MuleRate, "mule-rate", ccfg.MuleRate,
		"chance per tick an active mule forwards; lower is quieter and defeats velocity")
	flag.Float64Var(&ccfg.MuleAmountRupees, "mule-amount", ccfg.MuleAmountRupees,
		"median forwarded payment in rupees; smaller hides inside ordinary traffic")
	flag.Float64Var(&ccfg.TakeoverRate, "takeover-rate", ccfg.TakeoverRate,
		"chance per tick a compromised account pays into the ring")
	flag.Parse()

	// Applied before the individual flags are read back, so an explicit knob
	// still wins over the preset that named it.
	if *difficulty != "" {
		preset, ok := chaos.Difficulty(*difficulty)
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown difficulty %q — have: %s\n",
				*difficulty, strings.Join(chaos.DifficultyNames(), ", "))
			os.Exit(2)
		}
		// Preserve anything the user set explicitly on the command line.
		set := map[string]bool{}
		flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
		merged := preset
		if set["chaos-start"] {
			merged.StartTick = ccfg.StartTick
		}
		if set["chaos-ramp"] {
			merged.RampTicks = ccfg.RampTicks
		}
		if set["chaos-duration"] {
			merged.Duration = ccfg.Duration
		}
		if set["ring-size"] {
			merged.RingSize = ccfg.RingSize
		}
		if set["victims"] {
			merged.Victims = ccfg.Victims
		}
		if set["hops"] {
			merged.Hops = ccfg.Hops
		}
		if set["mule-rate"] {
			merged.MuleRate = ccfg.MuleRate
		}
		if set["mule-amount"] {
			merged.MuleAmountRupees = ccfg.MuleAmountRupees
		}
		if set["takeover-rate"] {
			merged.TakeoverRate = ccfg.TakeoverRate
		}
		if set["outage-fail-rate"] {
			merged.OutageFailRate = ccfg.OutageFailRate
		}
		ccfg = merged
		if !*quiet {
			fmt.Printf("difficulty %s — ring %d, %d hops, mule rate %.3f, "+
				"median forward ₹%.0f, ramp %d ticks\n",
				*difficulty, ccfg.RingSize, ccfg.Hops, ccfg.MuleRate,
				ccfg.MuleAmountRupees, ccfg.RampTicks)
		}
	}

	scfg.Seed = *seed
	scfg.Ticks = obs.Tick(*ticks)
	scfg.NumAgents = *agents

	res, err := runner.Run(runner.Options{
		Workers:   *workers,
		Sim:       scfg,
		Chaos:     ccfg,
		Scenario:  *scenario,
		Detectors: strings.Split(*detectors, ","),
		RecordDir: *out,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}

	if *quiet {
		fmt.Printf("%016x\n", uint64(res.Hash))
		return
	}

	fmt.Printf("seed=%d ticks=%d scenario=%s detectors=%s\n%s\n\n",
		scfg.Seed, scfg.Ticks, orNone(*scenario), strings.Join(res.DetectorNames, "+"), res.Population)

	st := res.Stats
	fmt.Printf("stream hash   %016x\n", uint64(res.Hash))
	fmt.Printf("transactions  %d created, %d settled, %d failed, %d timed out, %d retried, %d declined\n",
		st.Created, st.Settled, st.Failed, st.TimedOut, st.Retried, st.Declined)
	fmt.Printf("findings      %d over %d events\n", res.Findings, res.Events)
	fmt.Printf("wall clock    %s (%.0f ticks/s, %.0f tx/s)\n",
		res.Elapsed.Round(time.Millisecond),
		float64(scfg.Ticks)/res.Elapsed.Seconds(),
		float64(st.Created)/res.Elapsed.Seconds())

	for _, in := range res.Incidents {
		fmt.Printf("incident #%d   %s injected at tick %d, ran to %d, %d members, %d transactions\n",
			in.ID, in.Kind, in.StartTick, in.EndTick, len(in.Members), len(in.TxIDs))
	}
	fmt.Println()

	var rep metrics.Report
	if *evaluate {
		rep = metrics.Evaluate(res.Rows, res.Incidents, scfg.Seed, scfg.MsPerTick)
		rep.Ticks, rep.Agents = *ticks, *agents
		rep.Scenario, rep.Detectors = *scenario, res.DetectorNames
		fmt.Print(rep.String())
	}

	if *scores {
		fmt.Printf("\n%s", separation(res))
	}
	if *diag {
		fmt.Printf("\n%s", res.World.BalanceReport())
	}

	if *out != "" {
		if err := record.WriteJSON(*out, "metrics.json", rep); err != nil {
			fmt.Fprintln(os.Stderr, "metrics:", err)
			os.Exit(1)
		}
		if err := record.WriteJSON(*out, "meta.json", map[string]any{
			"seed": scfg.Seed, "ticks": scfg.Ticks, "agents": scfg.NumAgents,
			"scenario": *scenario, "detectors": res.DetectorNames,
			"stream_hash": fmt.Sprintf("%016x", uint64(res.Hash)),
			"incidents":   res.Incidents,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "meta:", err)
			os.Exit(1)
		}
		fmt.Printf("\nrecorded      %s/{events,findings}.jsonl, metrics.json\n", *out)
	}
}

// separation reports peak score by ground-truth group.
//
// Two things must hold, and the second matters more: ring members must score
// above ordinary traffic, AND the hard negatives must score above zero. A run
// where the legitimate merchants, payroll and supply chains all score zero has
// not shown the detectors work — it has shown the traffic is too easy to be
// evidence of anything.
func separation(res runner.Result) string {
	member := map[obs.AgentID]bool{}
	arch := map[obs.AgentID]truth.Archetype{}
	for _, in := range res.Incidents {
		for _, m := range in.Members {
			member[m] = true
		}
	}
	for _, r := range res.Rows {
		arch[r.From] = r.Archetype
	}

	groups := map[string][]float64{}
	for id := 1; id < len(res.Peak); id++ {
		aid := obs.AgentID(id)
		a := arch[aid]
		switch {
		case member[aid]:
			groups["FRAUD "+a.String()] = append(groups["FRAUD "+a.String()], res.Peak[id])
		case a.HardNegative():
			groups["hard-neg "+a.String()] = append(groups["hard-neg "+a.String()], res.Peak[id])
		default:
			groups["normal "+a.String()] = append(groups["normal "+a.String()], res.Peak[id])
		}
	}

	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	fmt.Fprintf(&b, "peak detector score by ground-truth group (detectors never saw these labels)\n")
	fmt.Fprintf(&b, "%-26s %5s %8s %8s %8s %8s\n", "group", "n", "median", "p90", "max", "scored>0")
	for _, n := range names {
		s := groups[n]
		sort.Float64s(s)
		var nz int
		for _, v := range s {
			if v > 0 {
				nz++
			}
		}
		fmt.Fprintf(&b, "%-26s %5d %8.3f %8.3f %8.3f %8d\n",
			n, len(s), q(s, 0.5), q(s, 0.9), q(s, 1.0), nz)
	}
	return b.String()
}

func q(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(p*float64(len(sorted)-1))]
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
