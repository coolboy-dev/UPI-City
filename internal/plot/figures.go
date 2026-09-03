package plot

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/risk"
)

const (
	colDetector = "#2563eb"
	colRandom   = "#94a3b8"
	colAmount   = "#f59e0b"
	colAlways   = "#a78bfa"
	colHardNeg  = "#dc2626"
	colNormal   = "#64748b"
	colPoint    = "#059669"
)

// PRCurve draws precision against recall, with the baselines on the same axes.
//
// A precision/recall curve alone is decoration: 0.14 precision could be
// excellent or useless and the reader cannot tell. Plotting the trivial
// scorers on the same axes turns the figure into evidence, and marking the
// chosen operating point turns it into a decision rather than a shape.
func PRCurve(r metrics.Report) string {
	c := New(680, 420,
		fmt.Sprintf("Precision vs recall — %d transactions, %.2f%% fraudulent", r.Transactions, r.Prevalence*100),
		"recall", "precision")
	c.XMin, c.XMax = 0, 1
	c.YMin, c.YMax = 0, 1
	c.Axes(5, 5)

	draw := func(ops []metrics.Operating, colour, dash string) {
		pts := append([]metrics.Operating(nil), ops...)
		sort.Slice(pts, func(a, b int) bool { return pts[a].Recall < pts[b].Recall })
		xs := make([]float64, 0, len(pts))
		ys := make([]float64, 0, len(pts))
		for _, o := range pts {
			if o.TP+o.FP == 0 {
				continue
			}
			xs = append(xs, o.Recall)
			ys = append(ys, o.Precision)
		}
		c.Polyline(xs, ys, colour, 2.2, dash)
	}

	for _, bl := range r.Baselines {
		switch bl.Name {
		case "random":
			draw(bl.Ops, colRandom, "4 3")
		case "amount-rank":
			draw(bl.Ops, colAmount, "6 3")
		case "always-flag":
			draw(bl.Ops, colAlways, "2 3")
		}
	}
	draw(r.Curve, colDetector, "")

	c.Marker(r.BestF1.Recall, r.BestF1.Precision, colPoint,
		fmt.Sprintf("chosen tau=%.2f", r.BestF1.Tau))

	c.Legend([]LegendEntry{
		{Label: fmt.Sprintf("detectors (AUC %.3f)", r.AUCPR), Colour: colDetector},
		{Label: "amount-rank", Colour: colAmount, Dash: "6 3"},
		{Label: "random", Colour: colRandom, Dash: "4 3"},
		{Label: "always-flag", Colour: colAlways, Dash: "2 3"},
	})
	return c.String()
}

// TradeCurve shows what a threshold buys and what it costs, on one axis the
// reader already understands: how much legitimate traffic gets caught up.
func TradeCurve(r metrics.Report) string {
	c := New(680, 420, "What each threshold buys, and what it costs",
		"threshold (tau)", "")
	c.XMin, c.XMax = 0, 1

	var maxFP float64
	for _, o := range r.Curve {
		if o.FPPer1kLegit > maxFP {
			maxFP = o.FPPer1kLegit
		}
	}
	if maxFP <= 0 {
		maxFP = 1
	}
	c.YMin, c.YMax = 0, 1
	c.Axes(5, 5)

	xs := make([]float64, 0, len(r.Curve))
	rec := make([]float64, 0, len(r.Curve))
	fps := make([]float64, 0, len(r.Curve))
	for _, o := range r.Curve {
		xs = append(xs, o.Tau)
		rec = append(rec, o.Recall)
		fps = append(fps, o.FPPer1kLegit/maxFP)
	}
	c.Polyline(xs, rec, colDetector, 2.2, "")
	c.Polyline(xs, fps, colHardNeg, 2.2, "5 3")
	c.Marker(r.BestF1.Tau, r.BestF1.Recall, colPoint, "chosen")

	c.Legend([]LegendEntry{
		{Label: "recall (fraud caught)", Colour: colDetector},
		{Label: fmt.Sprintf("FP/1k legit (max %.0f)", maxFP), Colour: colHardNeg, Dash: "5 3"},
	})
	return c.String()
}

