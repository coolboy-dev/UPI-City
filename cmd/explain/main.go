// Command explain generates incident narratives for the audit trail.
//
// Run ahead of a demo to fill the cache: during the demo itself the model is
// never invoked, so a slow or absent model server cannot break anything.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/explain"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/sim"
)

func indent(s string) string {
	out := ""
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out += "     " + l + "\n"
	}
	return out
}

func main() {
	scfg := sim.DefaultConfig()
	ccfg := chaos.DefaultConfig()

	seeds := flag.String("seeds", "1,2,3,42", "seeds to generate explanations for")
	ticks := flag.Uint64("ticks", 40000, "ticks per run")
	scenario := flag.String("scenario", "fraud-ring", "chaos scenario")
	model := flag.String("model", "qwen3:1.7b", "ollama model")
	endpoint := flag.String("endpoint", "http://localhost:11434", "ollama endpoint")
	out := flag.String("out", "results/explanations.json", "cache file to write")
	timeout := flag.Duration("timeout", 60*time.Second, "per-incident generation timeout")
	templateOnly := flag.Bool("template-only", false, "skip the model entirely")
	verbose := flag.Bool("verbose", false, "print rejected model output and the facts it was given")
	flag.Parse()

	scfg.Ticks = obs.Tick(*ticks)

	oll := explain.NewOllama(*endpoint, *model)
	useModel := !*templateOnly
	if useModel {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ok := oll.Available(ctx)
		cancel()
		if !ok {
			names, _ := oll.Models(context.Background())
			fmt.Fprintf(os.Stderr,
				"model %q unavailable at %s (found: %s)\n"+
					"falling back to templates — the audit trail is still complete, only less fluent\n\n",
				*model, *endpoint, strings.Join(names, ", "))
			useModel = false
		}
	}

	cache := explain.LoadCache(*out)
	fmt.Printf("cache loaded with %d existing entries\n", cache.Len())

	var generated, cached, templated int

	for _, s := range strings.Split(*seeds, ",") {
		var seed uint64
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &seed); err != nil {
			continue
		}
		cfg := scfg
		cfg.Seed = seed

		res, err := runner.Run(runner.Options{
			Sim: cfg, Chaos: ccfg, Scenario: *scenario,
			Detectors: []string{"velocity", "fanout", "cycle"},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "run:", err)
			os.Exit(1)
		}
		rep := metrics.Evaluate(res.Rows, res.Incidents, seed, cfg.MsPerTick)

		for _, inc := range res.Incidents {
			var lat metrics.IncidentLatency
			for _, il := range rep.Latency.Incidents {
				if il.IncidentID == inc.ID {
					lat = il
				}
			}
			facts := explain.FromIncident(inc, res.Rows, lat,
				rep.Deployed.FraudBlocked, rep.Deployed.Reviewed, cfg.MsPerTick)

			if _, hit := cache.Get(facts); hit {
				cached++
				fmt.Printf("seed %-3d incident %d  cache hit\n", seed, inc.ID)
				continue
			}

			if !useModel {
				cache.Put(facts, explain.Template(facts))
				templated++
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), *timeout)
			start := time.Now()
			e, err := oll.Explain(ctx, facts)
			cancel()

			switch {
			case err != nil:
				fmt.Printf("seed %-3d incident %d  model failed (%v) — template\n", seed, inc.ID, err)
				cache.Put(facts, explain.Template(facts))
				templated++
			case !explain.Plausible(e, facts):
				// The guard fired: the model invented a figure. Discard it.
				fmt.Printf("seed %-3d incident %d  REJECTED: invented a number not in the facts — template\n",
					seed, inc.ID)
				if *verbose {
					fmt.Printf("   facts given:\n%s", indent(explain.Describe(facts)))
					fmt.Printf("   model wrote: %s | %s | %s\n", e.Headline, e.Narrative, e.Action)
				}
				cache.Put(facts, explain.Template(facts))
				templated++
			default:
				e.Elapsed = time.Since(start).Round(time.Millisecond).String()
				cache.Put(facts, e)
				generated++
				fmt.Printf("seed %-3d incident %d  generated in %s\n", seed, inc.ID, e.Elapsed)
				fmt.Printf("   %s\n", e.Headline)
			}
		}
	}

	if err := cache.Save(*out); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
		os.Exit(1)
	}
	fmt.Printf("\n%d generated, %d already cached, %d fell back to template\n",
		generated, cached, templated)
	fmt.Printf("wrote %s (%d entries)\n", *out, cache.Len())
}
