package explain

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func sampleFacts() Facts {
	return Facts{
		IncidentID: 1, Kind: "fraud-ring", StartTick: 4001, DurationS: 600,
		Members: 24, Transactions: 4416,
		TotalRupees: 8637301, MedianRupees: 1078,
		Detected: true, DetectedAfterS: 18.8,
		Blocked: 1289, Reviewed: 4811, MissedPct: 63,
		TopSignals: []Signal{
			{Name: "velocity", Contribution: 2.1, Note: signalNote("velocity")},
			{Name: "cycle", Contribution: 1.4, Note: signalNote("cycle")},
		},
	}
}

// TestExplainCannotSeeGroundTruth keeps the firewall intact through this
// package.
//
// The explanation layer is handed pre-computed numbers by the metrics layer;
// it must not acquire its own route to the labels, or a future refactor could
// quietly let narrative generation influence anything upstream.
func TestExplainCannotSeeGroundTruth(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(p) == "github.com/yug/upi-city/internal/sim" {
			t.Error("internal/explain must not depend on internal/sim")
		}
	}
}

// TestGuardRejectsInventedNumbers is the whole point of the guard.
//
// This is an audit trail. A model that writes "roughly ₹9.2 lakh across a
// dozen accounts" is more readable and factually wrong, and a wrong figure in
// a compliance record is quotable in a way that clumsy prose never is.
func TestGuardRejectsInventedNumbers(t *testing.T) {
	f := sampleFacts()

	good := Explanation{
		Headline:  "Laundering ring across 24 accounts",
		Narrative: "24 accounts moved 8,637,301 rupees over 4416 transactions. Detected after 18.8 seconds.",
		Action:    "Review all 24 accounts.",
	}
	if !Plausible(good, f) {
		t.Error("rejected an explanation whose every number came from the facts")
	}

	for name, bad := range map[string]Explanation{
		"invented account count": {
			Headline:  "Laundering ring across 31 accounts",
			Narrative: "31 accounts moved 8,637,301 rupees.",
		},
		"invented total": {
			Headline:  "Laundering ring across 24 accounts",
			Narrative: "24 accounts moved 9,200,000 rupees.",
		},
		"invented duration": {
			Headline:  "Laundering ring across 24 accounts",
			Narrative: "24 accounts, active for 1400 seconds.",
		},
		"empty narrative": {
			Headline:  "Laundering ring",
			Narrative: "   ",
		},
	} {
		if Plausible(bad, f) {
			t.Errorf("%s: guard accepted a hallucinated figure", name)
		}
	}
}

// TestGuardAllowsHumanRounding checks the guard is not so strict that no
// model can satisfy it. "₹86.4 lakh" for 8,637,301 is how a person writes it.
func TestGuardAllowsHumanRounding(t *testing.T) {
	f := sampleFacts()
	e := Explanation{
		Headline:  "Ring of 24 accounts",
		Narrative: "About 86.4 lakh rupees moved across 4416 transactions.",
		Action:    "Investigate.",
	}
	if !Plausible(e, f) {
		t.Error("guard rejected ordinary human rounding of a figure it was given")
	}
}

// TestTemplateNeedsNoModel asserts the floor holds on its own.
//
// If the model is missing, slow or unreachable, the audit trail must still be
// complete. An explanation layer whose failure mode is a blank field would be
// worse than not having one.
func TestTemplateNeedsNoModel(t *testing.T) {
	f := sampleFacts()
	e := Template(f)

	if e.Source != SourceTemplate {
		t.Errorf("source = %q, want template", e.Source)
	}
	for name, s := range map[string]string{"headline": e.Headline, "narrative": e.Narrative, "action": e.Action} {
		if strings.TrimSpace(s) == "" {
			t.Errorf("template produced an empty %s", name)
		}
	}
	if !Plausible(e, f) {
		t.Error("the template's own output fails the number guard")
	}
	if !strings.Contains(e.Narrative, "24") || !strings.Contains(e.Narrative, "18.8") {
		t.Errorf("template dropped facts it was given: %q", e.Narrative)
	}
}

// TestUndetectedIncidentSaysSo checks the narrative does not quietly imply
// success when the detector missed the whole thing.
func TestUndetectedIncidentSaysSo(t *testing.T) {
	f := sampleFacts()
	f.Detected = false
	f.Blocked, f.Reviewed = 0, 0

	e := Template(f)
	if !strings.Contains(strings.ToLower(e.Narrative), "not flagged") {
		t.Errorf("an undetected incident must say so plainly: %q", e.Narrative)
	}
	if !strings.Contains(strings.ToLower(e.Action), "no automated action") {
		t.Errorf("action should state that nothing was done: %q", e.Action)
	}
}

// failing is a model that always errors.
type failing struct{}

func (failing) Explain(context.Context, Facts) (Explanation, error) {
	return Explanation{}, errors.New("model unavailable")
}

// slow is a model that never answers in time.
type slow struct{}

func (slow) Explain(ctx context.Context, _ Facts) (Explanation, error) {
	<-ctx.Done()
	return Explanation{}, ctx.Err()
}

// TestLayerNeverBlocks is the property that makes this safe to demo.
//
// A dead model, a slow model, or a full queue must all resolve instantly to a
// template. Nothing in this layer may ever become backpressure on the
// simulation.
func TestLayerNeverBlocks(t *testing.T) {
	for name, model := range map[string]Explainer{"failing": failing{}, "slow": slow{}} {
		l := NewLayer(model, NewCache(), 50*time.Millisecond)

		start := time.Now()
		for i := 0; i < 200; i++ {
			f := sampleFacts()
			f.IncidentID = uint32(i)
			if e := l.Immediate(f); strings.TrimSpace(e.Narrative) == "" {
				t.Fatalf("%s model: got an empty explanation", name)
			}
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s model: 200 explanations took %s — the layer is blocking on the model", name, d)
		}
	}
}

// TestCacheHitsOnIdenticalFacts is what makes a live demo instant: the same
// seed produces the same incident, the same rounded facts, the same key.
func TestCacheHitsOnIdenticalFacts(t *testing.T) {
	c := NewCache()
	f := sampleFacts()
	c.Put(f, Explanation{Headline: "cached"})

	if _, ok := c.Get(sampleFacts()); !ok {
		t.Error("identical facts missed the cache")
	}

	// A trivially different figure must still hit — rounding is the point.
	near := sampleFacts()
	near.TotalRupees += 137
	if _, ok := c.Get(near); !ok {
		t.Error("a few rupees' difference broke the cache key")
	}

	// A materially different incident must not.
	far := sampleFacts()
	far.Members = 60
	if _, ok := c.Get(far); ok {
		t.Error("a different incident collided with a cached one")
	}
}
