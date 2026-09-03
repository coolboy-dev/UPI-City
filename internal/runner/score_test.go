package runner

import (
	"testing"

	"github.com/yug/upi-city/internal/detect"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/risk"
	"github.com/yug/upi-city/internal/truth"
)

// mapLabels is ground truth that did not come from a simulator: a lookup
// table, which is the shape external data arrives in. A payment processor's
// chargeback record is exactly this and nothing more.
type mapLabels struct {
	label map[obs.TxID]truth.Label
}

func (m mapLabels) Label(id obs.TxID) truth.Label { return m.label[id] }

// External data carries no injected incidents, no archetypes and no attack
// ramp. Reporting anything but the zero value here would be inventing
// structure the source does not have.
func (m mapLabels) Incident(obs.TxID) truth.IncidentID    { return 0 }
func (m mapLabels) Archetype(obs.AgentID) truth.Archetype { return truth.ArchConsumer }
func (m mapLabels) Intensity(obs.Tick) float64            { return 0 }

func newTestScorer(agentCap int) *Scorer {
	return NewScorer(ScoreConfig{
		Pipeline: detect.NewDefault(),
		Fusion:   risk.DefaultFusion(),
		AgentCap: agentCap,
	})
}

// TestScorerAcceptsExternalTruth is the point of the Labels seam: the scoring
// body must run against truth that no simulator produced.
//
// If this needs a *sim.World to work, the real-data path has to be a second
// implementation of the scoring loop — and then "the real data ran through the
// same code" stops being true, silently, while still compiling.
func TestScorerAcceptsExternalTruth(t *testing.T) {
	lb := mapLabels{label: map[obs.TxID]truth.Label{
		2: truth.LabelRingMule,
		4: truth.LabelRingMule,
	}}
	s := newTestScorer(64)

	for i := 1; i <= 5; i++ {
		e := obs.Event{
			Tick: obs.Tick(i), SettleTick: obs.Tick(i), TxID: obs.TxID(i),
			From: obs.AgentID(i), To: obs.AgentID(i + 1),
			AmountP: int64(1000 * i), Status: obs.StatusSuccess,
		}
		if err := s.Observe(e, lb); err != nil {
			t.Fatalf("observe: %v", err)
		}
	}

	rows := s.Rows()
	if len(rows) != 5 {
		t.Fatalf("every event must produce a row, got %d of 5", len(rows))
	}
	var fraud int
	for _, r := range rows {
		if r.Fraudulent() {
			fraud++
		}
	}
	if fraud != 2 {
		t.Fatalf("external labels did not reach the rows: %d fraudulent, want 2", fraud)
	}
}

// TestScorerSurvivesAgentIDsBeyondCapacity guards a crash the real-data path
// walks straight into.
//
// Per-agent state is indexed directly by agent id. In the simulator ids are
// dense and bounded by the population size, so the bound is trivially correct.
// An external file's identifiers are whatever the source assigned them — the
// IEEE-CIS card ids run past 18,000 with gaps — and a caller that sizes
// AgentCap from a row COUNT rather than a maximum id produces an out-of-range
// write on the first oversized id. It must be dropped from per-agent state,
// not panic, and the transaction must still be scored and graded.
func TestScorerSurvivesAgentIDsBeyondCapacity(t *testing.T) {
	lb := mapLabels{label: map[obs.TxID]truth.Label{1: truth.LabelRingMule}}
	s := newTestScorer(8)

	e := obs.Event{
		Tick: 1, SettleTick: 1, TxID: 1,
		From: 99999, To: 100000,
		AmountP: 5000, Status: obs.StatusSuccess,
	}
	if err := s.Observe(e, lb); err != nil {
		t.Fatalf("observe: %v", err)
	}
	rows := s.Rows()
	if len(rows) != 1 {
		t.Fatalf("oversized agent id dropped the transaction entirely: %d rows", len(rows))
	}
	if !rows[0].Fraudulent() {
		t.Fatal("oversized agent id lost the label")
	}
}

// TestScorerHashesEveryEvent checks the determinism gate still has something
// to gate on after the scoring body moved: the hash must depend on the stream.
func TestScorerHashesEveryEvent(t *testing.T) {
	lb := mapLabels{label: map[obs.TxID]truth.Label{}}
	mk := func(amount int64) obs.StreamHash {
		s := newTestScorer(64)
		for i := 1; i <= 3; i++ {
			e := obs.Event{
				Tick: obs.Tick(i), SettleTick: obs.Tick(i), TxID: obs.TxID(i),
				From: obs.AgentID(i), To: obs.AgentID(i + 1),
				AmountP: amount, Status: obs.StatusSuccess,
			}
			if err := s.Observe(e, lb); err != nil {
				t.Fatalf("observe: %v", err)
			}
		}
		return s.Hash()
	}
	if mk(1000) == mk(1001) {
		t.Fatal("stream hash ignored a change in amount")
	}
	if mk(1000) != mk(1000) {
		t.Fatal("stream hash is not deterministic")
	}
}