// FPBreakdown shows which kind of legitimate customer absorbs the errors.
//
// This is the figure that says whether the detector is facing a hard problem
// or is merely noisy. Errors concentrated on the deliberately-confusable
// archetypes mean the discrimination is genuinely difficult; errors spread
// evenly across ordinary consumers mean the detector is guessing.
func FPBreakdown(r metrics.Report) string {
	rows := append([]metrics.ArchetypeFP(nil), r.FPByArch...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].FPPer1kThis > rows[j].FPPer1kThis })
	if len(rows) == 0 {
		rows = append(rows, metrics.ArchetypeFP{Archetype: "(none)"})
	}

	c := New(680, 60+34*len(rows),
		fmt.Sprintf("False positives per 1,000 legitimate transactions (tau=%.2f)", r.BestF1.Tau),
		"", "")
	c.L, c.B = 130, 30

	var maxV float64
	for _, a := range rows {
		if a.FPPer1kThis > maxV {
			maxV = a.FPPer1kThis
		}
	}
	if maxV <= 0 {
		maxV = 1
	}
	for i, a := range rows {
		colour := colNormal
		label := a.Archetype
		if a.HardNeg {
			colour = colHardNeg
			label += " *"
		}
		c.HBar(i, len(rows), a.FPPer1kThis/maxV, colour, label,
			fmt.Sprintf("%.1f  (%d of %d)", a.FPPer1kThis, a.FP, a.LegitTx))
	}
	return c.String()
}

// LatencySpread plots detection latency per seed, so the variance is visible
// rather than collapsed into a single reassuring median.
func LatencySpread(reports []metrics.Report) string {
	type pt struct {
		seed    float64
		seconds float64
		missed  bool
	}
	var pts []pt
	var maxS float64
	for _, r := range reports {
		p := pt{seed: float64(r.Seed)}
		if r.Latency.Total > r.Latency.NeverDetected {
			p.seconds = r.Latency.MedianSeconds
		} else {
			p.missed = true
		}
		if p.seconds > maxS {
			maxS = p.seconds
		}
		pts = append(pts, p)
	}
	if maxS <= 0 {
		maxS = 1
	}

	c := New(680, 380, "Detection latency by seed (one run is an anecdote)",
		"seed", "seconds from injection to first flag")
	c.XMin, c.XMax = 0, float64(len(pts))+1
	c.YMin, c.YMax = 0, maxS*1.15
	c.Axes(len(pts), 5)

	for _, p := range pts {
		if p.missed {
			c.Marker(p.seed, 0, colHardNeg, "never")
			continue
		}
		c.Polyline([]float64{p.seed, p.seed}, []float64{0, p.seconds}, colDetector, 6, "")
		c.Marker(p.seed, p.seconds, colPoint, "")
	}
	return c.String()
}

