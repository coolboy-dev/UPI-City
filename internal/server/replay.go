package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

	cursor int
	tick   obs.Tick
}

// LoadReplay reads a recorded run from a directory.
func LoadReplay(dir string) (Source, error) {
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
	r.tick = r.events[0].SettleTick
	return r, nil
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
