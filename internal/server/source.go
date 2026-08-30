package server

import (
	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// Source is where the server gets its events.
//
// Two implementations: a live world, and a recorded run played back from
// disk. They sit behind the same interface so the streaming loop, the
// detectors, the decision layer and the wire protocol are all identical —
// which is the point. A replay the interface could distinguish from a live
// run would be useless as demo insurance.
type Source interface {
	// Step advances one tick and returns whatever became observable.
	Step() []obs.Event
	// Now is the current tick.
	Now() obs.Tick
	// Done reports that a replay has reached the end of its recording. A
	// live world never finishes.
	Done() bool

	// Ground truth, for the live scoreboard only. Everything here is read
	// AFTER detection has run; no detector can reach any of it.
	Label(obs.TxID) truth.Label
	IncidentOf(obs.TxID) truth.IncidentID
	Incidents() []truth.Incident

	// Nodes is the frozen display layout.
	Nodes() []Node
	// Inject arms a scenario. A replay cannot: its future is already
	// written, and pretending otherwise would silently desynchronise the
	// recording from what the interface claims is happening.
	Inject(name string, cfg chaos.Config) error
	// Live reports whether this source can be injected into.
	Live() bool
}
