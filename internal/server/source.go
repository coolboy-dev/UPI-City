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

	// Rival is a competing detector's score for a transaction, precomputed by
	// a program outside this repository and read back from disk.
	//
	// ─── Why the rival's scores are precomputed rather than live ────────────
	//
	// The point of the head-to-head view is that the challenger never saw the
	// answer key, and the cleanest proof of that is that it ran in a different
	// process, at a different time, against a file containing no labels. Piping
	// a Python process into the tick loop would make the demo depend on that
	// process staying alive, and would buy nothing: the scores are a pure
	// function of the recording, so computing them twice would produce the same
	// numbers more slowly and with more ways to fail on stage.
	//
	// A live world has no rival — nobody has scored a run that has not happened
	// yet — so it returns zero and the interface hides the comparison.
	Rival(obs.TxID) float64
	// RivalName identifies the challenger, or "" when there is none.
	RivalName() string
	// RivalTauForRate is the challenger's score threshold that would flag the
	// given share of the recording.
	//
	// ─── Why the two detectors are not compared at one threshold ────────────
	//
	// A raw score means whatever the detector that produced it decided it
	// should mean. This project's fused score is a weighted sum with a strongly
	// negative bias, so 0.15 is already selective; an IsolationForest's
	// normalised anomaly score is diffuse, and 0.15 lands near the middle of
	// its distribution. Judged at one shared tau the challenger flagged 15,487
	// legitimate payments to this project's 141 — a difference that measures
	// where two authors happened to put their scales, not which detector is
	// better.
	//
	// Matching on flag rate instead asks the question a risk team actually
	// asks: given the same analyst budget, who finds more fraud? That is the
	// same footing the leaderboard reports at, and it is the only comparison
	// that survives the two scores having been designed independently.
	RivalTauForRate(rate float64) float64

	// Nodes is the frozen display layout.
	Nodes() []Node
	// Inject arms a scenario. A replay cannot: its future is already
	// written, and pretending otherwise would silently desynchronise the
	// recording from what the interface claims is happening.
	Inject(name string, cfg chaos.Config) error
	// Live reports whether this source can be injected into.
	Live() bool
}
