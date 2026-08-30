// Command adversary searches the attacker's parameter space for the settings
// that extract the most money past the detector.
//
// ─── Why this exists ────────────────────────────────────────────────────────
//
// Every other number in this project grades the detector against a ring whose
// parameters the DEFENCE chose. That is a straw opponent, and worse, those
// parameters were tuned during development until the detectors could see the
// attack — which is uncomfortably close to fitting the attack to the defence.
//
// Real launderers watch what gets stopped and move. So the honest figure is
// detection quality at the attacker's BEST RESPONSE, not at whatever settings
// happened to be committed. This searches for that best response and reports
// how far performance falls when it is found.
//
// The attacker's objective is deliberately MONEY, not evasion. A ring that
// evades perfectly by never moving anything has achieved nothing; the thing to
// maximise is value successfully extracted, which forces the real trade-off
// between moving quietly and moving at all.
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"sort"
	"sync"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/decide"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/sim"
)

// candidate is one attacker configuration and how it fared.
type candidate struct {
	MuleRate     float64 `json:"mule_rate"`
	AmountRupees float64 `json:"mule_amount_rupees"`
	TakeoverRate float64 `json:"takeover_rate"`
	RingSize     int     `json:"ring_size"`
	Hops         int     `json:"hops"`
	RampTicks    uint64  `json:"ramp_ticks"`

	// ExtractedRupees is the attacker's objective: fraud value that was
	// neither blocked nor caught in review.
	ExtractedRupees int64   `json:"extracted_rupees"`
	AttemptedRupees int64   `json:"attempted_rupees"`
	EvasionRate     float64 `json:"evasion_rate"`

	// What it cost the defender.
	AUCPR       float64 `json:"aucpr"`
	Recall      float64 `json:"recall"`
	NetPerCrore float64 `json:"defender_net_per_crore"`
}

