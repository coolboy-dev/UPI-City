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
			Name: "share of payments under ₹500", Value: 0.86, Unit: "fraction",
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
}

// CheckRealism measures the simulated traffic against published UPI figures.
//
// merchantIDs is the set of agents that count as merchants, so the
// person-to-merchant share can be computed the way NPCI reports it.
func CheckRealism(rows Rows, isMerchant func(row ScoredRow) bool, op Operating) Realism {
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
	var under500, toMerchant, fraudCount int
	amounts := make([]int64, 0, len(rows))

	for _, row := range rows {
		rupees := row.AmountP / 100
		totalRupees += rupees
		amounts = append(amounts, rupees)
		if rupees < 500 {
			under500++
		}
		if isMerchant(row) {
			toMerchant++
		}
		if row.Fraudulent() {
			fraudCount++
			fraudRupees += rupees
		}
	}
	n := float64(len(rows))

	r.Observed["average ticket size"] = float64(totalRupees) / n
	r.Observed["share of payments under ₹500"] = float64(under500) / n
	r.Observed["person-to-merchant share of volume"] = float64(toMerchant) / n
	r.Observed["fraud as share of transaction VALUE"] = float64(fraudRupees) / float64(totalRupees)
	r.Observed["fraud as share of transaction COUNT"] = float64(fraudCount) / n

	sort.Slice(amounts, func(i, j int) bool { return amounts[i] < amounts[j] })
	r.Observed["median ticket size"] = float64(amounts[len(amounts)/2])

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
