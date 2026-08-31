// Command feedback measures what an analyst review queue actually teaches you.
//
// A production fraud system's most valuable asset is supposed to be its
// feedback loop: analysts work the review queue, their verdicts become labels,
// the labels retrain the model. This project has had a review queue since the
// decision layer was built, and it went nowhere — nothing was ever labelled
// back. That is the gap this closes.
//
// ─── The thing being measured is not "does retraining help" ─────────────────
//
// It is a specific structural problem with the loop, and it is easy to state:
// you only ever get labels on transactions you already flagged. Anything the
// detector scored below the review threshold is never looked at, never
// labelled, and therefore never learned from. The training set is not a sample
// of fraud; it is a sample of fraud YOU ALREADY CATCH.
//
// So a detector retrained on its own review queue should get better at what it
// is already good at and stay exactly as blind to everything else — and it
// should do measurably worse than the same labelling budget spent at random.
// That second comparison is the important one, because it isolates selection
// bias from sample size. Spending 2% of your analyst hours on random audits
// rather than queue review costs the same money.
//
// Three regimes, identical budget where it matters:
//
//	reviewed   label the top-scoring rows, as a real queue does
//	random     label the same NUMBER of rows, chosen at random
//	oracle     label everything (impossible in production, included as a ceiling)
//
// Each refits the fusion weights, and each is then evaluated on a later period
// of the same run that none of them was fitted on.
package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/risk"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/sim"
)

var detectors = []string{"velocity", "fanout", "cycle"}

// Regime is one labelling policy and what it produced.
type Regime struct {
	Name     string             `json:"name"`
	Labelled int                `json:"rows_labelled"`
	Fraud    int                `json:"fraud_rows_labelled"`
	Weights  map[string]float64 `json:"refit_weights"`
	// EvalAUCPR is measured on a later period none of the regimes saw.
	EvalAUCPR float64 `json:"eval_aucpr"`
	EvalLift  float64 `json:"eval_lift_over_chance"`
	// ScamAUCPR is the same weights applied to the attack type the detector
	// was never built for, which is where selection bias should bite hardest.
	ScamAUCPR float64 `json:"scam_aucpr"`
	ScamLift  float64 `json:"scam_lift_over_chance"`
}

func main() {
	seed := flag.Uint64("seed", 42, "seed")
	ticks := flag.Uint64("ticks", 80000, "ticks")
	budget := flag.Float64("budget", 0.02, "share of transactions analysts can review")
	out := flag.String("out", "results", "output directory")
	flag.Parse()

	train, eval, err := split(*seed, obs.Tick(*ticks), "fraud-ring")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// The untuned attack, used only for evaluation. No regime ever trains on it.
	_, scam, err := split(*seed, obs.Tick(*ticks), "scam-payout")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	n := int(float64(len(train)) * *budget)
	rng := rand.New(rand.NewPCG(*seed, 99))

	regimes := []Regime{
		fit("reviewed queue", topN(train, n), eval, scam),
		fit("random audit", sampleN(train, n, rng), eval, scam),
		fit("oracle (all labels)", train, eval, scam),
	}

	fmt.Print(report(regimes, eval, scam, *budget, n, len(train)))

	if *out != "" {
		if err := record.WriteJSON(*out, "feedback.json", regimes); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Printf("\nwrote %s/feedback.json\n", *out)
	}
}

// split runs the simulation and cuts it in half by time. Weights are fitted on
// the first half and judged on the second, so no regime is ever scored on a
// period it was fitted to.
func split(seed uint64, ticks obs.Tick, scenario string) (train, eval metrics.Rows, err error) {
	scfg := sim.DefaultConfig()
	scfg.Seed, scfg.Ticks = seed, ticks

	ccfg := chaos.DefaultConfig()
	ccfg.StartTick, ccfg.Duration, ccfg.RampTicks = 2000, ticks, 3000

	res, err := runner.Run(runner.Options{
		Sim: scfg, Chaos: ccfg, Scenario: scenario, Detectors: detectors,
	})
	if err != nil {
		return nil, nil, err
	}
	rows := res.Rows
	sort.Slice(rows, func(i, j int) bool { return rows[i].Tick < rows[j].Tick })
	mid := ticks / 2
	for _, r := range rows {
		if r.Tick < mid {
			train = append(train, r)
		} else {
			eval = append(eval, r)
		}
	}
	return train, eval, nil
}

// detectorScores recovers each detector's own score from the stored signed
// contributions. A contribution is weight × score with the deployed weights,
// so dividing back out recovers the score the detector actually emitted.
func detectorScores(r metrics.ScoredRow) map[string]float64 {
	base := risk.DefaultFusion()
	out := make(map[string]float64, len(detectors))
	for _, d := range detectors {
		if w := base.Weights[d]; w != 0 {
			out[d] = r.Contributions[d] / w
		}
	}
	return out
}

// rescore applies a candidate weighting to a set of rows.
func rescore(rows metrics.Rows, w map[string]float64) metrics.Rows {
	out := make(metrics.Rows, len(rows))
	copy(out, rows)
	for i := range out {
		s := detectorScores(out[i])
		var sum float64
		for d, v := range s {
			sum += w[d] * v
		}
		out[i].Score = sigmoid(sum - 3.0)
	}
	return out
}

func sigmoid(x float64) float64 {
	if x > 30 {
		return 1
	}
	if x < -30 {
		return 0
	}
	return 1 / (1 + math.Exp(-x))
}

// topN returns the highest-scoring rows under the DEPLOYED scorer — the rows a
// real review queue would actually put in front of an analyst.
func topN(rows metrics.Rows, n int) metrics.Rows {
	cp := make(metrics.Rows, len(rows))
	copy(cp, rows)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Score > cp[j].Score })
	if n > len(cp) {
		n = len(cp)
	}
	return cp[:n]
}

