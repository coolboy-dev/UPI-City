package metrics

import (
	"fmt"
	"strings"

	"github.com/yug/upi-city/internal/truth"
)

// Report is everything one run has to say about detector performance.
type Report struct {
	Seed      uint64   `json:"seed"`
	Ticks     uint64   `json:"ticks"`
	Agents    int      `json:"agents"`
	Scenario  string   `json:"scenario"`
	Detectors []string `json:"detectors"`

	Transactions int     `json:"transactions"`
	Fraudulent   int     `json:"fraudulent"`
	Prevalence   float64 `json:"prevalence"`

	AUCPR float64 `json:"aucpr"`

	// Operating points chosen two different ways, because "best F1" is a
	// statistician's answer and "most recall within a review budget" is the
	// one an actual risk team gives.
	BestF1       Operating `json:"best_f1"`
	AtBudget1pct Operating `json:"at_1pct_review_budget"`

	// Decision layer: what a policy actually does, rather than what a score
	// ranks.
	Deployed     DecisionPoint     `json:"deployed_policy"`
	DeployedOK   bool              `json:"deployed_policy_feasible"`
	Budgets      []BudgetCurve     `json:"budget_curve"`
	ByIntensity  []IntensityBucket `json:"recall_by_intensity"`
	Knee         float64           `json:"detection_knee"`
	KneeFound    bool              `json:"detection_knee_found"`
	MinBlockPrec float64           `json:"min_block_precision"`
	ReviewBudget float64           `json:"review_budget"`

	// Money. Counts compare detectors; rupees decide whether to deploy one.
	Economics Economics     `json:"economics"`
	CostSweep []Sensitivity `json:"cost_sensitivity"`

	Latency  LatencyReport `json:"latency"`
	FPByArch []ArchetypeFP `json:"fp_by_archetype"`

	Baselines   []Baseline         `json:"baselines"`
	Permutation PermutationControl `json:"permutation_control"`

	Curve []Operating `json:"curve"`
}

// Evaluate produces the full report for one run.
func Evaluate(rows Rows, incidents []truth.Incident, seed uint64, msPerTick uint64) Report {
	taus := DefaultTaus()
	ops := Sweep(rows, taus)

	r := Report{
		Seed:         seed,
		Transactions: len(rows),
		Fraudulent:   rows.Positives(),
		Prevalence:   rows.Prevalence(),
		AUCPR:        AUCPR(ops),
		BestF1:       BestF1(ops),
		AtBudget1pct: AtReviewBudget(ops, 0.01),
		Curve:        ops,
		Baselines:    Baselines(rows, taus, seed),
		Permutation:  Permute(rows, taus, 5, seed),
	}
	// The decision layer. A 2% review queue and a 90% block-precision floor
	// are the defaults a payments risk team would recognise: humans can only
	// look at so much, and blocking a real customer is expensive enough that
	// most of what you stop had better be fraud.
	r.ReviewBudget, r.MinBlockPrec = 0.02, 0.90
	pts := SweepDecisions(rows, taus)
	r.Deployed, r.DeployedOK = BestUnderBudget(pts, r.ReviewBudget, r.MinBlockPrec)
	r.Budgets = Budgets(pts, DefaultBudgets(), r.MinBlockPrec)

	// The policy that maximises MONEY, which is not the same policy that
	// maximises F1 or recall.
	cost := DefaultCostModel()
	r.Economics = cost.BestByNet(rows, taus)
	r.CostSweep = SweepFalseBlockCost(rows, cost,
		[]float64{100, 300, 900, 2500, 7000}, taus)

	r.ByIntensity = RecallByIntensity(rows, r.Deployed.Policy.TauReview, 10)
	r.Knee, r.KneeFound = DetectionKnee(r.ByIntensity, 0.25)

	r.Latency = Latency(rows, incidents, r.BestF1.Tau, msPerTick)
	r.FPByArch = FalsePositiveBreakdown(rows, r.BestF1.Tau)
	return r
}

