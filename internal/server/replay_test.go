package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/sim"
)

// recordRun produces a small recording to replay.
func recordRun(t *testing.T) (string, runner.Result) {
	t.Helper()
	dir := t.TempDir()

	cfg := sim.DefaultConfig()
	cfg.Ticks = 8000
	ccfg := chaos.DefaultConfig()

	res, err := runner.Run(runner.Options{
		Sim: cfg, Chaos: ccfg, Scenario: "fraud-ring",
		Detectors: []string{"velocity", "fanout", "cycle"},
		RecordDir: dir,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return dir, res
}

// TestReplayEmitsEveryRecordedEvent is the fidelity requirement.
//
// A replay that silently drops or duplicates events would produce different
// detector state and therefore different numbers, which defeats the whole
// purpose: the point of a recording is that it is the same traffic.
func TestReplayEmitsEveryRecordedEvent(t *testing.T) {
	dir, res := recordRun(t)

	src, err := LoadReplay(dir)
	if err != nil {
		t.Fatalf("LoadReplay: %v", err)
	}

	seen := map[obs.TxID]int{}
	var total int
	for !src.Done() {
		for _, e := range src.Step() {
			seen[e.TxID]++
			total++
		}
	}

	if total != res.Events {
		t.Errorf("replayed %d events, recorded %d", total, res.Events)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("transaction %d replayed %d times", id, n)
		}
	}
}

// TestReplayPreservesSettlementOrder checks events arrive in the order a
// detector would have seen them live.
//
// Every detector here is stateful — sliding windows, per-agent baselines,
// recent-edge rings — so reordering the stream changes the scores even when
// the set of events is identical.
func TestReplayPreservesSettlementOrder(t *testing.T) {
	dir, _ := recordRun(t)
	src, err := LoadReplay(dir)
	if err != nil {
		t.Fatalf("LoadReplay: %v", err)
	}

	var last obs.Tick
	for !src.Done() {
		for _, e := range src.Step() {
			if e.SettleTick < last {
				t.Fatalf("event settled at tick %d after one at %d", e.SettleTick, last)
			}
			last = e.SettleTick
		}
	}
}

// TestRecordedEventsCarryNoGroundTruth is the firewall, applied to what gets
// written to disk.
//
// The dependency test guarantees the detection PACKAGE cannot see labels. This
// guarantees the recording cannot either: a file that mixed truth into the
// event stream would reopen the leak through the replay path, where the
// package boundary offers no protection at all.
func TestRecordedEventsCarryNoGroundTruth(t *testing.T) {
	dir, _ := recordRun(t)

	b, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	first := strings.SplitN(string(b), "\n", 2)[0]

	var row map[string]any
	if err := json.Unmarshal([]byte(first), &row); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Whatever the field names are, the recorded event must decode cleanly
	// into the observable type and nothing else.
	var known record.EventRow
	if err := json.Unmarshal([]byte(first), &known); err != nil {
		t.Fatalf("event row does not match the observable schema: %v", err)
	}
	allowed := map[string]bool{
		"t": true, "s": true, "id": true, "f": true, "to": true,
		"b": true, "a": true, "st": true, "l": true, "d": true, "n": true,
	}
	for k := range row {
		if !allowed[k] {
			t.Errorf("recorded event carries unexpected field %q — if this is ground truth, "+
				"the replay path leaks what the package boundary forbids", k)
		}
	}

	// And truth must exist, separately.
	if _, err := os.Stat(filepath.Join(dir, "labels.jsonl")); err != nil {
		t.Errorf("no labels.jsonl: a replay cannot score itself without one")
	}
}

// TestReplayRefusesInjection pins the honest behaviour.
//
// A recording's future is already written. Accepting an injection would make
// the interface claim something the data does not contain, which is a worse
// failure than a clear refusal.
func TestReplayRefusesInjection(t *testing.T) {
	dir, _ := recordRun(t)
	src, err := LoadReplay(dir)
	if err != nil {
		t.Fatalf("LoadReplay: %v", err)
	}
	if src.Live() {
		t.Error("a replay reported itself as live")
	}
	if err := src.Inject("fraud-ring", chaos.DefaultConfig()); err == nil {
		t.Error("replay accepted a chaos injection instead of refusing it")
	}
}

// TestReplayCarriesTheLayout checks the picture survives the round trip, so a
// replay looks identical rather than merely equivalent.
func TestReplayCarriesTheLayout(t *testing.T) {
	dir, _ := recordRun(t)
	src, err := LoadReplay(dir)
	if err != nil {
		t.Fatalf("LoadReplay: %v", err)
	}

	nodes := src.Nodes()
	if len(nodes) == 0 {
		t.Fatal("replay has no layout")
	}
	var merchants int
	for _, n := range nodes {
		if n.X == 0 && n.Y == 0 {
			t.Fatalf("agent %d has no position", n.ID)
		}
		if n.Kind == "mega_merchant" {
			merchants++
		}
	}
	if merchants == 0 {
		t.Error("layout lost the archetypes; every node would render identically")
	}
}
