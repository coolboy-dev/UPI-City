package metrics

import "sort"

// Stat is a value with the spread it showed across seeds.
//
// Every headline figure carries one. A mean on its own invites the reader to
// assume the mean is the whole story, and in this project latency alone
// ranges from under two seconds to over twenty depending on which world you
// happen to simulate.
type Stat struct {
	Mean float64 `json:"mean"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

// NewStat summarises a slice.
func NewStat(vs []float64) Stat {
	if len(vs) == 0 {
		return Stat{}
	}
	c := append([]float64(nil), vs...)
	sort.Float64s(c)
	var sum float64
	for _, v := range c {
		sum += v
	}
	return Stat{Mean: sum / float64(len(c)), Min: c[0], Max: c[len(c)-1]}
}

// Summary aggregates many runs.
type Summary struct {
	Seeds int `json:"seeds"`

	AUCPR      Stat `json:"aucpr"`
	Precision  Stat `json:"precision"`
	Recall     Stat `json:"recall"`
	FPPer1k    Stat `json:"fp_per_1k_legit"`
	Prevalence Stat `json:"prevalence"`
	LatencySec Stat `json:"latency_seconds"`

	BaselineAUCPR map[string]Stat `json:"baseline_aucpr"`

	IncidentsTotal int `json:"incidents_total"`
	NeverDetected  int `json:"incidents_never_detected"`

	PermutationPassed int `json:"permutation_control_passed"`
}

// Summarise aggregates a set of per-run reports.
func Summarise(reps []Report) Summary {
	s := Summary{Seeds: len(reps), BaselineAUCPR: map[string]Stat{}}
	var auc, prec, rec, fp, prev, lat []float64
	byBaseline := map[string][]float64{}

	for _, r := range reps {
		auc = append(auc, r.AUCPR)
		prec = append(prec, r.BestF1.Precision)
		rec = append(rec, r.BestF1.Recall)
		fp = append(fp, r.BestF1.FPPer1kLegit)
		prev = append(prev, r.Prevalence)
		if r.Latency.Total > r.Latency.NeverDetected {
			lat = append(lat, r.Latency.MedianSeconds)
		}
		s.IncidentsTotal += r.Latency.Total
		s.NeverDetected += r.Latency.NeverDetected
		if r.Permutation.Passed {
			s.PermutationPassed++
		}
		for _, bl := range r.Baselines {
			byBaseline[bl.Name] = append(byBaseline[bl.Name], bl.AUCPR)
		}
	}

	s.AUCPR, s.Precision, s.Recall = NewStat(auc), NewStat(prec), NewStat(rec)
	s.FPPer1k, s.Prevalence, s.LatencySec = NewStat(fp), NewStat(prev), NewStat(lat)
	for n, vs := range byBaseline {
		s.BaselineAUCPR[n] = NewStat(vs)
	}
	return s
}