func main() {
	scfg := sim.DefaultConfig()

	trials := flag.Int("trials", 60, "attacker configurations to try")
	seeds := flag.Int("seeds", 3, "seeds per configuration (averaged)")
	ticks := flag.Uint64("ticks", 40000, "ticks per run")
	searchSeed := flag.Uint64("search-seed", 7, "seed for the attacker's own search")
	out := flag.String("out", "results", "output directory")
	flag.Parse()

	scfg.Ticks = obs.Tick(*ticks)

	base := chaos.DefaultConfig()
	fmt.Printf("attacker searching %d configurations x %d seeds\n", *trials, *seeds)
	fmt.Printf("objective: maximise rupees extracted past the detector\n")
	fmt.Printf("defender is FIXED throughout — thresholds chosen against the baseline attack\n\n")

	// The defender's policy is fixed up front, from the baseline attack, and
	// never re-tuned. Letting it adapt per candidate would measure a different
	// thing — an arms race rather than the cost of a static defence.
	policy := baselinePolicy(scfg, base)
	fmt.Printf("fixed policy: review>=%.2f, block>=%.2f\n\n", policy.TauReview, policy.TauBlock)

	rng := rand.New(rand.NewPCG(*searchSeed, *searchSeed*0x9E3779B97F4A7C15|1))
	cands := make([]candidate, *trials)

	// The baseline is trial 0, so the table always contains the attack the
	// detector was actually designed against.
	cands[0] = candidate{
		MuleRate: base.MuleRate, AmountRupees: base.MuleAmountRupees,
		TakeoverRate: base.TakeoverRate, RingSize: base.RingSize,
		Hops: base.Hops, RampTicks: uint64(base.RampTicks),
	}
	for i := 1; i < *trials; i++ {
		cands[i] = candidate{
			// Quieter and louder than the baseline in every direction, so the
			// search can discover that either extreme helps.
			MuleRate:     0.01 + rng.Float64()*0.24,
			AmountRupees: 200 + rng.Float64()*6000,
			TakeoverRate: 0.01 + rng.Float64()*0.20,
			RingSize:     3 + rng.IntN(30),
			Hops:         3 + rng.IntN(3),
			RampTicks:    uint64(200 + rng.IntN(6000)),
		}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i := range cands {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cands[i] = evaluate(cands[i], scfg, base, policy, *seeds)
		}(i)
	}
	wg.Wait()

	baseline := cands[0]
	sort.Slice(cands, func(a, b int) bool {
		return cands[a].ExtractedRupees > cands[b].ExtractedRupees
	})
	best := cands[0]

	fmt.Print(table(cands, baseline))
	fmt.Print(verdict(baseline, best))

	if *out != "" {
		top := cands
		if len(top) > 15 {
			top = top[:15]
		}
		if err := record.WriteJSON(*out, "adversary.json", map[string]any{
			"policy":   policy,
			"baseline": baseline,
			"best":     best,
			"top":      top,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Printf("\nwrote %s/adversary.json\n", *out)
	}
}

// baselinePolicy picks the defender's thresholds against the default attack,
// once. This is the policy a team would have shipped.
func baselinePolicy(scfg sim.Config, ccfg chaos.Config) decide.Policy {
	cfg := scfg
	cfg.Seed = 1
	res, err := runner.Run(runner.Options{
		Sim: cfg, Chaos: ccfg, Scenario: "fraud-ring",
		Detectors: []string{"velocity", "fanout", "cycle"},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return metrics.DefaultCostModel().BestByNet(res.Rows, metrics.DefaultTaus()).Policy
}

func evaluate(c candidate, scfg sim.Config, base chaos.Config, p decide.Policy, seeds int) candidate {
	cost := metrics.DefaultCostModel()

	var extracted, attempted int64
	var auc, recall, net float64

	for s := 1; s <= seeds; s++ {
		cfg := scfg
		cfg.Seed = uint64(s)

		cc := base
		cc.MuleRate = c.MuleRate
		cc.MuleAmountRupees = c.AmountRupees
		cc.TakeoverRate = c.TakeoverRate
		cc.RingSize = c.RingSize
		cc.Hops = c.Hops
		cc.RampTicks = obs.Tick(c.RampTicks)

		res, err := runner.Run(runner.Options{
			Sim: cfg, Chaos: cc, Scenario: "fraud-ring",
			Detectors: []string{"velocity", "fanout", "cycle"},
		})
		if err != nil {
			continue
		}

		e := cost.Evaluate(res.Rows, p)
		// Extracted = fraud value that got through, plus the share of
		// reviewed fraud a human missed.
		extracted += e.FraudAllowedRupees +
			int64((1-cost.ReviewCatchRate)*float64(e.FraudReviewedRupees))
		attempted += e.FraudRupees
		net += e.NetPerCrore

		rep := metrics.Evaluate(res.Rows, res.Incidents, cfg.Seed, cfg.MsPerTick)
		auc += rep.AUCPR
		recall += rep.AtBudget1pct.Recall
	}

	n := float64(seeds)
	c.ExtractedRupees = extracted / int64(seeds)
	c.AttemptedRupees = attempted / int64(seeds)
	if c.AttemptedRupees > 0 {
		c.EvasionRate = float64(c.ExtractedRupees) / float64(c.AttemptedRupees)
	}
	c.AUCPR, c.Recall, c.NetPerCrore = auc/n, recall/n, net/n
	return c
}

func table(cands []candidate, baseline candidate) string {
	var b []byte
	w := func(f string, a ...any) { b = append(b, fmt.Sprintf(f, a...)...) }

	w("%-9s %-9s %-9s %-6s %-6s %12s %8s %8s %12s\n",
		"muleRate", "amount₹", "takeover", "ring", "hops",
		"extracted₹", "evasion", "AUC-PR", "net/crore₹")
	w("%s\n", dashes(96))

	show := cands
	if len(show) > 8 {
		show = show[:8]
	}
	for _, c := range show {
		w("%-9.3f %-9.0f %-9.3f %-6d %-6d %12s %7.1f%% %8.3f %12.0f\n",
			c.MuleRate, c.AmountRupees, c.TakeoverRate, c.RingSize, c.Hops,
			lakh(c.ExtractedRupees), c.EvasionRate*100, c.AUCPR, c.NetPerCrore)
	}
	w("%s\n", dashes(96))
	w("%-9.3f %-9.0f %-9.3f %-6d %-6d %12s %7.1f%% %8.3f %12.0f   ← baseline\n",
		baseline.MuleRate, baseline.AmountRupees, baseline.TakeoverRate,
		baseline.RingSize, baseline.Hops, lakh(baseline.ExtractedRupees),
		baseline.EvasionRate*100, baseline.AUCPR, baseline.NetPerCrore)
	return string(b)
}

func verdict(baseline, best candidate) string {
	var b []byte
	w := func(f string, a ...any) { b = append(b, fmt.Sprintf(f, a...)...) }

	w("\n─── verdict ─────────────────────────────────────────────────\n")
	w("baseline attack       %s extracted, %.1f%% evasion, AUC-PR %.3f\n",
		lakh(baseline.ExtractedRupees), baseline.EvasionRate*100, baseline.AUCPR)
	w("attacker best response %s extracted, %.1f%% evasion, AUC-PR %.3f\n",
		lakh(best.ExtractedRupees), best.EvasionRate*100, best.AUCPR)

	mult := 0.0
	if baseline.ExtractedRupees > 0 {
		mult = float64(best.ExtractedRupees) / float64(baseline.ExtractedRupees)
	}
	w("\nAn adversary that tunes its own parameters extracts %.1fx what the\n", mult)
	w("baseline attack does against the SAME fixed policy.\n\n")

	switch {
	case mult > 1.5:
		w("This is the number that should be quoted, not the baseline. Detection\n")
		w("measured against an attack the defence chose is measuring the wrong\n")
		w("thing; an opponent who adapts is the operating condition.\n")
	case mult > 1.1:
		w("The defence degrades under adaptation but does not collapse. Worth\n")
		w("reporting alongside the baseline rather than instead of it.\n")
	default:
		w("The baseline attack was already close to optimal for this attacker,\n")
		w("so the headline figures are not flattered by the choice of parameters.\n")
	}
	return string(b)
}

func lakh(v int64) string {
	switch {
	case v >= 10_000_000:
		return fmt.Sprintf("%.2f cr", float64(v)/1e7)
	case v >= 100_000:
		return fmt.Sprintf("%.2f L", float64(v)/1e5)
	}
	return fmt.Sprintf("%d", v)
}

func dashes(n int) string {
	s := make([]byte, n)
	for i := range s {
		s[i] = '-'
	}
	return string(s)
}
