// Command drift measures how detection quality decays as the legitimate
// population changes underneath a fixed detector.
//
// Every other number in this project is measured on a world whose legitimate
// behaviour is frozen for the entire run. That quietly makes all of them an
// upper bound, because the thing that actually degrades production fraud
// systems is not a cleverer attacker — it is ordinary customers slowly
// behaving differently, while the detector's assumptions stay where they were.
//
// The specific question here is sharper than "does it get worse", because this
// detector has two parts that age differently:
//
//   - RANKING is driven by per-agent EWMA baselines, which adapt by
//     construction. They should absorb drift.
//   - CALIBRATION is an isotonic fit frozen at training time, and the decision
//     thresholds sit on top of it. Nothing about it adapts.
//
// So the interesting failure is not that the detector stops ranking fraud
// above non-fraud. It is that it keeps ranking correctly while the probability
// attached to that ranking silently stops being true — which is worse, because
// the ranking looks fine on a PR curve while the review queue quietly fills
// with the wrong volume.
//
// Reported per time bucket: AUC-PR (ranking) and expected calibration error
// (whether the number means what it says).
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/plot"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/risk"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/sim"
)

// Bucket is one time slice of a run.
type Bucket struct {
	From obs.Tick `json:"from_tick"`
	To   obs.Tick `json:"to_tick"`
	Rows       int      `json:"rows"`
	Fraud      int      `json:"fraud"`
	Prevalence float64  `json:"prevalence"`
	AUCPR      float64  `json:"aucpr"`
	// Lift is AUC-PR over prevalence. Comparing raw AUC-PR across buckets is
	// invalid when prevalence differs between them, which it does.
	Lift float64 `json:"lift_over_chance"`
	ECE  float64 `json:"ece"`
	// DriftFactor is how much larger legitimate activity is than at t=0.
	DriftFactor float64 `json:"drift_factor"`
}

// Arm is one configuration of the experiment.
type Arm struct {
	Name          string   `json:"name"`
	DriftPerKTick float64  `json:"drift_per_k_tick"`
	Buckets       []Bucket `json:"buckets"`
}

func main() {
	seed := flag.Uint64("seed", 42, "seed")
	ticks := flag.Uint64("ticks", 120000, "ticks (long, so drift has room to act)")
	nbuckets := flag.Int("buckets", 6, "time buckets")
	rate := flag.Float64("drift", 0.006, "legitimate activity growth per 1,000 ticks (0.006 ≈ 2x over a 120k-tick run)")
	out := flag.String("out", "results", "output directory")
	flag.Parse()

	// A dose-response sweep rather than one on/off comparison, because the
	// on/off comparison at a realistic rate returns a null result and a single
	// null is easy to mistake for "not measured". Sweeping shows both where
	// the design holds and where it breaks.
	doses := []struct {
		name string
		d    float64
	}{
		{"none (control)", 0},
		{"1.9x over run", *rate},
		{"3.6x over run", *rate * 2},
		{"13x over run", *rate * 4},
		{"180x over run", *rate * 8},
	}

	var arms []Arm
	for _, a := range doses {
		arm, err := run(*seed, obs.Tick(*ticks), a.d, *nbuckets)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		arm.Name = a.name
		arms = append(arms, arm)
	}

	fmt.Print(report(arms, *rate))
	fmt.Print(summary(arms))

	if *out != "" {
		if err := record.WriteJSON(*out, "drift.json", arms); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		svg := *out + "/figures/drift.svg"
		if err := os.WriteFile(svg, []byte(plot.Drift(toPlot(arms))), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Printf("\nwrote %s/drift.json and %s\n", *out, svg)
	}
}

func run(seed uint64, ticks obs.Tick, drift float64, nbuckets int) (Arm, error) {
	scfg := sim.DefaultConfig()
	scfg.Seed = seed
	scfg.Ticks = ticks
	scfg.DriftPerKTick = drift

	// Fraud must be present in every bucket or most buckets have no positives
	// and AUC-PR is undefined there. The ring therefore runs for essentially
	// the whole simulation rather than as a single episode.
	ccfg := chaos.DefaultConfig()
	ccfg.StartTick = 4000
	ccfg.Duration = ticks
	ccfg.RampTicks = 4000

	res, err := runner.Run(runner.Options{
		Sim: scfg, Chaos: ccfg, Scenario: "fraud-ring",
		Detectors: []string{"velocity", "fanout", "cycle"},
	})
	if err != nil {
		return Arm{}, err
	}

	// The calibrator is fitted ONCE, on the earliest bucket only. That is the
	// point of the experiment: it represents a model trained at deployment and
	// never refreshed. Fitting it across the whole run would hide exactly the
	// decay being measured.
	rows := res.Rows
	sort.Slice(rows, func(i, j int) bool { return rows[i].Tick < rows[j].Tick })

	arm := Arm{DriftPerKTick: drift}
	width := ticks / obs.Tick(nbuckets)

	var fitRaw []float64
	var fitFraud []bool
	for _, r := range rows {
		if r.Tick < width {
			fitRaw = append(fitRaw, r.Raw)
			fitFraud = append(fitFraud, r.Fraudulent())
		}
	}
	cal := risk.FitIsotonic(fitRaw, fitFraud, 50)

	for b := 0; b < nbuckets; b++ {
		lo := obs.Tick(b) * width
		hi := lo + width

		var sub metrics.Rows
		var calibrated []float64
		var fraud []bool
		for _, r := range rows {
			if r.Tick < lo || r.Tick >= hi {
				continue
			}
			sub = append(sub, r)
			calibrated = append(calibrated, cal.Apply(r.Raw))
			fraud = append(fraud, r.Fraudulent())
		}
		if len(sub) == 0 {
			continue
		}

		prev := sub.Prevalence()
		aucpr := metrics.AUCPR(metrics.Sweep(sub, metrics.DefaultTaus()))
		lift := 0.0
		if prev > 0 {
			lift = aucpr / prev
		}
		bk := Bucket{
			From: lo, To: hi,
			Rows: len(sub), Fraud: sub.Positives(),
			Prevalence: prev, AUCPR: aucpr, Lift: lift,
			ECE:         risk.ECE(risk.Reliability(calibrated, fraud, 10)),
			DriftFactor: math.Pow(1+drift, float64(lo+width/2)/1000),
		}
		arm.Buckets = append(arm.Buckets, bk)
	}
	return arm, nil
}

// summary is the dose-response table: one row per drift rate, showing what
// survived and what did not.
func summary(arms []Arm) string {
	var b strings.Builder
	fmt.Fprintf(&b, "─── dose response, first bucket vs last ────────────────────\n")
	fmt.Fprintf(&b, "%-18s %10s %18s %18s\n", "drift", "legit x", "ranking lift", "calibration ECE")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("─", 68))
	for _, a := range arms {
		if len(a.Buckets) < 2 {
			continue
		}
		f, l := a.Buckets[0], a.Buckets[len(a.Buckets)-1]
		fmt.Fprintf(&b, "%-18s %9.1fx %6.1fx → %-7s %8.4f → %-8.4f\n",
			a.Name, l.DriftFactor, f.Lift, fmt.Sprintf("%.1fx", l.Lift), f.ECE, l.ECE)
	}
	return b.String()
}

