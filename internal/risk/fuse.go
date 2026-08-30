// Package risk turns detector findings into a single calibrated risk score.
//
// Like internal/detect, this package MUST NOT import internal/sim,
// internal/truth or internal/chaos. Fitting a calibrator needs labels, so that
// function takes plain []float64 and []bool rather than any ground-truth type
// — the labels arrive as numbers from the metrics layer and no path back to
// the truth package is ever opened.
package risk

import (
	"math"
	"sort"

	"github.com/yug/upi-city/internal/detect"
	"github.com/yug/upi-city/internal/obs"
)

// Fusion combines several detector scores into one.
//
// ─── Why not simply take the highest score ──────────────────────────────────
//
// Taking the maximum was the first implementation, and measurement killed it:
// all three detectors together scored AUC-PR 0.062 while the cycle detector
// ALONE scored 0.078. Combining evidence was making the system worse.
//
// The reason is that max-wins is decided entirely by whichever detector is
// loudest, so the noisiest one sets the answer. Velocity fires readily on
// ordinary consumers; under max-wins each of those became the transaction's
// score outright, drowning the far more specific cycle signal.
//
// A weighted sum inverts that. One mediocre signal alone stays below the line;
// two independent signals AGREEING push it over. That is what "evidence"
// should mean, and it is why the bias term is strongly negative.
type Fusion struct {
	Bias    float64
	Weights map[string]float64
}

// DefaultFusion returns the standard weighting.
//
// The weights are HAND-SET from how specific each detector is, not fitted to
// the data. Fitting them on the same synthetic traffic they are evaluated
// against is the circularity this whole project exists to avoid, and with only
// three inputs the honest gain would be small. The comparison harness reports
// this against max-wins so the improvement is measured rather than asserted.
func DefaultFusion() Fusion {
	return Fusion{
		// Strongly negative: a lone mid-strength signal must not cross the
		// line on its own.
		Bias: -3.0,
		Weights: map[string]float64{
			// Most specific. Requires a pass-through, anomalous throughput
			// AND a timed cycle, so it is expensive to trip by accident.
			"cycle": 4.0,
			// Fires readily, including on legitimate bursts and retry storms.
			"velocity": 2.5,
			// Counterparty novelty is informative but noisy; standalone it
			// scores barely above chance.
			"fanout": 2.0,
		},
	}
}

// RiskScore is one transaction's fused risk, with its evidence intact.
type RiskScore struct {
	TxID  obs.TxID    `json:"tx"`
	Tick  obs.Tick    `json:"t"`
	Agent obs.AgentID `json:"a"`

	// Raw is the fused score before calibration: monotone in suspicion, but
	// not a probability.
	Raw float64 `json:"raw"`
	// Calibrated is Raw mapped through the fitted calibrator, so 0.7 means
	// roughly "70% of transactions scoring this high are fraudulent".
	Calibrated float64 `json:"cal"`

	// Contributions is each detector's signed push on the score. It is the
	// explainability panel's entire data source and, later, the fact input to
	// the narrative layer — one structure, three consumers, so the panel and
	// the explanation can never disagree.
	Contributions map[string]float64 `json:"contrib,omitempty"`
}

// Score fuses the findings attached to a single transaction.
func (f Fusion) Score(findings []detect.Finding) RiskScore {
	if len(findings) == 0 {
		return RiskScore{}
	}

	// Highest score per detector: two findings from the same detector are one
	// piece of evidence, not two.
	best := map[string]float64{}
	for _, fd := range findings {
		if fd.Score > best[fd.Detector] {
			best[fd.Detector] = fd.Score
		}
	}

	z := f.Bias
	contrib := make(map[string]float64, len(best))
	// Iterate detector names in sorted order, never the map directly: Go
	// randomises map iteration and floating-point addition is not
	// associative, so summing in a varying order would produce results that
	// differ in the last bits between runs and break the determinism gate.
	names := make([]string, 0, len(best))
	for n := range best {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := f.Weights[n] * best[n]
		contrib[n] = c
		z += c
	}

	r := RiskScore{
		TxID:          findings[0].TxID,
		Tick:          findings[0].Tick,
		Agent:         findings[0].Agent,
		Raw:           sigmoid(z),
		Contributions: contrib,
	}
	r.Calibrated = r.Raw
	return r
}

// MaxFusion reproduces the original loudest-detector-wins behaviour, kept so
// the comparison harness can measure the improvement instead of claiming it.
func MaxFusion() Fusion { return Fusion{Bias: math.Inf(-1)} }

// IsMax reports whether this is the max-wins strategy.
func (f Fusion) IsMax() bool { return math.IsInf(f.Bias, -1) }

// ScoreWith applies whichever strategy this Fusion represents.
func (f Fusion) ScoreWith(findings []detect.Finding) RiskScore {
	if !f.IsMax() {
		return f.Score(findings)
	}
	var r RiskScore
	if len(findings) == 0 {
		return r
	}
	contrib := make(map[string]float64, len(findings))
	for _, fd := range findings {
		if fd.Score > contrib[fd.Detector] {
			contrib[fd.Detector] = fd.Score
		}
		if fd.Score > r.Raw {
			r.Raw = fd.Score
		}
	}
	r.TxID, r.Tick, r.Agent = findings[0].TxID, findings[0].Tick, findings[0].Agent
	r.Contributions, r.Calibrated = contrib, r.Raw
	return r
}

func sigmoid(z float64) float64 {
	if z >= 0 {
		return 1 / (1 + math.Exp(-z))
	}
	e := math.Exp(z)
	return e / (1 + e)
}
