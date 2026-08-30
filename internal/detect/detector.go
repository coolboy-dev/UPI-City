// Package detect scores the live transaction stream for suspicious activity.
//
// ┌───────────────────────────────────────────────────────────────────────┐
// │ This package MUST NOT import internal/sim or internal/truth, directly │
// │ or transitively. boundary_test.go walks the dependency graph and      │
// │ fails the build if it ever does.                                      │
// │                                                                       │
// │ Everything here knows about the network is an obs.Event, which has no │
// │ label. That is what makes precision, recall and detection latency     │
// │ meaningful rather than circular.                                      │
// └───────────────────────────────────────────────────────────────────────┘
package detect

import (
	"math"

	"github.com/yug/upi-city/internal/obs"
)

// ScoreFloor is the lowest score worth recording. Anything below it is
// treated as zero by the metrics layer.
//
// This is a storage decision, not a detection threshold: a run emits hundreds
// of thousands of events and recording a row for every one of them at every
// tick would produce files too large to keep. Real thresholding happens
// offline, in the sweep.
const ScoreFloor = 0.01

// Finding is one detector's suspicion about one agent at one moment.
//
// Score is continuous and carries NO threshold. Detectors do not decide;
// they measure. Turning a score into an allow/review/block decision is the
// alert policy's job, and it happens after every score has been recorded —
// which is what makes it possible to sweep the threshold offline and plot the
// precision/recall trade the operating point actually buys.
type Finding struct {
	Tick     obs.Tick           `json:"t"`
	TxID     obs.TxID           `json:"tx"`
	Agent    obs.AgentID        `json:"a"`
	Detector string             `json:"d"`
	Score    float64            `json:"s"`
	Evidence map[string]float64 `json:"e,omitempty"`
}

// Detector scores a stream of events.
//
// Observe is called once per event, in settlement order, single-threaded. It
// appends to dst rather than returning a fresh slice so the hot loop does not
// allocate.
type Detector interface {
	Name() string
	Observe(ev obs.Event, dst []Finding) []Finding
	Reset()
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// squash maps an unbounded non-negative statistic into [0,1), rising steeply
// at first and flattening out. knee is the value that maps to about 0.63.
func squash(x, knee float64) float64 {
	if x <= 0 || knee <= 0 {
		return 0
	}
	return 1 - math.Exp(-x/knee)
}

// grow extends a per-agent slice so index id is addressable. Detector state
// is indexed by dense agent id rather than held in a map: map iteration order
// is randomised in Go, and anything in this path that affects output order
// would silently break reproducibility.
func grow[T any](s []T, id obs.AgentID) []T {
	if int(id) < len(s) {
		return s
	}
	n := len(s)*2 + 1
	if n <= int(id) {
		n = int(id) + 1
	}
	out := make([]T, n)
	copy(out, s)
	return out
}

// seenSet is a bounded, generational record of an agent's counterparties.
//
// Counterparty RECURRENCE is the single most important discriminator in this
// project: a large merchant is paid over and over by the same customers,
// while a collection mule is paid by accounts that have never paid it before.
// Both look identical on volume alone.
//
// Two generations rather than eviction-by-age, because finding the oldest
// entry would mean iterating a map, and map order in Go is randomised.
type seenSet struct {
	cur, prev map[obs.AgentID]struct{}
	cap       int
}

func newSeenSet(capacity int) *seenSet {
	return &seenSet{cur: make(map[obs.AgentID]struct{}, 16), cap: capacity}
}

// Add records a counterparty and reports whether it had been seen before.
func (s *seenSet) Add(id obs.AgentID) (known bool) {
	if _, ok := s.cur[id]; ok {
		return true
	}
	if _, ok := s.prev[id]; ok {
		s.cur[id] = struct{}{}
		return true
	}
	if len(s.cur) >= s.cap {
		s.prev = s.cur
		s.cur = make(map[obs.AgentID]struct{}, 16)
	}
	s.cur[id] = struct{}{}
	return false
}

// Size is the approximate number of distinct counterparties remembered.
func (s *seenSet) Size() int { return len(s.cur) + len(s.prev) }
