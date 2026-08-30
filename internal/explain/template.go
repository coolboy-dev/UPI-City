package explain

import (
	"fmt"
	"strings"
)

// Template builds an explanation without any model at all.
//
// This is the floor beneath the whole layer, and it is deliberately good
// rather than a stub. If the model is missing, slow, unreachable or talking
// nonsense, the audit trail still gets a correct and readable record — the
// only thing lost is fluency. An explanation layer whose failure mode is a
// blank field would be worse than having no explanation layer.
func Template(f Facts) Explanation {
	e := Explanation{IncidentID: f.IncidentID, Source: SourceTemplate}

	switch f.Kind {
	case "bank-outage":
		e.Headline = fmt.Sprintf("Payment infrastructure degraded — %d failures, %d false alarms",
			f.FailedInWindow, f.FalseAlarms)
		e.Narrative = fmt.Sprintf(
			"A payment service provider began failing at tick %d and stayed degraded for %.0f seconds. "+
				"%d of %d transactions in that window failed to settle. Retries from legitimate "+
				"customers pushed transaction rates up network-wide, which looks very much like a "+
				"velocity attack and is not one: %d legitimate transactions were scored as suspicious "+
				"during the outage.",
			f.StartTick, f.DurationS, f.FailedInWindow, f.TxInWindow, f.FalseAlarms)
		e.Action = "Confirm the outage with the PSP before acting on any velocity alerts raised in this " +
			"window. They are almost certainly retry traffic, and treating them as fraud would freeze " +
			"customers for a fault that is not theirs."
		return e
	}

	e.Headline = fmt.Sprintf("Suspected laundering ring — %d accounts, ₹%s moved",
		f.Members, comma(f.TotalRupees))

	var b strings.Builder
	fmt.Fprintf(&b, "%d accounts moved ₹%s across %d transactions (median ₹%s) starting at tick %d, "+
		"over roughly %.0f seconds.",
		f.Members, comma(f.TotalRupees), f.Transactions, comma(f.MedianRupees), f.StartTick, f.DurationS)

	if len(f.TopSignals) > 0 {
		fmt.Fprintf(&b, " The strongest evidence was %s: %s.",
			f.TopSignals[0].Name, f.TopSignals[0].Note)
		if len(f.TopSignals) > 1 {
			names := make([]string, 0, len(f.TopSignals)-1)
			for _, s := range f.TopSignals[1:] {
				names = append(names, s.Name)
			}
			fmt.Fprintf(&b, " Corroborated by %s.", strings.Join(names, " and "))
		}
	}

	if f.Detected {
		fmt.Fprintf(&b, " First flagged %.1f seconds after the activity began.", f.DetectedAfterS)
	} else {
		b.WriteString(" This activity was NOT flagged by the detection layer.")
	}
	if f.MissedPct > 0 {
		fmt.Fprintf(&b, " %.0f%% of the ring's transactions went through undetected.", f.MissedPct)
	}
	e.Narrative = b.String()

	switch {
	case f.Blocked > 0:
		e.Action = fmt.Sprintf("%d transactions were blocked and %d queued for review. "+
			"Review the full member set before releasing any holds.", f.Blocked, f.Reviewed)
	case f.Reviewed > 0:
		e.Action = fmt.Sprintf("%d transactions are queued for review; none were blocked. "+
			"Prioritise the accounts with the highest inbound concentration.", f.Reviewed)
	default:
		e.Action = "No automated action was taken. Investigate manually and consider whether the " +
			"review threshold is set too high for this attack profile."
	}
	return e
}

func comma(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Indian grouping: last three digits, then pairs.
	head, tail := s[:len(s)-3], s[len(s)-3:]
	var parts []string
	for len(head) > 2 {
		parts = append([]string{head[len(head)-2:]}, parts...)
		head = head[:len(head)-2]
	}
	if head != "" {
		parts = append([]string{head}, parts...)
	}
	return strings.Join(parts, ",") + "," + tail
}
