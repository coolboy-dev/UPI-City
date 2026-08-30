// Package record persists a run to disk as JSONL.
//
// The recording is what makes replay and offline threshold sweeps possible:
// the simulation is run once, and every later question — what would precision
// have been at a different threshold, what did this incident actually look
// like — is answered from the file rather than by re-simulating.
package record

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/yug/upi-city/internal/obs"
)

// Writer streams records to a JSONL file.
type Writer struct {
	f  *os.File
	bw *bufio.Writer
	en *json.Encoder
	n  int
}

// Create opens dir/name for writing, creating dir if needed.
func Create(dir, name string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	return &Writer{f: f, bw: bw, en: json.NewEncoder(bw)}, nil
}

// Write appends one record.
func (w *Writer) Write(v any) error {
	w.n++
	return w.en.Encode(v)
}

// Count returns how many records have been written.
func (w *Writer) Count() int { return w.n }

// Close flushes and closes the file.
func (w *Writer) Close() error {
	if err := w.bw.Flush(); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

// EventRow is the on-disk form of an observable event.
//
// Deliberately mirrors obs.Event exactly and adds nothing: the recording is
// the detector's view of the world, so anything extra written here would be a
// ground-truth leak with a longer fuse.
type EventRow struct {
	T   obs.Tick    `json:"t"`
	S   obs.Tick    `json:"s"`
	ID  obs.TxID    `json:"id"`
	Frm obs.AgentID `json:"f"`
	To  obs.AgentID `json:"to"`
	Bnk obs.BankID  `json:"b"`
	Amt int64       `json:"a"`
	St  uint8       `json:"st"`
	Lat uint16      `json:"l"`
	Dev uint32      `json:"d"`
	New bool        `json:"n"`
}

// RowOf converts an event to its on-disk form.
func RowOf(e obs.Event) EventRow {
	return EventRow{
		T: e.Tick, S: e.SettleTick, ID: e.TxID,
		Frm: e.From, To: e.To, Bnk: e.Bank, Amt: e.AmountP,
		St: uint8(e.Status), Lat: e.LatencyMs, Dev: e.DeviceID, New: e.IsNewFrom,
	}
}

// WriteJSON writes a single value as pretty JSON, for metadata files.
func WriteJSON(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(dir, name), b, 0o644)
}
