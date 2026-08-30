package metrics

import (
	"sort"

	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// IncidentLatency is how long one injected incident went undetected.
type IncidentLatency struct {
	IncidentID truth.IncidentID `json:"incident_id"`
	Kind       string           `json:"kind"`
	StartTick  obs.Tick         `json:"start_tick"`

	// Detected is false when the incident was never flagged at this
	// threshold. Ticks and Seconds are then meaningless and must be ignored
	// rather than treated as zero.
	Detected bool     `json:"detected"`
	Ticks    obs.Tick `json:"ticks,omitempty"`
	Seconds  float64  `json:"seconds,omitempty"`

	// FlaggedTxOfIncident counts how many of the incident's transactions were
	// caught, which distinguishes "noticed it once" from "saw it clearly".
	FlaggedTxOfIncident int `json:"flagged_tx"`
	TotalTxOfIncident   int `json:"total_tx"`
}

// LatencyReport summarises detection delay across all incidents.
type LatencyReport struct {
	Tau       float64           `json:"tau"`
	Incidents []IncidentLatency `json:"incidents"`

	// NeverDetected is reported as a first-class field, never folded away.
	//
	// Dropping undetected incidents from the mean is THE classic dishonest
	// move in this field: a detector that catches one incident out of ten,
	// quickly, then reports "median latency 3.1s" is describing its successes
	// and calling it performance. An incident that is never caught has
	// infinite latency, and the only honest way to summarise that is to count
	// it separately and say so.
	NeverDetected int `json:"never_detected"`
	Total         int `json:"total"`

	MedianTicks   obs.Tick `json:"median_ticks,omitempty"`
	MedianSeconds float64  `json:"median_seconds,omitempty"`
}

// Latency computes detection delay per incident at a given threshold.
//
// Delay is measured from the incident's StartTick — written by the engine at
// the moment of injection, not by the scenario — to the first transaction
// belonging to that incident whose score crosses tau.
func Latency(rows Rows, incidents []truth.Incident, tau float64, msPerTick uint64) LatencyReport {
	rep := LatencyReport{Tau: tau, Total: len(incidents)}

	// Group rows by incident, in tick order.
	byIncident := map[truth.IncidentID][]ScoredRow{}
	for _, r := range rows {
		if r.Incident != 0 {
			byIncident[r.Incident] = append(byIncident[r.Incident], r)
		}
	}

	var detectedTicks []obs.Tick

	// Iterate incidents in their own order, never over the map, so output
	// ordering is stable.
	for _, inc := range incidents {
		rs := byIncident[inc.ID]
		sort.Slice(rs, func(a, b int) bool { return rs[a].Tick < rs[b].Tick })

		il := IncidentLatency{
			IncidentID:        inc.ID,
			Kind:              inc.Kind,
			StartTick:         inc.StartTick,
			TotalTxOfIncident: len(rs),
		}
		for _, r := range rs {
			if r.Score >= tau {
				il.FlaggedTxOfIncident++
				if !il.Detected {
					il.Detected = true
					if r.Tick >= inc.StartTick {
						il.Ticks = r.Tick - inc.StartTick
					}
					il.Seconds = float64(il.Ticks) * float64(msPerTick) / 1000
				}
			}
		}
		if il.Detected {
			detectedTicks = append(detectedTicks, il.Ticks)
		} else {
			rep.NeverDetected++
		}
		rep.Incidents = append(rep.Incidents, il)
	}

	if len(detectedTicks) > 0 {
		sort.Slice(detectedTicks, func(a, b int) bool { return detectedTicks[a] < detectedTicks[b] })
		rep.MedianTicks = detectedTicks[len(detectedTicks)/2]
		rep.MedianSeconds = float64(rep.MedianTicks) * float64(msPerTick) / 1000
	}
	return rep
}

// ArchetypeFP counts false positives by the sender's true archetype.
type ArchetypeFP struct {
	Archetype   string  `json:"archetype"`
	HardNeg     bool    `json:"hard_negative"`
	FP          int     `json:"fp"`
	LegitTx     int     `json:"legit_tx"`
	FPPer1kThis float64 `json:"fp_per_1k_of_this_archetype"`
}

// FalsePositiveBreakdown attributes false positives to the kind of legitimate
// customer that suffered them.
//
// A single aggregate FP count says a detector is wrong; this says WHY it is
// wrong, and whether the errors fall on the traffic that was designed to be
// confusable. If the false positives were spread evenly across ordinary
// consumers instead of concentrating on the hard negatives, that would mean
// the detector is simply noisy rather than facing a genuinely hard problem.
func FalsePositiveBreakdown(rows Rows, tau float64) []ArchetypeFP {
	type acc struct {
		fp, legit int
		hardNeg   bool
	}
	byArch := map[string]*acc{}

	for _, r := range rows {
		if r.Fraudulent() {
			continue
		}
		name := r.Archetype.String()
		a := byArch[name]
		if a == nil {
			a = &acc{hardNeg: r.Archetype.HardNegative()}
			byArch[name] = a
		}
		a.legit++
		if r.Score >= tau {
			a.fp++
		}
	}

	names := make([]string, 0, len(byArch))
	for n := range byArch {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]ArchetypeFP, 0, len(names))
	for _, n := range names {
		a := byArch[n]
		e := ArchetypeFP{Archetype: n, HardNeg: a.hardNeg, FP: a.fp, LegitTx: a.legit}
		if a.legit > 0 {
			e.FPPer1kThis = 1000 * float64(a.fp) / float64(a.legit)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FP > out[j].FP })
	return out
}
