// Package explain turns a cluster of detector findings into a plain-English
// incident narrative for the audit trail.
//
// ┌───────────────────────────────────────────────────────────────────────┐
// │ THE MODEL NEVER DETECTS ANYTHING.                                     │
// │                                                                       │
// │ Detection stays entirely in rules and statistics, because those are   │
// │ measurable, reproducible, and cheap enough to run on every            │
// │ transaction. This layer runs AFTER a decision has already been made   │
// │ and only describes it.                                                │
// │                                                                       │
// │ That split is deliberate. A language model in the scoring path would  │
// │ make every number in this project unreproducible, and "the model      │
// │ said so" is not something a risk team can put in front of a regulator.│
// │ What a regulator does want is a written record of why an account was  │
// │ blocked — which is exactly this.                                      │
// └───────────────────────────────────────────────────────────────────────┘
package explain

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/yug/upi-city/internal/obs"
)

// Source records where an explanation came from, and is surfaced in the
// interface as a badge. A reader should always be able to tell whether they
// are looking at generated prose or a filled-in template.
type Source string

const (
	SourceTemplate Source = "template"
	SourceLLM      Source = "llm"
	SourceCache    Source = "cache"
)

// Facts is the structured input an explanation is built from.
//
// Deliberately numbers and short strings only — never raw transaction dumps.
// A model given a list of payments will invent plausible-looking totals; a
// model given the totals has nothing left to invent.
type Facts struct {
	IncidentID uint32   `json:"incident_id"`
	Kind       string   `json:"kind"`
	StartTick  obs.Tick `json:"start_tick"`
	DurationS  float64  `json:"duration_seconds"`

	Members      int     `json:"accounts_involved"`
	Transactions int     `json:"transactions"`
	TotalRupees  int64   `json:"total_rupees"`
	MedianRupees int64   `json:"median_rupees"`
	Intensity    float64 `json:"attack_intensity"`

	// Detection outcome.
	DetectedAfterS float64 `json:"detected_after_seconds"`
	Detected       bool    `json:"detected"`
	Blocked        int     `json:"blocked"`
	Reviewed       int     `json:"queued_for_review"`
	MissedPct      float64 `json:"percent_undetected"`

	// ─── Infrastructure incidents ──────────────────────────────────────
	//
	// A bank outage has no members and no fraudulent transactions, so the
	// fraud fields above are all zero for one. Describing it from those
	// fields produced a self-contradictory record — "no transactions
	// occurred" followed by "4,452 queued for review" — because the model
	// was handed an empty struct and wrote around it.
	//
	// An outage's real story is different: how much traffic failed, and how
	// many FALSE ALARMS the resulting retry storm caused. That last number
	// is the interesting one, since telling "our bank is broken" apart from
	// "we are being robbed" is the competency on trial.
	TxInWindow     int `json:"transactions_in_window,omitempty"`
	FailedInWindow int `json:"failed_transactions,omitempty"`
	FalseAlarms    int `json:"false_alarms_caused,omitempty"`

	// TopSignals is which detectors contributed, strongest first.
	TopSignals []Signal `json:"top_signals"`
}

// Signal is one detector's contribution to the decision.
type Signal struct {
	Name         string  `json:"name"`
	Contribution float64 `json:"contribution"`
	Note         string  `json:"note"`
}

// Explanation is the finished narrative.
type Explanation struct {
	IncidentID uint32 `json:"incident_id"`
	Headline   string `json:"headline"`
	Narrative  string `json:"narrative"`
	Action     string `json:"action"`
	Source     Source `json:"source"`
	Elapsed    string `json:"elapsed,omitempty"`
}

// Explainer produces a narrative from facts.
type Explainer interface {
	Explain(ctx context.Context, f Facts) (Explanation, error)
}

// Layer is the full pipeline: a template floor, an optional model, and a
// cache in front of both.
//
// The design rule is that the simulation and the detectors must NEVER wait on
// this. Generation happens on one worker, off the critical path, behind a
// bounded queue; when the queue is full or the model is slow the template
// result stands and the badge says so. A demo that stalls because a language
// model is thinking is a demo that fails.
type Layer struct {
	model Explainer
	cache *Cache

	queue   chan Facts
	results chan Explanation
	timeout time.Duration

	// inflight stops the same incident being queued over and over.
	//
	// The interface asks for an explanation on every frame it renders, so a
	// single unresolved incident would enqueue twenty requests a second and
	// the worker would spend its life regenerating the same paragraph. One
	// request per distinct set of facts is enough.
	mu       sync.Mutex
	inflight map[string]bool
}

// NewLayer builds the pipeline. model may be nil, in which case every
// explanation comes from the template.
func NewLayer(model Explainer, cache *Cache, timeout time.Duration) *Layer {
	l := &Layer{
		model:    model,
		cache:    cache,
		queue:    make(chan Facts, 8),
		results:  make(chan Explanation, 32),
		timeout:  timeout,
		inflight: map[string]bool{},
	}
	if model != nil {
		go l.worker()
	}
	return l
}

// Immediate returns an explanation right now, without ever blocking.
//
// Cache first, then the template. The model is only ever consulted
// asynchronously, and its result arrives later via Results.
func (l *Layer) Immediate(f Facts) Explanation {
	if l.cache != nil {
		if e, ok := l.cache.Get(f); ok {
			e.Source = SourceCache
			return e
		}
	}
	e := Template(f)
	if l.model == nil {
		return e
	}

	k := Key(f)
	l.mu.Lock()
	already := l.inflight[k]
	if !already {
		l.inflight[k] = true
	}
	l.mu.Unlock()
	if already {
		return e
	}

	select {
	case l.queue <- f:
	default:
		// Queue full: the template result stands. Dropping work is correct
		// here — falling behind must never become backpressure on the
		// simulation.
		l.mu.Lock()
		delete(l.inflight, k)
		l.mu.Unlock()
	}
	return e
}

// Results delivers model-generated explanations as they finish.
func (l *Layer) Results() <-chan Explanation { return l.results }

func (l *Layer) worker() {
	for f := range l.queue {
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
		start := time.Now()
		e, err := l.model.Explain(ctx, f)
		cancel()

		k := Key(f)
		l.mu.Lock()
		delete(l.inflight, k)
		l.mu.Unlock()

		if err != nil || !Plausible(e, f) {
			// Any failure — a timeout, a dead model, an invented number —
			// falls back silently to the template. There is no error path
			// that can surface as a broken interface.
			continue
		}
		e.Source = SourceLLM
		e.IncidentID = f.IncidentID
		e.Elapsed = time.Since(start).Round(time.Millisecond).String()
		if l.cache != nil {
			l.cache.Put(f, e)
		}
		select {
		case l.results <- e:
		default:
		}
	}
}

// TopSignalsOf returns the strongest contributions, sorted, for a stable
// prompt and a stable cache key.
func TopSignalsOf(contrib map[string]float64, n int) []Signal {
	out := make([]Signal, 0, len(contrib))
	for k, v := range contrib {
		out = append(out, Signal{Name: k, Contribution: v, Note: signalNote(k)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Contribution != out[j].Contribution {
			return out[i].Contribution > out[j].Contribution
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// signalNote gives each detector a one-line meaning, so the narrative can
// explain the evidence rather than name a variable.
func signalNote(name string) string {
	switch name {
	case "velocity":
		return "transaction rate far above this account's own established norm"
	case "fanout":
		return "sudden concentration of counterparties never seen before"
	case "cycle":
		return "funds returned to their origin through intermediaries, quickly, with little value lost"
	}
	return "anomalous activity"
}
