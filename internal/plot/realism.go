package plot

import (
	"fmt"
	"math"

	"github.com/yug/upi-city/internal/metrics"
)

// AmountCDF draws the simulated person-to-merchant amount distribution against
// the published figures it was fitted to.
//
// An honest note about what this figure can and cannot show: NPCI and SBI
// Research publish summary statistics, not the underlying distribution. There
// are two anchor points available — "86% of P2M payments are under ₹500" and a
// median ticket size — not a curve. So the simulated CDF is drawn in full and
// the published anchors are drawn as points on top of it. Anywhere the curve
// is not near an anchor, nothing is being claimed.
//
// The x axis is log₁₀(rupees), because payment amounts span four orders of
// magnitude and a linear axis would compress the entire consumer range into
// the leftmost few pixels.
func AmountCDF(r metrics.Realism) string {
	c := New(720, 400,
		"simulated P2M amount distribution vs published anchors",
		"payment amount (₹, log scale)", "cumulative share of P2M payments")
	c.XMin, c.XMax = 0, 4 // ₹1 to ₹10,000
	c.YMin, c.YMax = 0, 1

	if len(r.P2MPercentiles) == 0 {
		return c.String()
	}

	xs := make([]float64, 0, len(r.P2MPercentiles))
	ys := make([]float64, 0, len(r.P2MPercentiles))
	for i, amt := range r.P2MPercentiles {
		if amt < 1 {
			amt = 1
		}
		xs = append(xs, math.Log10(amt))
		ys = append(ys, float64(i)/100)
	}
	c.Polyline(xs, ys, "#2563eb", 2.4, "")

	// The ₹500 threshold the published share is quoted at.
	x500 := math.Log10(500)
	c.Polyline([]float64{x500, x500}, []float64{0, 1}, "#94a3b8", 1.2, "4,3")

	// Published anchor: 86% under ₹500.
	c.Marker(x500, 0.86, "#dc2626", "published 86%")

	// Where the simulator actually lands at ₹500.
	if v, ok := r.Observed["share of P2M payments under ₹500"]; ok {
		c.Marker(x500, v, "#2563eb", fmt.Sprintf("simulated %.1f%%", v*100))
	}

	c.Axes(5, 5)
	c.Legend([]LegendEntry{
		{Colour: "#2563eb", Label: "simulated P2M CDF"},
		{Colour: "#dc2626", Label: "published anchor (SBI Research)"},
	})
	return c.String()
}