// WriteAll renders every figure into dir.
func WriteAll(dir string, reports []metrics.Report) error {
	if len(reports) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"pr-curve.svg":      PRCurve(reports[0]),
		"trade-curve.svg":   TradeCurve(reports[0]),
		"fp-breakdown.svg":  FPBreakdown(reports[0]),
		"latency-seeds.svg": LatencySpread(reports),
		"budget-curve.svg":  BudgetCurve(reports[0]),
		"intensity.svg":     IntensityCurve(reports[0]),
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(files[n]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Reliability draws predicted probability against observed frequency.
//
// The diagonal is perfect calibration: of the transactions rated 0.3, about
// 30% should really be fraud. Points below the line mean the scorer is
// over-confident — in a payments context, blocking customers on evidence
// weaker than the number claims.
func Reliability(bins []risk.ReliabilityBin) string {
	c := New(520, 460, "Calibration: predicted vs observed fraud rate",
		"predicted probability", "observed fraud rate")

	var maxV float64 = 0.001
	for _, b := range bins {
		if b.N == 0 {
			continue
		}
		if b.Predicted > maxV {
			maxV = b.Predicted
		}
		if b.Observed > maxV {
			maxV = b.Observed
		}
	}
	maxV *= 1.15
	c.XMin, c.XMax, c.YMin, c.YMax = 0, maxV, 0, maxV
	c.Axes(5, 5)

	// Perfect calibration.
	c.Polyline([]float64{0, maxV}, []float64{0, maxV}, colRandom, 1.5, "5 4")

	xs := make([]float64, 0, len(bins))
	ys := make([]float64, 0, len(bins))
	for _, b := range bins {
		if b.N == 0 {
			continue
		}
		xs = append(xs, b.Predicted)
		ys = append(ys, b.Observed)
	}
	c.Polyline(xs, ys, colDetector, 2.2, "")
	for i := range xs {
		c.Marker(xs[i], ys[i], colPoint, "")
	}

	c.Legend([]LegendEntry{
		{Label: "observed", Colour: colDetector},
		{Label: "perfect", Colour: colRandom, Dash: "5 4"},
	})
	return c.String()
}

// BudgetCurve plots what a bigger review queue actually buys.
//
// This is the axis a risk team negotiates on. Reviewer capacity is the scarce
// resource, and the useful question is not "how good is the model" but "given
// N analysts, how much fraud can we stop" — plus the point beyond which more
// staff buy nothing, which is usually the more actionable fact.
func BudgetCurve(r metrics.Report) string {
	c := New(680, 400, "What a bigger review queue buys",
		"review budget (% of all traffic)", "share of fraud caught")

	var maxB float64
	for _, b := range r.Budgets {
		if b.Budget > maxB {
			maxB = b.Budget
		}
	}
	if maxB <= 0 {
		maxB = 0.1
	}
	c.XMin, c.XMax, c.YMin, c.YMax = 0, maxB*100, 0, 1
	c.Axes(5, 5)

	xs := make([]float64, 0, len(r.Budgets))
	ys := make([]float64, 0, len(r.Budgets))
	for _, b := range r.Budgets {
		if !b.Feasible {
			continue
		}
		xs = append(xs, b.Budget*100)
		ys = append(ys, b.Caught)
	}
	c.Polyline(xs, ys, colDetector, 2.4, "")
	for i := range xs {
		c.Marker(xs[i], ys[i], colPoint, "")
	}
	if r.DeployedOK {
		c.Marker(r.ReviewBudget*100, r.Deployed.Caught, colHardNeg,
			fmt.Sprintf("deployed: %.0f%% caught", r.Deployed.Caught*100))
	}
	c.Legend([]LegendEntry{
		{Label: "fraud caught", Colour: colDetector},
		{Label: "chosen policy", Colour: colHardNeg},
	})
	return c.String()
}

// IntensityCurve plots recall against how hard the attack was pushing.
//
// A single recall number averages over the whole episode and so overstates
// performance against an adversary who simply operates more quietly. This
// shows where the detector actually becomes useful — and it is only
// computable because the attack ramps.
func IntensityCurve(r metrics.Report) string {
	c := New(680, 400, "Recall vs attack intensity — how loud must an attack be?",
		"attack intensity", "recall")
	c.XMin, c.XMax, c.YMin, c.YMax = 0, 1, 0, 1
	c.Axes(5, 5)

	xs := make([]float64, 0, len(r.ByIntensity))
	ys := make([]float64, 0, len(r.ByIntensity))
	for _, b := range r.ByIntensity {
		if b.Fraud == 0 {
			continue
		}
		xs = append(xs, (b.Lo+b.Hi)/2)
		ys = append(ys, b.Recall)
	}
	c.Polyline(xs, ys, colDetector, 2.4, "")
	for i := range xs {
		c.Marker(xs[i], ys[i], colPoint, "")
	}
	if r.KneeFound {
		c.Polyline([]float64{r.Knee, r.Knee}, []float64{0, 1}, colHardNeg, 1.5, "4 4")
		c.Marker(r.Knee, 0.25, colHardNeg, "knee")
	}
	c.Legend([]LegendEntry{{Label: "recall", Colour: colDetector}})
	return c.String()
}

// PropagationArm is one arm of the guilt-by-association ablation.
type PropagationArm struct {
	Name         string  `json:"name"`
	AUCPR        float64 `json:"aucpr_mean"`
	MerchantFP1k float64 `json:"merchant_fp_per_1k"`
}

// Propagation draws what spreading suspicion along the graph actually did.
//
// Two bars per arm, because the story needs both: detection quality, and the
// harm done to large merchants — who are connected to everyone and therefore
// the first casualty of guilt by association. Naive propagation flags 93% of
// their legitimate traffic while collapsing detection to chance.
func Propagation(arms []PropagationArm) string {
	if len(arms) == 0 {
		return ""
	}
	c := New(700, 90+64*len(arms),
		"Does spreading suspicion along the graph help? (measured, not assumed)", "", "")
	c.L, c.B = 210, 34

	var maxA, maxF float64
	for _, a := range arms {
		if a.AUCPR > maxA {
			maxA = a.AUCPR
		}
		if a.MerchantFP1k > maxF {
			maxF = a.MerchantFP1k
		}
	}
	if maxA <= 0 {
		maxA = 1
	}
	if maxF <= 0 {
		maxF = 1
	}

	rows := len(arms) * 2
	for i, a := range arms {
		c.HBar(i*2, rows, a.AUCPR/maxA, colDetector,
			a.Name, fmt.Sprintf("AUC-PR %.3f", a.AUCPR))
		c.HBar(i*2+1, rows, a.MerchantFP1k/maxF, colHardNeg,
			"", fmt.Sprintf("%.0f merchant FP / 1k", a.MerchantFP1k))
	}
	c.Legend([]LegendEntry{
		{Label: "detection quality", Colour: colDetector},
		{Label: "harm to merchants", Colour: colHardNeg},
	})
	return c.String()
}
