package risk

import "sort"

// Calibrator maps a raw score to an estimated probability of fraud.
//
// Without calibration a score of 0.7 means only "more suspicious than 0.6".
// With it, 0.7 means roughly "seven in ten transactions scoring this high are
// fraudulent" — which is the difference between a ranking and a number a risk
// team can set policy against. Review budgets, block thresholds and expected
// loss all need a probability, not an ordinal.
type Calibrator struct {
	// Breakpoints of a monotone step function, ascending in X.
	X []float64 `json:"x"`
	Y []float64 `json:"y"`

	// FitSeeds and EvalSeeds record the split this calibrator was built
	// under, so a report can never quietly present in-sample calibration as
	// if it had been validated.
	FitSeeds  []uint64 `json:"fit_seeds"`
	EvalSeeds []uint64 `json:"eval_seeds"`
}

// FitIsotonic fits a monotone mapping from raw scores to fraud probability by
// pool-adjacent-violators.
//
// Isotonic rather than a parametric fit because it assumes only that higher
// scores mean more fraud, which is the one thing the detectors genuinely
// guarantee. A logistic fit would additionally assume a shape the scores have
// no reason to follow.
//
// ┌──────────────────────────────────────────────────────────────────────┐
// │ THIS MUST BE FIT ON DIFFERENT DATA THAN IT IS REPORTED ON.           │
// │                                                                      │
// │ A calibrator fitted and evaluated on the same run will look          │
// │ near-perfect on a reliability diagram no matter how bad the underlying│
// │ scores are — it has memorised that run's base rates. Fitting on one   │
// │ set of seeds and reporting on another is the entire reason the        │
// │ resulting diagram means anything. The split is recorded on the        │
// │ struct so it travels with the numbers.                               │
// └──────────────────────────────────────────────────────────────────────┘
func FitIsotonic(raw []float64, fraud []bool, minBin int) *Calibrator {
	n := len(raw)
	if n == 0 || n != len(fraud) {
		return &Calibrator{X: []float64{0, 1}, Y: []float64{0, 1}}
	}
	if minBin < 1 {
		minBin = 1
	}

	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return raw[idx[a]] < raw[idx[b]] })

	// Blocks of (weight, mean label), initially one point each.
	xs := make([]float64, 0, n)
	ws := make([]float64, 0, n)
	ys := make([]float64, 0, n)
	for _, i := range idx {
		v := 0.0
		if fraud[i] {
			v = 1
		}
		xs = append(xs, raw[i])
		ws = append(ws, 1)
		ys = append(ys, v)
	}

	// Pool adjacent violators: merge any block whose mean is below its
	// predecessor's, until the sequence is non-decreasing.
	k := 0
	for i := 1; i < len(ys); i++ {
		xs[k+1], ws[k+1], ys[k+1] = xs[i], ws[i], ys[i]
		k++
		for k > 0 && ys[k-1] > ys[k] {
			w := ws[k-1] + ws[k]
			ys[k-1] = (ys[k-1]*ws[k-1] + ys[k]*ws[k]) / w
			ws[k-1] = w
			xs[k-1] = xs[k] // block spans up to the higher score
			k--
		}
	}
	xs, ws, ys = xs[:k+1], ws[:k+1], ys[:k+1]

	// Merge blocks lighter than minBin, so a probability is never estimated
	// from a handful of transactions.
	cx := make([]float64, 0, len(xs))
	cy := make([]float64, 0, len(xs))
	var accW, accY float64
	var accX float64
	for i := range xs {
		accY += ys[i] * ws[i]
		accW += ws[i]
		accX = xs[i]
		if accW >= float64(minBin) {
			cx = append(cx, accX)
			cy = append(cy, accY/accW)
			accW, accY = 0, 0
		}
	}
	if accW > 0 {
		cx = append(cx, accX)
		cy = append(cy, accY/accW)
	}
	if len(cx) == 0 {
		return &Calibrator{X: []float64{0, 1}, Y: []float64{0, 1}}
	}
	return &Calibrator{X: cx, Y: cy}
}

// Apply maps a raw score to a calibrated probability, interpolating between
// breakpoints.
func (c *Calibrator) Apply(raw float64) float64 {
	if c == nil || len(c.X) == 0 {
		return raw
	}
	if raw <= c.X[0] {
		return c.Y[0]
	}
	last := len(c.X) - 1
	if raw >= c.X[last] {
		return c.Y[last]
	}
	i := sort.SearchFloat64s(c.X, raw)
	if i == 0 {
		return c.Y[0]
	}
	x0, x1 := c.X[i-1], c.X[i]
	y0, y1 := c.Y[i-1], c.Y[i]
	if x1 == x0 {
		return y1
	}
	return y0 + (y1-y0)*(raw-x0)/(x1-x0)
}

// ReliabilityBin is one bucket of a reliability diagram.
type ReliabilityBin struct {
	Lo        float64 `json:"lo"`
	Hi        float64 `json:"hi"`
	N         int     `json:"n"`
	Predicted float64 `json:"predicted"` // mean calibrated score in the bin
	Observed  float64 `json:"observed"`  // actual fraud rate in the bin
}

// Reliability compares predicted probability against observed frequency.
//
// A well-calibrated scorer lies on the diagonal: of the transactions it rated
// at 0.3, about 30% should really be fraud. Deviation upward means the scorer
// is overconfident, which in a payments context means blocking customers on
// evidence weaker than the number implies.
func Reliability(calibrated []float64, fraud []bool, bins int) []ReliabilityBin {
	if bins < 2 {
		bins = 10
	}
	type acc struct {
		n         int
		sumPred   float64
		sumActual float64
	}
	buckets := make([]acc, bins)
	for i := range calibrated {
		if i >= len(fraud) {
			break
		}
		b := int(calibrated[i] * float64(bins))
		if b >= bins {
			b = bins - 1
		}
		if b < 0 {
			b = 0
		}
		buckets[b].n++
		buckets[b].sumPred += calibrated[i]
		if fraud[i] {
			buckets[b].sumActual++
		}
	}

	out := make([]ReliabilityBin, 0, bins)
	for i, a := range buckets {
		rb := ReliabilityBin{
			Lo: float64(i) / float64(bins),
			Hi: float64(i+1) / float64(bins),
			N:  a.n,
		}
		if a.n > 0 {
			rb.Predicted = a.sumPred / float64(a.n)
			rb.Observed = a.sumActual / float64(a.n)
		}
		out = append(out, rb)
	}
	return out
}

// ECE is the expected calibration error: the average gap between predicted
// and observed probability, weighted by how much traffic falls in each bin.
//
// One number for "how far off the diagonal is this", so calibration quality
// can be tracked without reading a chart.
func ECE(bins []ReliabilityBin) float64 {
	var total int
	for _, b := range bins {
		total += b.N
	}
	if total == 0 {
		return 0
	}
	var e float64
	for _, b := range bins {
		if b.N == 0 {
			continue
		}
		d := b.Predicted - b.Observed
		if d < 0 {
			d = -d
		}
		e += d * float64(b.N) / float64(total)
	}
	return e
}
