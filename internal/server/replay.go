package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/truth"
)

// replaySource plays a recorded run back through the identical pipeline.
//
// ─── What this is for ───────────────────────────────────────────────────────
//
//  1. Demo insurance. If the live simulation misbehaves on stage, a replay is
//     one flag away and is visually indistinguishable — better than a recorded
//     video, because it is still interactive.
//  2. Working on the interface without waiting for a live world to reach an
//     interesting state.
//  3. Feeding the SAME traffic to a changed detector. The engine is
//     deterministic, so re-running a seed would also work — but only while the
//     engine itself is unchanged. A recording survives engine changes, which
//     is exactly when you most want to know whether a detector got better or
//     the world just moved.
//
// Events and labels come from separate files, and this type is careful to
// keep them that way: the detectors are handed obs.Event and nothing else.
type replaySource struct {
	events []obs.Event
	labels map[obs.TxID]record.LabelRow
	world  record.WorldFile

	// rival holds a competing detector's scores, read from a scores-*.jsonl
	// written by a program outside this repository. Empty when the recording
	// has no challenger.
	rival     map[obs.TxID]float64
	rivalName string
	// rivalSorted is every rival score in descending order, so the threshold
	// that flags a given share of traffic is one index lookup. Computed once
	// at load: the recording's future is already written, and a quantile over
	// it is not a peek at anything the challenger did not already commit to.
	rivalSorted []float64

	cursor int
	tick   obs.Tick
}

// LoadReplay reads a recorded run from a directory, with no challenger.
//
// This is the demo-insurance path and it deliberately ignores any scores-*.jsonl
// sitting beside the recording. Loading one automatically would mean a replay
// changed appearance the moment somebody graded an entrant against it, and a
// replay that is not visually identical to a live run is useless as a fallback.
func LoadReplay(dir string) (Source, error) {
	return LoadReplayWithRival(dir, "")
}

