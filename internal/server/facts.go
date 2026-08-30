package server

import (
	"sort"

	"github.com/yug/upi-city/internal/explain"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// factsFor assembles the facts for a live incident.
//
// A live run has no completed metrics report to draw on, so the figures are
// accumulated as the world turns. Everything here is computed in Go; the
// narrative layer receives finished numbers and is never asked to do
// arithmetic on a transaction list.
func (s *Server) factsFor(inc truth.Incident) explain.Facts {
	f := explain.Facts{
		IncidentID: uint32(inc.ID),
		Kind:       inc.Kind,
		StartTick:  inc.StartTick,
		Members:    len(inc.Members),
	}

	end := inc.EndTick
	if end < inc.StartTick || end == ^obs.Tick(0) {
		end = s.src.Now()
	}
	f.DurationS = float64(end-inc.StartTick) * float64(s.cfg.MsPerTick) / 1000

	s.mu.Lock()
	st := s.incStats[inc.ID]
	f.Blocked, f.Reviewed = s.live.FraudBlocked, s.live.Reviewed
	s.mu.Unlock()

	if inc.Kind == "bank-outage" {
		f.TxInWindow, f.FailedInWindow, f.FalseAlarms = st.txInWindow, st.failed, st.falseAlarms
		return f
	}

	f.Transactions = st.tx
	f.TotalRupees = st.totalP / 100
	if n := len(st.amounts); n > 0 {
		a := append([]int64(nil), st.amounts...)
		sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
		f.MedianRupees = a[n/2] / 100
	}
	f.Detected = st.firstFlagTick > 0
	if f.Detected {
		f.DetectedAfterS = float64(st.firstFlagTick-inc.StartTick) * float64(s.cfg.MsPerTick) / 1000
	}
	if st.tx > 0 {
		f.MissedPct = 100 * float64(st.tx-st.flagged) / float64(st.tx)
	}
	f.TopSignals = explain.TopSignalsOf(st.contrib, 3)
	return f
}

// incidentStats accumulates what an incident has done so far.
type incidentStats struct {
	tx      int
	flagged int
	totalP  int64
	amounts []int64
	contrib map[string]float64

	firstFlagTick obs.Tick

	// Infrastructure incidents are described by their window rather than by
	// membership: an outage recruits nobody and labels no transaction.
	txInWindow  int
	failed      int
	falseAlarms int
}
