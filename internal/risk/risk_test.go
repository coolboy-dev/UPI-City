package risk

import (
	"math"
	"math/rand/v2"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/yug/upi-city/internal/detect"
)

// TestRiskCannotSeeGroundTruth extends the firewall to this package.
//
// Calibration is the one place in the pipeline that legitimately needs to know
// which transactions were fraudulent, which makes it exactly where a
// convenient import of the truth package would appear. Labels reach it as a
// plain []bool from the metrics layer and no path back is opened.
func TestRiskCannotSeeGroundTruth(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		switch strings.TrimSpace(p) {
		case "github.com/yug/upi-city/internal/truth":
			t.Error("GROUND TRUTH LEAK: internal/risk depends on internal/truth")
		case "github.com/yug/upi-city/internal/sim":
			t.Error("LEAK RISK: internal/risk depends on internal/sim")
		}
	}
}

// TestAgreementBeatsLoudness is the reason weighted fusion exists.
//
// Measurement showed max-wins scoring WORSE with three detectors than with the
// cycle detector alone: the noisiest detector decided every answer. Under a
// weighted sum, two independent moderate signals agreeing must outrank one
// loud signal on its own — which is what "corroborating evidence" means.
func TestAgreementBeatsLoudness(t *testing.T) {
	f := DefaultFusion()

	oneLoud := f.Score([]detect.Finding{
		{Detector: "velocity", Score: 0.80},
	})
	twoAgreeing := f.Score([]detect.Finding{
		{Detector: "velocity", Score: 0.45},
		{Detector: "cycle", Score: 0.45},
	})

	if twoAgreeing.Raw <= oneLoud.Raw {
		t.Errorf("two agreeing signals (%.3f) did not outrank one loud signal (%.3f); "+
			"fusion has collapsed back to loudest-wins", twoAgreeing.Raw, oneLoud.Raw)
	}

	// And max-wins must behave the other way, or the comparison is not
	// measuring what it claims.
	m := MaxFusion()
	if m.ScoreWith([]detect.Finding{{Detector: "velocity", Score: 0.80}}).Raw <=
		m.ScoreWith([]detect.Finding{
			{Detector: "velocity", Score: 0.45}, {Detector: "cycle", Score: 0.45}}).Raw {
		t.Error("max-wins should favour the single loud signal; the baseline is not a baseline")
	}
}

// TestContributionsExplainTheScore checks the evidence adds up to the answer.
//
// The contributions feed the explainability panel and, later, the narrative
// layer. If they did not reconstruct the score, the interface would be showing
// a rationale that had nothing to do with the decision.
func TestContributionsExplainTheScore(t *testing.T) {
	f := DefaultFusion()
	rs := f.Score([]detect.Finding{
		{Detector: "velocity", Score: 0.6},
		{Detector: "cycle", Score: 0.4},
		{Detector: "fanout", Score: 0.2},
	})

	z := f.Bias
	for _, c := range rs.Contributions {
		z += c
	}
	want := sigmoid(z)
	if math.Abs(rs.Raw-want) > 1e-9 {
		t.Errorf("contributions sum to %.6f but the score is %.6f", want, rs.Raw)
	}
}

// TestFusionIsOrderIndependent guards determinism.
//
// Findings arrive in detector order, but floating-point addition is not
// associative, so summing contributions in a varying order would change the
// result in its last bits — enough to break the stream hash and every
// comparison built on identical traffic.
func TestFusionIsOrderIndependent(t *testing.T) {
	f := DefaultFusion()
	a := []detect.Finding{
		{Detector: "velocity", Score: 0.31}, {Detector: "cycle", Score: 0.72},
		{Detector: "fanout", Score: 0.19},
	}
	b := []detect.Finding{
		{Detector: "fanout", Score: 0.19}, {Detector: "velocity", Score: 0.31},
		{Detector: "cycle", Score: 0.72},
	}
	if f.Score(a).Raw != f.Score(b).Raw {
		t.Errorf("fusion depends on finding order: %.17g vs %.17g", f.Score(a).Raw, f.Score(b).Raw)
	}
}