// LoadReplayWithRival reads a recording alongside a competing detector's
// scores, for the head-to-head view.
//
// rival names the challenger. Empty loads none; "auto" takes the first found,
// which is a convenience for a directory with exactly one entrant in it.
func LoadReplayWithRival(dir, rival string) (Source, error) {
	r := &replaySource{labels: map[obs.TxID]record.LabelRow{}}

	b, err := os.ReadFile(filepath.Join(dir, "world.json"))
	if err != nil {
		return nil, fmt.Errorf("replay: %w (record one with `just record`)", err)
	}
	if err := json.Unmarshal(b, &r.world); err != nil {
		return nil, fmt.Errorf("replay: world.json: %w", err)
	}

	if err := readLines(filepath.Join(dir, "events.jsonl"), func(line []byte) error {
		var row record.EventRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		r.events = append(r.events, obs.Event{
			Tick: row.T, SettleTick: row.S, TxID: row.ID,
			From: row.Frm, To: row.To, Bank: row.Bnk, AmountP: row.Amt,
			Status: obs.Status(row.St), LatencyMs: row.Lat,
			DeviceID: row.Dev, IsNewFrom: row.New,
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("replay: events.jsonl: %w", err)
	}

	// Labels are optional: without them the replay still renders, it just
	// cannot score itself. That is a legitimate mode — a recording shared
	// without its answer key.
	_ = readLines(filepath.Join(dir, "labels.jsonl"), func(line []byte) error {
		var row record.LabelRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		r.labels[row.TxID] = row
		return nil
	})

	if len(r.events) == 0 {
		return nil, errors.New("replay: recording contains no events")
	}

	// The challenger's scores load exactly the way labels do — an optional
	// side file keyed by transaction id. That symmetry is the point: to this
	// package a rival detector's opinion and the ground truth are both things
	// that arrive from disk after the fact and are attached to a transaction
	// for display, never fed back into detection.
	if err := r.loadRival(dir, rival); err != nil {
		return nil, err
	}

	r.tick = r.events[0].SettleTick
	return r, nil
}

// loadRival reads scores-NAME.jsonl, or the first such file when name is empty.
func (r *replaySource) loadRival(dir, name string) error {
	if name == "" {
		return nil // no challenger asked for; the head-to-head view stays hidden
	}
	pattern := "scores-*.jsonl"
	if name != "auto" {
		pattern = "scores-" + name + ".jsonl"
	}
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("replay: no scores for rival %q in %s — "+
			"run `just entrants` first, or see detectors/README.md", name, dir)
	}
	sort.Strings(matches)
	path := matches[0]

	r.rival = make(map[obs.TxID]float64, len(r.events))
	if err := readLines(path, func(line []byte) error {
		var row struct {
			TxID  obs.TxID `json:"tx"`
			Score float64  `json:"s"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		r.rival[row.TxID] = row.Score
		return nil
	}); err != nil {
		return fmt.Errorf("replay: %s: %w", path, err)
	}
	r.rivalName = strings.TrimSuffix(
		strings.TrimPrefix(filepath.Base(path), "scores-"), ".jsonl")

	r.rivalSorted = make([]float64, 0, len(r.rival))
	for _, s := range r.rival {
		r.rivalSorted = append(r.rivalSorted, s)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(r.rivalSorted)))
	return nil
}

func (r *replaySource) Rival(id obs.TxID) float64 { return r.rival[id] }
func (r *replaySource) RivalName() string         { return r.rivalName }

func (r *replaySource) RivalTauForRate(rate float64) float64 {
	n := len(r.rivalSorted)
	if n == 0 || rate <= 0 {
		// Above every possible score, so the challenger flags nothing. This is
		// the honest reading of a zero budget, and it is what the first frames
		// of a run report before either detector has flagged anything.
		return 2
	}
	if rate >= 1 {
		return 0
	}
	k := int(float64(n) * rate)
	if k >= n {
		k = n - 1
	}
	// A score of zero means the challenger declined to speak. Handing it
	// budget it never asked for would credit it with an arbitrary slice of the
	// traffic it ignored, so the cut never descends past the last real score.
	if v := r.rivalSorted[k]; v > 0 {
		return v
	}
	return 1e-9
}

func readLines(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Step returns every event that settled on the current tick, then advances.
func (r *replaySource) Step() []obs.Event {
	if r.cursor >= len(r.events) {
		r.tick++
		return nil
	}
	var out []obs.Event
	for r.cursor < len(r.events) && r.events[r.cursor].SettleTick <= r.tick {
		out = append(out, r.events[r.cursor])
		r.cursor++
	}
	r.tick++
	return out
}

func (r *replaySource) Now() obs.Tick { return r.tick }
func (r *replaySource) Done() bool    { return r.cursor >= len(r.events) }
func (r *replaySource) Live() bool    { return false }

func (r *replaySource) Label(id obs.TxID) truth.Label {
	return r.labels[id].Label
}

func (r *replaySource) IncidentOf(id obs.TxID) truth.IncidentID {
	return r.labels[id].Incident
}

func (r *replaySource) Incidents() []truth.Incident {
	// Only incidents that have actually begun, so the feed fills in as the
	// replay reaches them rather than showing the whole run's future at once.
	var out []truth.Incident
	for _, in := range r.world.Incidents {
		if in.StartTick <= r.tick {
			out = append(out, in)
		}
	}
	return out
}

func (r *replaySource) Nodes() []Node {
	out := make([]Node, 0, len(r.world.Nodes))
	for _, n := range r.world.Nodes {
		out = append(out, Node{
			ID: n.ID, X: n.X, Y: n.Y, R: radiusForName(n.Kind),
			Kind: n.Kind, Bank: n.Bank,
		})
	}
	return out
}

// Inject is refused during a replay.
//
// A recording's future is already written. Accepting an injection would
// desynchronise what the interface claims is happening from what the data
// actually contains, which is worse than simply saying no.
func (r *replaySource) Inject(string, chaos.Config) error {
	return errors.New("cannot inject chaos into a replay: the recording's future is already fixed")
}

func radiusForName(kind string) float32 {
	switch kind {
	case "mega_merchant":
		return 7
	case "payroll":
		return 5.5
	case "supply_pair":
		return 4
	}
	return 2.2
}
