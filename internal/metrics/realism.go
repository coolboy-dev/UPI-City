package metrics

import "sort"

// Reference is a published statistic about real UPI traffic, with its source.
//
// Every number here is external and citable. The point of the comparison is to
// stop the simulator's realism being an assertion, and that only works if the
// targets cannot be quietly adjusted to match whatever the simulator happens
// to produce.
type Reference struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Source string  `json:"source"`
	// Tolerance is the factor within which the simulator is considered
	// close enough. Generous, because the goal is "same order of magnitude
	// and same shape", not a fitted model.
	Tolerance float64 `json:"tolerance"`
}

// UPIReferences are the published figures this simulator is checked against.
func UPIReferences() []Reference {
	return []Reference{
		{
			Name: "average ticket size", Value: 1348, Unit: "₹",
			Source:    "NPCI/SBI Research, H1 2025 (Jan-Jun)",
			Tolerance: 2.0,
		},
		{
			// Measured over person-to-merchant traffic ONLY, because that is
			// the population the published figure covers. Comparing it against
			// all traffic — as this check originally did — silently mixes in
			// payroll and business settlement and understates the match.
			Name: "share of P2M payments under ₹500", Value: 0.86, Unit: "fraction",
			Source:    "SBI Research, P2M transactions, Jul 2025",
			Tolerance: 1.3,
		},
		{
			Name: "person-to-merchant share of volume", Value: 0.63, Unit: "fraction",
			Source:    "NPCI, 2025",
			Tolerance: 1.5,
		},
		{
			Name: "fraud as share of transaction VALUE", Value: 0.005, Unit: "fraction",
			Source:    "RBI, FY2023-24 (₹1,087 cr of UPI fraud losses)",
			Tolerance: 4.0,
		},
		{
			Name: "fraud as share of transaction COUNT", Value: 0.00001, Unit: "fraction",
			Source:    "RBI FY24: ~1.34M fraud cases against 131bn UPI transactions",
			Tolerance: 10.0,
		},
	}
}

// Realism compares simulated traffic to published reality.
type Realism struct {
	Observed  map[string]float64 `json:"observed"`
	Reference []Reference        `json:"reference"`
	Ratio     map[string]float64 `json:"ratio_observed_over_reference"`
	Within    map[string]bool    `json:"within_tolerance"`

	// PrecisionAtRealPrevalence is the number that matters most here.
	//
	// Precision depends on how rare fraud is. Holding the detector's true and
	// false positive RATES fixed and moving prevalence to the published
	// figure shows what the headline precision would really be against live
	// traffic. It is much worse, and saying so is the entire reason this
	// comparison exists.
	PrecisionSimulated float64 `json:"precision_at_simulated_prevalence"`
	PrecisionReal      float64 `json:"precision_at_real_prevalence"`
	SimPrevalence      float64 `json:"simulated_prevalence"`
	RealPrevalence     float64 `json:"real_prevalence"`

	// P2MPercentiles is the simulated person-to-merchant amount distribution
	// sampled at each percentile, in rupees, so the fit can be drawn rather
	// than only tabulated. 101 entries, index i = the i-th percentile.
	P2MPercentiles []float64 `json:"p2m_percentiles_rupees"`
}

// CheckRealism measures the simulated traffic against published UPI figures.
//
// isP2M reports whether a row is a person-to-merchant payment, which NPCI
// defines by who RECEIVES it. The caller supplies it because only the caller
// knows the world's merchant set; rows carry ids, not archetypes of receivers.
func CheckRealism(rows Rows, isP2M func(row ScoredRow) bool, op Operating) Realism {
	r := Realism{
		Observed:  map[string]float64{},
		Reference: UPIReferences(),
		Ratio:     map[string]float64{},
		Within:    map[string]bool{},
	}
	if len(rows) == 0 {
		return r
	}

	var totalRupees, fraudRupees int64
	var p2mUnder500, p2mCount, fraudCount int
	amounts := make([]int64, 0, len(rows))
	p2m := make([]int64, 0, len(rows))

	for _, row := range rows {
		rupees := row.AmountP / 100
		totalRupees += rupees
		amounts = append(amounts, rupees)
		// The under-₹500 share is P2M-scoped, so it is counted only over
		// merchant-directed payments.
		if isP2M(row) {
			p2mCount++
			p2m = append(p2m, rupees)
			if rupees < 500 {
				p2mUnder500++
			}
		}
		if row.Fraudulent() {
			fraudCount++
			fraudRupees += rupees
		}
	}
	n := float64(len(rows))

	// Average ticket is NOT P2M-scoped: the NPCI figure covers all UPI.
	r.Observed["average ticket size"] = float64(totalRupees) / n
	if p2mCount > 0 {
		r.Observed["share of P2M payments under ₹500"] = float64(p2mUnder500) / float64(p2mCount)
	}
	r.Observed["person-to-merchant share of volume"] = float64(p2mCount) / n
	r.Observed["fraud as share of transaction VALUE"] = float64(fraudRupees) / float64(totalRupees)
	r.Observed["fraud as share of transaction COUNT"] = float64(fraudCount) / n

	sort.Slice(amounts, func(i, j int) bool { return amounts[i] < amounts[j] })
	r.Observed["median ticket size"] = float64(amounts[len(amounts)/2])

	sort.Slice(p2m, func(i, j int) bool { return p2m[i] < p2m[j] })
	if len(p2m) > 0 {
		r.P2MPercentiles = make([]float64, 101)
		for i := range r.P2MPercentiles {
			k := i * (len(p2m) - 1) / 100
			r.P2MPercentiles[i] = float64(p2m[k])
		}
		r.Observed["median P2M ticket size"] = r.P2MPercentiles[50]
	}

	for _, ref := range r.Reference {
		obs, ok := r.Observed[ref.Name]
		if !ok || ref.Value == 0 {
			continue
		}
		ratio := obs / ref.Value
		r.Ratio[ref.Name] = ratio
		r.Within[ref.Name] = ratio <= ref.Tolerance && ratio >= 1/ref.Tolerance
	}

	// Re-state precision at the real base rate.
	r.SimPrevalence = rows.Prevalence()
	r.RealPrevalence = 0.00001
	r.PrecisionSimulated = op.Precision

	if op.TP+op.FN > 0 && op.FP+op.TN > 0 {
		tpr := float64(op.TP) / float64(op.TP+op.FN)
		fpr := float64(op.FP) / float64(op.FP+op.TN)
		p := r.RealPrevalence
		denom := tpr*p + fpr*(1-p)
		if denom > 0 {
			r.PrecisionReal = tpr * p / denom
		}
	}
	return r
}