func toPlot(arms []Arm) []plot.DriftArm {
	var out []plot.DriftArm
	for _, a := range arms {
		pa := plot.DriftArm{Name: a.Name}
		for _, b := range a.Buckets {
			pa.Tick = append(pa.Tick, float64(b.From))
			pa.Lift = append(pa.Lift, b.Lift)
			pa.ECE = append(pa.ECE, b.ECE)
		}
		out = append(out, pa)
	}
	return out
}

func report(arms []Arm, rate float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "═══ concept drift: does the detector age? ═══════════════════\n\n")
	fmt.Fprintf(&b, "Legitimate activity grows %.1f%% per 1,000 ticks, compounding.\n", rate*100)
	fmt.Fprintf(&b, "The calibrator is fitted on the FIRST bucket only and never\n")
	fmt.Fprintf(&b, "refreshed, which is what a deployed-and-left model looks like.\n\n")

	for _, a := range arms {
		fmt.Fprintf(&b, "─── %s ───\n", a.Name)
		fmt.Fprintf(&b, "%-13s %8s %11s %8s %8s %8s\n",
			"ticks", "legit x", "prevalence", "AUC-PR", "lift", "ECE")
		fmt.Fprintf(&b, "%s\n", strings.Repeat("─", 62))
		for _, k := range a.Buckets {
			fmt.Fprintf(&b, "%6d-%-6d %8.2fx %10.3f%% %8.3f %7.1fx %8.4f\n",
				k.From, k.To, k.DriftFactor, k.Prevalence*100, k.AUCPR, k.Lift, k.ECE)
		}
		fmt.Fprintln(&b)
	}

	if len(arms) == 2 && len(arms[0].Buckets) > 1 && len(arms[1].Buckets) > 1 {
		c, d := arms[0], arms[1]
		cf, df := c.Buckets[0], d.Buckets[0]
		cl, dl := c.Buckets[len(c.Buckets)-1], d.Buckets[len(d.Buckets)-1]
		fmt.Fprintf(&b, "─── what aged ──────────────────────────────────────────────\n")
		fmt.Fprintf(&b, "ranking (lift over chance)   control %.1fx → %.1fx   drift %.1fx → %.1fx\n",
			cf.Lift, cl.Lift, df.Lift, dl.Lift)
		fmt.Fprintf(&b, "calibration (ECE, lower=better)  control %.4f → %.4f   drift %.4f → %.4f\n",
			cf.ECE, cl.ECE, df.ECE, dl.ECE)
	}
	return b.String()
}
