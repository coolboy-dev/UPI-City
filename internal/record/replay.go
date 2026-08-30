package record

import (
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// LabelRow is one transaction's ground truth, written to a SEPARATE file from
// the events.
//
// ─── Why a separate file ────────────────────────────────────────────────────
//
// events.jsonl is the detector's view of the world and must stay exactly that:
// no labels, no archetypes, no incident ids. Putting truth in the same file
// would make it possible — eventually likely — for something reading "the
// recording" to consume labels by accident.
//
// So truth lives alongside, in a file only the scoreboard and the metrics
// layer ever open. A replay feeds events.jsonl to the detectors and
// labels.jsonl to the scoreboard, which is the same separation the live path
// enforces through package boundaries.
type LabelRow struct {
	TxID      obs.TxID         `json:"tx"`
	Label     truth.Label      `json:"l"`
	Incident  truth.IncidentID `json:"inc,omitempty"`
	Archetype truth.Archetype  `json:"ar"`
	Intensity float64          `json:"int,omitempty"`
}

// NodeRow is one agent's frozen display position.
type NodeRow struct {
	ID   obs.AgentID `json:"i"`
	X    float32     `json:"x"`
	Y    float32     `json:"y"`
	Kind string      `json:"k"`
	Bank uint8       `json:"b"`
}

// WorldFile is everything a replay needs that is not an event: the layout,
// the banks, and the incidents that were injected.
type WorldFile struct {
	Seed      uint64           `json:"seed"`
	Agents    int              `json:"agents"`
	Ticks     uint64           `json:"ticks"`
	MsPerTick uint64           `json:"ms_per_tick"`
	Scenario  string           `json:"scenario"`
	Banks     []string         `json:"banks"`
	Nodes     []NodeRow        `json:"nodes"`
	Incidents []truth.Incident `json:"incidents"`
}
