// Command upicity runs the simulation live and serves the dashboard.
//
// A single binary with the interface embedded: no dev server, no second
// toolchain, no node_modules. Start it, open the page, watch the network.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/explain"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/server"
	"github.com/yug/upi-city/internal/sim"
)

func main() {
	scfg := sim.DefaultConfig()
	ccfg := chaos.DefaultConfig()

	addr := flag.String("addr", ":8080", "listen address")
	agents := flag.Int("agents", scfg.NumAgents, "number of agents")
	seed := flag.Uint64("seed", scfg.Seed, "master seed")
	tps := flag.Float64("tps", 60, "simulated ticks per real second")
	fps := flag.Float64("fps", 20, "frames streamed per second")
	detectors := flag.String("detectors", "velocity,fanout,cycle", "detector set")
	modelName := flag.String("model", "qwen3.5:9b", "ollama model for incident narratives")
	endpoint := flag.String("endpoint", "http://localhost:11434", "ollama endpoint")
	cachePath := flag.String("explanations", "results/explanations.json", "pre-generated narrative cache")
	noLLM := flag.Bool("no-llm", false, "templates only; never contact a model")
	replay := flag.String("replay", "", "play back a recorded run instead of simulating (e.g. results/seed-42)")
	flag.Parse()

	scfg.NumAgents = *agents
	scfg.Seed = *seed
	scfg.Ticks = ^obs.Tick(0) // runs until stopped

	// Chaos is armed only on request from the interface, so the network
	// starts healthy and the audience sees normal operation first.
	ccfg.StartTick = ^obs.Tick(0)

	var s *server.Server
	if *replay != "" {
		src, err := server.LoadReplay(*replay)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		s = server.NewWithSource(src, scfg, ccfg, strings.Split(*detectors, ","))
		fmt.Printf("REPLAY of %s — identical pipeline, identical wire protocol\n", *replay)
	} else {
		s = server.New(scfg, ccfg, strings.Split(*detectors, ","))
	}

	// The narrative layer. Cache first, template always, model only if it is
	// actually there — and never on the path of a tick.
	cache := explain.LoadCache(*cachePath)
	var model explain.Explainer
	status := "templates only"
	if !*noLLM {
		oll := explain.NewOllama(*endpoint, *modelName)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ok := oll.Available(ctx)
		cancel()
		if ok {
			model = oll
			status = "model " + *modelName
		} else {
			status = "model unavailable, templates only"
		}
	}
	s.SetExplainer(explain.NewLayer(model, cache, 45*time.Second))

	go s.Run(*tps, *fps)

	fmt.Printf("UPI City — %d agents, %.0f ticks/s, streaming at %.0f fps\n", *agents, *tps, *fps)
	fmt.Printf("narratives — %s, %d cached\n", status, cache.Len())
	fmt.Printf("open http://localhost%s\n", *addr)

	if err := http.ListenAndServe(*addr, s.Handler()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
