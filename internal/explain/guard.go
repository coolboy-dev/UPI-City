package explain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var numberRe = regexp.MustCompile(`\d[\d,]*\.?\d*`)

// Plausible rejects a generated explanation that contains numbers which were
// never in the input facts.
//
// ─── Why this exists ────────────────────────────────────────────────────────
//
// This is an audit trail. Somebody may read it while deciding whether to keep
// a customer's account frozen. A language model asked to write fluently about
// "₹4,20,000 moved by 11 accounts" will cheerfully produce "roughly ₹4.5
// lakh across a dozen accounts" — more readable, and wrong. Small
// hallucinated figures in a compliance record are worse than clumsy prose,
// because they are quotable.
//
// So every number in the output must trace back to a number in the input.
// The check is mechanical and deliberately strict: a model that cannot follow
// the constraint gets discarded and the template stands. Constraining the
// model is cheaper and far more reliable than trusting it.
//
// It earns its place. Measured over six incidents, qwen3:1.7b passed once and
// qwen3.5:9b passed every time. One rejected 1.7b output, against facts
// stating 2,481 transactions totalling ₹40,30,400 over 600 seconds, read:
// "24,810 transactions ... 86,37,301 rupees ... 1 minute of activity" —
// fluent, confident, and wrong by a factor of ten in three places.
//
// ─── What this check does NOT do ────────────────────────────────────────────
//
// It constrains ARITHMETIC, not SEMANTICS. A model that writes "1,786
// accounts were blocked" when 1,786 TRANSACTIONS were blocked passes cleanly,
// because the figure really is in the facts. Catching that would need the
// output parsed against the meaning of each field, which is a much larger
// piece of work.
//
// The 5% tolerance is likewise a deliberate trade. It exists so a writer can
// say "about ₹86.4 lakh" for 8,637,301, and the cost is that a transposed
// digit inside that band also survives. Tightening it would reject the human
// phrasing that makes these records readable at all.
func Plausible(e Explanation, f Facts) bool {
	if strings.TrimSpace(e.Headline) == "" || strings.TrimSpace(e.Narrative) == "" {
		return false
	}
	// Guard against a model that ignores the brief and writes an essay.
	if len(e.Narrative) > 1200 || len(e.Headline) > 200 {
		return false
	}

	allowed := allowedNumbers(f)
	text := e.Headline + " " + e.Narrative + " " + e.Action

	for _, m := range numberRe.FindAllString(text, -1) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(m, ",", ""), 64)
		if err != nil {
			continue
		}
		if !nearAny(v, allowed) {
			return false
		}
	}
	return true
}

// allowedNumbers is every figure the model was given, plus the handful of
// derived forms a sensible writer would use.
func allowedNumbers(f Facts) []float64 {
	vals := []float64{
		float64(f.IncidentID), float64(f.StartTick), f.DurationS,
		float64(f.Members), float64(f.Transactions),
		float64(f.TotalRupees), float64(f.MedianRupees),
		f.Intensity, f.Intensity * 100,
		f.DetectedAfterS, float64(f.Blocked), float64(f.Reviewed),
		f.MissedPct,
		float64(f.TxInWindow), float64(f.FailedInWindow), float64(f.FalseAlarms),
		// Small integers are ordinary prose ("two or three hops") rather
		// than claims about the data.
		0, 1, 2, 3,
	}
	// Lakh and crore renderings of the money figures, since those are the
	// natural way to write them in this context.
	for _, r := range []int64{f.TotalRupees, f.MedianRupees} {
		vals = append(vals,
			float64(r)/1000, float64(r)/100000, float64(r)/10000000)
	}
	for _, s := range f.TopSignals {
		vals = append(vals, s.Contribution)
	}
	return vals
}

// nearAny allows the rounding a human writer would do — "₹4.2 lakh" for
// 420,431 — without allowing invention.
func nearAny(v float64, allowed []float64) bool {
	for _, a := range allowed {
		if a == 0 && v == 0 {
			return true
		}
		if a == 0 {
			continue
		}
		diff := v - a
		if diff < 0 {
			diff = -diff
		}
		if diff/abs(a) <= 0.05 || diff < 0.51 {
			return true
		}
	}
	return false
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// Describe renders the facts as the compact block handed to the model.
//
// Structured and pre-computed, so the model's only job is to write English.
// Handing it raw transactions would make arithmetic part of the task, and
// arithmetic is the thing it is worst at.
// The two incident kinds get DISJOINT fact sets, and that separation is
// load-bearing rather than tidiness.
//
// An outage has no members and no fraudulent transactions, so emitting the
// fraud fields alongside the outage fields handed the model
// "accounts_involved: 0, transactions: 0" right next to
// "transactions_in_window: 33301" — and it dutifully wrote a record stating
// both that no transactions occurred and that 33,301 were queued for review.
// Contradictory input produces contradictory output, and the fix belongs
// here rather than in the prompt.
func Describe(f Facts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "incident_id: %d\n", f.IncidentID)
	fmt.Fprintf(&b, "kind: %s\n", f.Kind)
	fmt.Fprintf(&b, "start_tick: %d\n", f.StartTick)
	fmt.Fprintf(&b, "duration_seconds: %.0f\n", f.DurationS)

	if f.Kind == "bank-outage" {
		fmt.Fprintf(&b, "transactions_in_window: %d\n", f.TxInWindow)
		fmt.Fprintf(&b, "failed_to_settle: %d\n", f.FailedInWindow)
		fmt.Fprintf(&b, "legitimate_transactions_wrongly_scored: %d\n", f.FalseAlarms)
		fmt.Fprint(&b, "note: a payment provider failed. This is NOT fraud. "+
			"Customer retries raised transaction rates, which resembles an attack.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "accounts_involved: %d\n", f.Members)
	fmt.Fprintf(&b, "transactions: %d\n", f.Transactions)
	fmt.Fprintf(&b, "total_rupees: %d\n", f.TotalRupees)
	fmt.Fprintf(&b, "median_rupees: %d\n", f.MedianRupees)
	fmt.Fprintf(&b, "detected: %v\n", f.Detected)
	if f.Detected {
		fmt.Fprintf(&b, "detected_after_seconds: %.1f\n", f.DetectedAfterS)
	}
	fmt.Fprintf(&b, "blocked: %d\n", f.Blocked)
	fmt.Fprintf(&b, "queued_for_review: %d\n", f.Reviewed)
	fmt.Fprintf(&b, "percent_undetected: %.0f\n", f.MissedPct)
	for _, s := range f.TopSignals {
		fmt.Fprintf(&b, "signal: %s (%s)\n", s.Name, s.Note)
	}
	return b.String()
}