// String renders the report as the terminal summary.
func (r Report) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "═══ detection performance ═══════════════════════════════════\n")
	fmt.Fprintf(&b, "transactions   %d  (%d fraudulent, prevalence %.2f%%)\n",
		r.Transactions, r.Fraudulent, r.Prevalence*100)
	fmt.Fprintf(&b, "AUC-PR         %.3f\n\n", r.AUCPR)

	fmt.Fprintf(&b, "%-22s %6s %9s %7s %7s %9s %11s\n",
		"operating point", "tau", "precision", "recall", "flag%", "FP/1k legit", "FP count")
	row := func(name string, o Operating) {
		fmt.Fprintf(&b, "%-22s %6.2f %9.3f %7.3f %7.2f %9.2f %11d\n",
			name, o.Tau, o.Precision, o.Recall, o.FlagRate*100, o.FPPer1kLegit, o.FP)
	}
	row("best F1", r.BestF1)
	row("1% review budget", r.AtBudget1pct)

	// The trade, stated in one line, because a curve without a chosen point
	// is an evasion.
	fmt.Fprintf(&b, "\nthe trade: at tau=%.2f we catch %.0f%% of fraudulent transactions\n"+
		"           and wrongly flag %.1f legitimate ones per thousand (%d in total).\n",
		r.BestF1.Tau, r.BestF1.Recall*100, r.BestF1.FPPer1kLegit, r.BestF1.FP)

	// ── The decision layer ─────────────────────────────────────────────
	fmt.Fprintf(&b, "\n─── allow / review / block ──────────────────────────────────\n")
	if !r.DeployedOK {
		fmt.Fprintf(&b, "NO DEPLOYABLE POLICY at a %.0f%% review budget with block precision >= %.0f%%.\n",
			r.ReviewBudget*100, r.MinBlockPrec*100)
		fmt.Fprintf(&b, "That is a result, not an error: at this operating quality the detector\n"+
			"cannot block safely without either a bigger review queue or a lower bar.\n")
	} else {
		d := r.Deployed
		fmt.Fprintf(&b, "policy            review at tau>=%.2f, block at tau>=%.2f\n",
			d.Policy.TauReview, d.Policy.TauBlock)
		fmt.Fprintf(&b, "%-18s %10s %12s %12s\n", "action", "volume", "of which fraud", "precision")
		fmt.Fprintf(&b, "%-18s %10d %12d %11.1f%%\n", "block", d.Blocked, d.FraudBlocked, d.BlockPrecision*100)
		fmt.Fprintf(&b, "%-18s %10d %12d %11.1f%%\n", "review", d.Reviewed, d.FraudReviewed, d.ReviewPrecision*100)
		fmt.Fprintf(&b, "%-18s %10d %12d %11s\n", "allow", d.Allowed, d.FraudAllowed, "—")
		fmt.Fprintf(&b, "\ncustomer harm     %.2f legitimate payments blocked per 1,000 legitimate\n",
			d.FalseBlockPer1k)

		// The sentence a risk team actually negotiates over.
		fmt.Fprintf(&b, "\n>>> at a %.0f%% review budget, we catch %.0f%% of fraudulent transactions,\n"+
			">>> blocking %.0f%% of them outright at %.0f%% precision. %.0f%% goes through undetected.\n",
			r.ReviewBudget*100, d.Caught*100,
			pctOf(d.FraudBlocked, d.FraudBlocked+d.FraudReviewed+d.FraudAllowed),
			d.BlockPrecision*100, d.Missed*100)
	}

	fmt.Fprintf(&b, "\nwhat a bigger review queue buys (block precision floor %.0f%%)\n", r.MinBlockPrec*100)
	fmt.Fprintf(&b, "%-14s %10s %14s %16s\n", "review budget", "caught", "block precision", "false blocks/1k")
	for _, c := range r.Budgets {
		if !c.Feasible {
			fmt.Fprintf(&b, "%-14.2f%% %10s %14s %16s\n", c.Budget*100, "—", "no safe policy", "—")
			continue
		}
		fmt.Fprintf(&b, "%-13.2f%% %9.1f%% %13.1f%% %16.2f\n",
			c.Budget*100, c.Caught*100, c.BlockPrecision*100, c.FalseBlockPer1k)
	}

	// ── Money ──────────────────────────────────────────────────────────
	e := r.Economics
	fmt.Fprintf(&b, "\n─── expected loss, in rupees ────────────────────────────────\n")
	fmt.Fprintf(&b, "processed         ₹%s\n", crore(e.ProcessedRupees))
	fmt.Fprintf(&b, "fraud attempted   ₹%s  (%.3f%% of volume)\n",
		crore(e.FraudRupees), e.LossRateBefore*100)
	fmt.Fprintf(&b, "\npolicy maximising NET VALUE: review>=%.2f, block>=%.2f\n",
		e.Policy.TauReview, e.Policy.TauBlock)
	fmt.Fprintf(&b, "%-24s %16s\n", "line", "rupees")
	fmt.Fprintf(&b, "%-24s %16s\n", "fraud value saved", "+"+crore(int64(e.Saved)))
	fmt.Fprintf(&b, "%-24s %16s   (%d legitimate payments stopped)\n",
		"cost of false blocks", "-"+crore(int64(e.FalseBlockCost)), e.FalseBlocks)
	fmt.Fprintf(&b, "%-24s %16s   (%d reviewed)\n",
		"cost of review queue", "-"+crore(int64(e.ReviewCost)), e.Reviews)
	fmt.Fprintf(&b, "%-24s %16s\n", "NET", crore(int64(e.Net)))
	fmt.Fprintf(&b, "\n>>> ₹%.0f net per crore processed. Fraud losses fall from\n"+
		">>> %.3f%% of volume to %.3f%%.\n",
		e.NetPerCrore, e.LossRateBefore*100, e.LossRateAfter*100)

	fmt.Fprintf(&b, "\nsensitivity — the cost of wrongly blocking a customer is the\n"+
		"least certain assumption here, so the policy is re-optimised across it\n\n")
	fmt.Fprintf(&b, "%-16s %10s %10s %14s %12s\n",
		"false block ₹", "review>=", "block>=", "net/crore ₹", "review rate")
	for _, sv := range r.CostSweep {
		fmt.Fprintf(&b, "%-16.0f %10.2f %10.2f %14.0f %11.2f%%\n",
			sv.FalseBlockRupees, sv.TauReview, sv.TauBlock, sv.NetPerCrore, sv.ReviewRate*100)
	}

	// ── Sensitivity to attack strength ─────────────────────────────────
	fmt.Fprintf(&b, "\n─── recall vs attack intensity ──────────────────────────────\n")
	fmt.Fprintf(&b, "how loud must an attack be before this detector notices it?\n\n")
	fmt.Fprintf(&b, "%-14s %10s %10s %8s\n", "intensity", "fraud tx", "caught", "recall")
	for _, ib := range r.ByIntensity {
		if ib.Fraud == 0 {
			continue
		}
		bar := strings.Repeat("█", int(ib.Recall*20))
		fmt.Fprintf(&b, "%.1f - %.1f     %10d %10d %7.1f%%  %s\n",
			ib.Lo, ib.Hi, ib.Fraud, ib.Caught, ib.Recall*100, bar)
	}
	if r.KneeFound {
		fmt.Fprintf(&b, "\nknee: recall first clears 25%% at intensity %.1f — quieter attacks than\n"+
			"that are effectively invisible to this detector.\n", r.Knee)
	} else {
		fmt.Fprintf(&b, "\nno knee: recall never clears 25%% at any attack strength tested.\n")
	}

	fmt.Fprintf(&b, "\n─── baselines ───────────────────────────────────────────────\n")
	fmt.Fprintf(&b, "%-16s %8s   %s\n", "scorer", "AUC-PR", "why it is here")
	fmt.Fprintf(&b, "%-16s %8.3f   %s\n", "THIS DETECTOR", r.AUCPR, "the thing being measured")
	for _, bl := range r.Baselines {
		fmt.Fprintf(&b, "%-16s %8.3f   %s\n", bl.Name, bl.AUCPR, bl.Why)
	}

	fmt.Fprintf(&b, "\n─── detection latency ───────────────────────────────────────\n")
	l := r.Latency
	fmt.Fprintf(&b, "incidents      %d total, %d never detected at tau=%.2f\n",
		l.Total, l.NeverDetected, l.Tau)
	if l.Total > l.NeverDetected {
		fmt.Fprintf(&b, "median         %d ticks (%.1fs) — computed over DETECTED incidents only\n",
			l.MedianTicks, l.MedianSeconds)
	}
	for _, il := range l.Incidents {
		if il.Detected {
			fmt.Fprintf(&b, "  #%d %-12s detected in %d ticks (%.1fs), %d/%d of its transactions flagged\n",
				il.IncidentID, il.Kind, il.Ticks, il.Seconds, il.FlaggedTxOfIncident, il.TotalTxOfIncident)
		} else {
			fmt.Fprintf(&b, "  #%d %-12s NEVER DETECTED (%d transactions went unflagged)\n",
				il.IncidentID, il.Kind, il.TotalTxOfIncident)
		}
	}

	fmt.Fprintf(&b, "\n─── false positives, by who suffered them ───────────────────\n")
	fmt.Fprintf(&b, "%-16s %6s %10s %12s %s\n", "archetype", "FP", "legit tx", "FP/1k", "")
	for _, a := range r.FPByArch {
		tag := ""
		if a.HardNeg {
			tag = "← hard negative"
		}
		fmt.Fprintf(&b, "%-16s %6d %10d %12.2f %s\n", a.Archetype, a.FP, a.LegitTx, a.FPPer1kThis, tag)
	}

	fmt.Fprintf(&b, "\n─── negative control (label permutation) ────────────────────\n")
	p := r.Permutation
	fmt.Fprintf(&b, "real AUC-PR %.3f vs permuted mean %.3f (max %.3f), prevalence %.3f\n",
		p.RealAUCPR, p.MeanAUCPR, p.MaxAUCPR, p.Prevalence)
	status := "PASS"
	if !p.Passed {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "%s — validates the METRIC: shuffling labels collapses performance to\n"+
		"       chance, so the figure above genuinely depends on ground truth.\n"+
		"       It does NOT prove the detectors never saw labels; that is enforced\n"+
		"       separately by the dependency test in internal/detect.\n", status)

	return b.String()
}

// crore renders rupees in Indian units, since the figures span several
// orders of magnitude.
func crore(v int64) string {
	switch {
	case v >= 10_000_000 || v <= -10_000_000:
		return fmt.Sprintf("%.2f cr", float64(v)/1e7)
	case v >= 100_000 || v <= -100_000:
		return fmt.Sprintf("%.2f L", float64(v)/1e5)
	default:
		return fmt.Sprintf("%d", v)
	}
}

func pctOf(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}
