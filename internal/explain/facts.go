package explain

import (
	"sort"

	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/truth"
)

// FromIncident assembles the facts for one incident from a completed run.
//
// Every figure is computed here, in Go, from the ground-truth record and the
// scored rows. The model receives only these numbers and never sees a
// transaction — it cannot do arithmetic, so it is not asked to.
func FromIncident(
	inc truth.Incident,
	rows metrics.Rows,
	lat metrics.IncidentLatency,
	blocked, reviewed int,
	msPerTick uint64,
) Facts {
	f := Facts{
		IncidentID: uint32(inc.ID),
		Kind:       inc.Kind,
		StartTick:  inc.StartTick,
		Members:    len(inc.Members),
		Detected:   lat.Detected,
		Blocked:    blocked,
		Reviewed:   reviewed,
	}
	if inc.EndTick >= inc.StartTick {
		f.DurationS = float64(inc.EndTick-inc.StartTick) * float64(msPerTick) / 1000
	}
	if lat.Detected {
		f.DetectedAfterS = lat.Seconds
	}

	// An infrastructure incident touches no accounts and labels no
	// transactions, so it is described from what happened in its WINDOW
	// rather than from its (empty) membership.
	if inc.Kind == "bank-outage" {
		for _, r := range rows {
			if r.Tick < inc.StartTick || r.Tick > inc.EndTick {
				continue
			}
			f.TxInWindow++
			if r.Failed {
				f.FailedInWindow++
			}
			if !r.Fraudulent() && r.Score > 0 {
				f.FalseAlarms++
			}
		}
		return f
	}

	var amounts []int64
	var total int64
	var maxIntensity float64
	var caught int

	for _, r := range rows {
		if r.Incident != inc.ID {
			continue
		}
		f.Transactions++
		total += r.AmountP
		amounts = append(amounts, r.AmountP)
		if r.Intensity > maxIntensity {
			maxIntensity = r.Intensity
		}
		if r.Score > 0 {
			caught++
		}
	}
	_ = caught

	f.TotalRupees = total / 100
	if len(amounts) > 0 {
		sort.Slice(amounts, func(i, j int) bool { return amounts[i] < amounts[j] })
		f.MedianRupees = amounts[len(amounts)/2] / 100
	}
	f.Intensity = maxIntensity
	if f.Transactions > 0 {
		f.MissedPct = 100 * float64(f.Transactions-lat.FlaggedTxOfIncident) / float64(f.Transactions)
	}

	// Which detectors actually drove the decision, aggregated across the
	// incident's flagged transactions.
	contrib := map[string]float64{}
	for _, r := range rows {
		if r.Incident != inc.ID || r.Score <= 0 {
			continue
		}
		for k, v := range r.Contributions {
			contrib[k] += v
		}
	}
	f.TopSignals = TopSignalsOf(contrib, 3)
	return f
}
