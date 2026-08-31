package plot

// DriftArm is one experimental arm's decay curve.
type DriftArm struct {
	Name string
	Tick []float64
	Lift []float64
	ECE  []float64
}

// Drift draws detection quality against time, for a drifting and a static
// legitimate population.
//
// Two series per arm, on deliberately different axes of interpretation:
// ranking lift (higher is better, normalised by prevalence so buckets are
// comparable) and calibration error (lower is better). They are drawn together
// because the expected result is that they diverge — ranking holding up while
// calibration rots is the failure this figure exists to make visible.
func Drift(arms []DriftArm) string {
	c := New(760, 400, "detector ageing under concept drift",
		"tick", "AUC-PR lift over chance  (solid) · ECE ×100 (dashed)")
	if len(arms) == 0 {
		return c.String()
	}

	maxX, maxY := 0.0, 1.0
	for _, a := range arms {
		for i := range a.Tick {
			if a.Tick[i] > maxX {
				maxX = a.Tick[i]
			}
			if a.Lift[i] > maxY {
				maxY = a.Lift[i]
			}
			if a.ECE[i]*100 > maxY {
				maxY = a.ECE[i] * 100
			}
		}
	}
	c.XMin, c.XMax = 0, maxX
	c.YMin, c.YMax = 0, maxY*1.1

	colours := []string{"#64748b", "#0ea5e9", "#f59e0b", "#ef4444", "#7c3aed"}
	var legend []LegendEntry
	for i, a := range arms {
		col := colours[i%len(colours)]
		c.Polyline(a.Tick, a.Lift, col, 2.4, "")
		ece := make([]float64, len(a.ECE))
		for j, v := range a.ECE {
			ece[j] = v * 100
		}
		c.Polyline(a.Tick, ece, col, 1.6, "5,4")
		legend = append(legend, LegendEntry{Colour: col, Label: a.Name})
	}
	c.Axes(6, 5)
	c.Legend(legend)
	return c.String()
}