// TestIsotonicIsMonotone checks the core property. A calibrator that inverts
// anywhere would rank a less suspicious transaction above a more suspicious
// one, silently corrupting every threshold downstream.
func TestIsotonicIsMonotone(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 5))
	n := 20000
	raw := make([]float64, n)
	fraud := make([]bool, n)
	for i := range raw {
		raw[i] = rng.Float64()
		fraud[i] = rng.Float64() < raw[i]*0.4 // higher score, more fraud
	}

	c := FitIsotonic(raw, fraud, 100)
	xs := []float64{0, 0.1, 0.25, 0.4, 0.55, 0.7, 0.85, 1.0}
	prev := -1.0
	for _, x := range xs {
		y := c.Apply(x)
		if y < prev-1e-9 {
			t.Fatalf("calibrator is not monotone: dropped to %.4f at raw=%.2f", y, x)
		}
		prev = y
	}
}

// TestCalibrationNeedsAHeldOutSplit demonstrates WHY the split exists.
//
// A calibrator fitted and scored on the same data will look excellent on a
// reliability diagram regardless of how good the underlying scores are,
// because it has memorised that data's base rates. This asserts that
// in-sample calibration error is at least as good as out-of-sample — which is
// exactly why an in-sample figure would be worthless as evidence.
func TestCalibrationNeedsAHeldOutSplit(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	gen := func(n int) ([]float64, []bool) {
		raw := make([]float64, n)
		fraud := make([]bool, n)
		for i := range raw {
			raw[i] = rng.Float64()
			fraud[i] = rng.Float64() < raw[i]*0.3
		}
		return raw, fraud
	}

	fitRaw, fitFraud := gen(8000)
	testRaw, testFraud := gen(8000)

	c := FitIsotonic(fitRaw, fitFraud, 100)

	apply := func(raw []float64) []float64 {
		out := make([]float64, len(raw))
		for i, r := range raw {
			out[i] = c.Apply(r)
		}
		return out
	}
	inSample := ECE(Reliability(apply(fitRaw), fitFraud, 10))
	outSample := ECE(Reliability(apply(testRaw), testFraud, 10))

	if inSample > outSample+0.02 {
		t.Errorf("in-sample ECE (%.4f) was worse than out-of-sample (%.4f); "+
			"the fitting procedure is not doing what it claims", inSample, outSample)
	}
	if outSample > 0.05 {
		t.Errorf("out-of-sample ECE %.4f exceeds the 0.05 gate", outSample)
	}
}

// TestCalibrationDoesNotChangeRanking pins a property that is easy to
// misunderstand and easy to overclaim.
//
// Isotonic regression is monotone, so it cannot reorder transactions and
// therefore CANNOT improve AUC-PR, precision or recall at a matched flag
// rate. Its entire value is interpretive: turning "more suspicious than 0.6"
// into "about seven in ten of these are fraud", which is what a review budget
// or an expected-loss calculation needs. Claiming calibration improved
// detection would be a straightforward error.
func TestCalibrationDoesNotChangeRanking(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 22))
	n := 5000
	raw := make([]float64, n)
	fraud := make([]bool, n)
	for i := range raw {
		raw[i] = rng.Float64()
		fraud[i] = rng.Float64() < raw[i]*0.5
	}
	c := FitIsotonic(raw, fraud, 100)

	type pair struct{ raw, cal float64 }
	ps := make([]pair, n)
	for i := range raw {
		ps[i] = pair{raw[i], c.Apply(raw[i])}
	}
	sort.Slice(ps, func(a, b int) bool { return ps[a].raw < ps[b].raw })
	for i := 1; i < len(ps); i++ {
		if ps[i].cal < ps[i-1].cal-1e-9 {
			t.Fatalf("calibration reordered two transactions: raw %.4f→%.4f gave cal %.4f→%.4f",
				ps[i-1].raw, ps[i].raw, ps[i-1].cal, ps[i].cal)
		}
	}
}
