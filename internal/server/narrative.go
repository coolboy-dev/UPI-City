package server

import (
	"time"

	"github.com/yug/upi-city/internal/explain"
	"github.com/yug/upi-city/internal/truth"
)

// narrativeCooldown is the minimum gap between generation attempts for one
// incident.
//
// A running incident grows every tick, so its facts — and therefore its cache
// key — change constantly. Left alone, every frame would look like a fresh
// request and the worker would never finish one before the next arrived.
const narrativeCooldown = 12 * time.Second

// narrative returns the best available explanation for a live incident.
//
// ─── Why this is not just a cache lookup ────────────────────────────────────
//
// The fact-keyed cache is exactly right for PRE-GENERATED explanations, where
// a seed replays identically and the numbers are final. It is exactly wrong
// for an incident still in progress: the totals climb every second, so each
// frame produces a different key, misses, and re-queues. The observed effect
// was a narrative that stayed on the template forever while the model
// endlessly wrote about totals that were already stale — ₹7 lakh, then ₹26
// lakh, then ₹38 lakh, none of them ever displayed.
//
// So live display is keyed by INCIDENT, not by facts. The most recent
// finished narrative for an incident is shown until a newer one replaces it,
// and generation is attempted on a cooldown rather than on every frame. The
// template covers the gap, which is what it is for.
func (s *Server) narrative(inc truth.Incident) explain.Explanation {
	if s.explain == nil {
		return explain.Explanation{}
	}
	facts := s.factsFor(inc)

	s.mu.Lock()
	latest, have := s.narratives[inc.ID]
	last := s.narrativeAt[inc.ID]
	due := time.Since(last) >= narrativeCooldown
	if due {
		s.narrativeAt[inc.ID] = time.Now()
	}
	s.mu.Unlock()

	// Nothing worth narrating yet.
	//
	// An incident opens the moment chaos is injected, before any of its
	// transactions have settled. Generating then produced the genuinely
	// useless record "Fraud ring, 24 accounts, zero transactions" — true when
	// written, worthless a second later, and it passed the number guard
	// because zero was indeed one of the facts. Waiting for the incident to
	// actually do something costs nothing: the template covers the gap.
	if facts.Transactions < 50 && facts.TxInWindow < 200 {
		if have {
			return latest
		}
		return explain.Template(facts)
	}

	if due {
		// Fire and forget: this queues generation and returns the template
		// immediately. It never blocks.
		tpl := s.explain.Immediate(facts)
		if !have {
			return tpl
		}
	}
	if have {
		return latest
	}
	return explain.Template(facts)
}

// collectNarratives drains finished explanations into the live map.
func (s *Server) collectNarratives() {
	if s.explain == nil {
		return
	}
	for e := range s.explain.Results() {
		s.mu.Lock()
		s.narratives[truth.IncidentID(e.IncidentID)] = e
		s.mu.Unlock()
	}
}
