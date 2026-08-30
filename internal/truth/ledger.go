package truth

import "github.com/yug/upi-city/internal/obs"

// Ledger records what actually happened during a run.
//
// Labels are DERIVED BY THE ENGINE from agent state, never written by
// scenario authors. When a chaos effect puts an agent into a fraud state,
// every transaction that agent originates while that state is active is
// labelled and attributed automatically. This is deliberate: a scenario
// author cannot forget to label a transaction, and cannot mislabel one.
type Ledger struct {
	labels    []Label      // indexed by TxID
	incOfTx   []IncidentID // indexed by TxID; 0 means none
	archetype []Archetype  // indexed by AgentID
	incidents []Incident
}

// NewLedger pre-allocates for the expected population and transaction volume.
func NewLedger(numAgents int, expectedTx int) *Ledger {
	return &Ledger{
		labels:    make([]Label, 0, expectedTx),
		incOfTx:   make([]IncidentID, 0, expectedTx),
		archetype: make([]Archetype, numAgents),
	}
}

// SetArchetype records an agent's behavioural class at construction time.
func (l *Ledger) SetArchetype(id obs.AgentID, a Archetype) {
	l.archetype[id] = a
}

// Archetype returns an agent's behavioural class.
func (l *Ledger) Archetype(id obs.AgentID) Archetype { return l.archetype[id] }

// RecordTx labels a transaction. TxIDs are assigned in strictly increasing
// order, so this appends densely.
func (l *Ledger) RecordTx(id obs.TxID, label Label, inc IncidentID) {
	for obs.TxID(len(l.labels)) <= id {
		l.labels = append(l.labels, LabelNormal)
		l.incOfTx = append(l.incOfTx, 0)
	}
	l.labels[id] = label
	l.incOfTx[id] = inc
	if inc != 0 {
		if i := l.index(inc); i >= 0 {
			l.incidents[i].TxIDs = append(l.incidents[i].TxIDs, id)
		}
	}
}

// Label returns the true label of a transaction.
func (l *Ledger) Label(id obs.TxID) Label {
	if int(id) >= len(l.labels) {
		return LabelNormal
	}
	return l.labels[id]
}

// IncidentOf returns the incident a transaction belongs to, or 0.
func (l *Ledger) IncidentOf(id obs.TxID) IncidentID {
	if int(id) >= len(l.incOfTx) {
		return 0
	}
	return l.incOfTx[id]
}

// NumTx returns how many transactions have been labelled.
func (l *Ledger) NumTx() int { return len(l.labels) }

// OpenIncident begins an incident and returns its id. Called by the engine's
// single effect-application path, never by a scenario directly.
func (l *Ledger) OpenIncident(kind string, start obs.Tick, members []obs.AgentID) IncidentID {
	id := IncidentID(len(l.incidents) + 1)
	m := make([]obs.AgentID, len(members))
	copy(m, members)
	l.incidents = append(l.incidents, Incident{
		ID:        id,
		Kind:      kind,
		StartTick: start,
		EndTick:   ^obs.Tick(0),
		Members:   m,
	})
	return id
}

// CloseIncident stamps an incident's end tick.
func (l *Ledger) CloseIncident(id IncidentID, end obs.Tick) {
	if i := l.index(id); i >= 0 {
		l.incidents[i].EndTick = end
	}
}

// Incidents returns all recorded incidents in injection order.
func (l *Ledger) Incidents() []Incident { return l.incidents }

func (l *Ledger) index(id IncidentID) int {
	i := int(id) - 1
	if i < 0 || i >= len(l.incidents) {
		return -1
	}
	return i
}

// Prevalence returns the fraction of transactions that were fraudulent.
//
// Worth watching: a benchmark at 20% prevalence is trivially easy and is the
// first thing a sharp reviewer will attack. The target here is 1.5-3%.
func (l *Ledger) Prevalence() float64 {
	if len(l.labels) == 0 {
		return 0
	}
	var n int
	for _, lb := range l.labels {
		if lb.Fraudulent() {
			n++
		}
	}
	return float64(n) / float64(len(l.labels))
}