func sampleN(rows metrics.Rows, n int, rng *rand.Rand) metrics.Rows {
	cp := make(metrics.Rows, len(rows))
	copy(cp, rows)
	for i := 0; i < n && i < len(cp); i++ {
		j := i + rng.IntN(len(cp)-i)
		cp[i], cp[j] = cp[j], cp[i]
	}
	if n > len(cp) {
		n = len(cp)
	}
	return cp[:n]
}

// fit grid-searches fusion weights to maximise AUC-PR on the labelled rows,
// then reports how those weights do on periods and attacks it never saw.
func fit(name string, labelled, eval, scam metrics.Rows) Regime {
	// 0 is in the grid so the search is ABLE to discover "ignore this detector
	// entirely" — without it, a weighting that isolates the one detector that
	// generalises is not even reachable, and the experiment would be rigged.
	grid := []float64{0, 0.5, 1.0, 2.0, 3.0, 4.0, 5.0}
	taus := metrics.DefaultTaus()

	best := map[string]float64{}
	bestScore := -1.0

	for _, wv := range grid {
		for _, wf := range grid {
			for _, wc := range grid {
				w := map[string]float64{"velocity": wv, "fanout": wf, "cycle": wc}
				got := metrics.AUCPR(metrics.Sweep(rescore(labelled, w), taus))
				if got > bestScore {
					bestScore, best = got, w
				}
			}
		}
	}

	var fraud int
	for _, r := range labelled {
		if r.Fraudulent() {
			fraud++
		}
	}

	ev := rescore(eval, best)
	sc := rescore(scam, best)
	evAUC := metrics.AUCPR(metrics.Sweep(ev, taus))
	scAUC := metrics.AUCPR(metrics.Sweep(sc, taus))

	reg := Regime{
		Name: name, Labelled: len(labelled), Fraud: fraud,
		Weights: best, EvalAUCPR: evAUC, ScamAUCPR: scAUC,
	}
	if p := ev.Prevalence(); p > 0 {
		reg.EvalLift = evAUC / p
	}
	if p := sc.Prevalence(); p > 0 {
		reg.ScamLift = scAUC / p
	}
	return reg
}

func report(rs []Regime, eval, scam metrics.Rows, budget float64, n, train int) string {
	var b strings.Builder
	taus := metrics.DefaultTaus()
	baseW := risk.DefaultFusion().Weights

	fmt.Fprintf(&b, "═══ what the review queue teaches, and what it cannot ═══════\n\n")
	fmt.Fprintf(&b, "training period %d rows · review budget %.0f%% = %d labels\n",
		train, budget*100, n)
	fmt.Fprintf(&b, "evaluation period %d rows, prevalence %.3f%%\n",
		len(eval), eval.Prevalence()*100)
	fmt.Fprintf(&b, "untuned attack   %d rows, prevalence %.3f%%\n\n",
		len(scam), scam.Prevalence()*100)

	fmt.Fprintf(&b, "%-22s %8s %7s %10s %9s %10s %9s\n",
		"labelling regime", "labels", "fraud", "ring AUC", "lift", "scam AUC", "lift")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("─", 82))

	deployed := metrics.AUCPR(metrics.Sweep(rescore(eval, baseW), taus))
	deployedScam := metrics.AUCPR(metrics.Sweep(rescore(scam, baseW), taus))
	dl, dsl := 0.0, 0.0
	if p := eval.Prevalence(); p > 0 {
		dl = deployed / p
	}
	if p := scam.Prevalence(); p > 0 {
		dsl = deployedScam / p
	}
	fmt.Fprintf(&b, "%-22s %8s %7s %10.4f %8.1fx %10.4f %8.1fx\n",
		"deployed (no refit)", "—", "—", deployed, dl, deployedScam, dsl)

	for _, r := range rs {
		fmt.Fprintf(&b, "%-22s %8d %7d %10.4f %8.1fx %10.4f %8.1fx\n",
			r.Name, r.Labelled, r.Fraud, r.EvalAUCPR, r.EvalLift, r.ScamAUCPR, r.ScamLift)
	}

	fmt.Fprintf(&b, "\nrefit weights\n")
	fmt.Fprintf(&b, "  %-22s velocity %.1f  fanout %.1f  cycle %.1f\n",
		"deployed", baseW["velocity"], baseW["fanout"], baseW["cycle"])
	for _, r := range rs {
		fmt.Fprintf(&b, "  %-22s velocity %.1f  fanout %.1f  cycle %.1f\n",
			r.Name, r.Weights["velocity"], r.Weights["fanout"], r.Weights["cycle"])
	}

	if len(rs) >= 2 {
		q, a := rs[0], rs[1]
		fmt.Fprintf(&b, "\n─── the comparison that matters ────────────────────────────\n")
		fmt.Fprintf(&b, "Same %d labels, same analyst cost, different selection:\n", n)
		fmt.Fprintf(&b, "  queue review  ring %.1fx   scam %.1fx\n", q.EvalLift, q.ScamLift)
		fmt.Fprintf(&b, "  random audit  ring %.1fx   scam %.1fx\n", a.EvalLift, a.ScamLift)
		fmt.Fprintf(&b, "\nThe queue only ever labels what the detector already ranked\n")
		fmt.Fprintf(&b, "highly, so it can confirm existing beliefs but cannot discover\n")
		fmt.Fprintf(&b, "anything scored below the review line. Fraud the detector is\n")
		fmt.Fprintf(&b, "blind to stays unlabelled forever, and retraining on that queue\n")
		fmt.Fprintf(&b, "cannot fix a blind spot it structurally cannot see.\n")
	}
	return b.String()
}
