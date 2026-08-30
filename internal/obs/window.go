package obs

import "math"

// Window is a fixed-memory sliding-window counter over ticks.
//
// Memory is allocated once at construction and never grows. At 5,000 agents
// with several windows each, growable per-agent slices would be the dominant
// allocation in the tick loop and would put real pressure on a machine with
// limited free RAM.
type Window struct {
	count []int32
	sum   []int64
	per   Tick // ticks per bucket
	last  Tick
	idx   int
}

// NewWindow returns a window covering span ticks, bucketed at per-tick
// granularity. span is rounded up to a whole number of buckets.
func NewWindow(span, per Tick) *Window {
	if per == 0 {
		per = 1
	}
	n := int((span + per - 1) / per)
	if n < 1 {
		n = 1
	}
	return &Window{
		count: make([]int32, n),
		sum:   make([]int64, n),
		per:   per,
	}
}

// Advance rolls the window forward to tick t, zeroing any buckets that have
// scrolled out of range.
func (w *Window) Advance(t Tick) {
	if t < w.last {
		return
	}
	steps := int((t / w.per) - (w.last / w.per))
	if steps <= 0 {
		w.last = t
		return
	}
	if steps > len(w.count) {
		steps = len(w.count)
	}
	for i := 0; i < steps; i++ {
		w.idx = (w.idx + 1) % len(w.count)
		w.count[w.idx] = 0
		w.sum[w.idx] = 0
	}
	w.last = t
}

// Add records one observation at tick t.
func (w *Window) Add(t Tick, amountP int64) {
	w.Advance(t)
	w.count[w.idx]++
	w.sum[w.idx] += amountP
}

// Count returns the number of observations currently in the window.
func (w *Window) Count() int32 {
	var n int32
	for _, c := range w.count {
		n += c
	}
	return n
}

// Sum returns the total value currently in the window, in paise.
func (w *Window) Sum() int64 {
	var s int64
	for _, v := range w.sum {
		s += v
	}
	return s
}

// Baseline is an exponentially-weighted mean and variance for a single agent.
//
// This is the primitive that lets every detector score an agent against its
// OWN history rather than a global threshold. That distinction is what keeps
// a legitimate high-volume merchant off the false-positive list, and what
// survives a network-wide surge: during a festival every agent's rate rises
// together, so a global threshold fires on everyone while a per-agent
// baseline barely moves.
type Baseline struct {
	Mean  float64
	Var   float64
	alpha float64
	n     uint32
}

// NewBaseline returns a baseline with the given smoothing factor. Smaller
// alpha means a longer memory.
func NewBaseline(alpha float64) Baseline {
	return Baseline{alpha: alpha}
}

// Observe folds x into the running mean and variance.
func (b *Baseline) Observe(x float64) {
	if b.alpha == 0 {
		b.alpha = 0.05
	}
	if b.n == 0 {
		b.Mean = x
		b.Var = 0
		b.n = 1
		return
	}
	d := x - b.Mean
	b.Mean += b.alpha * d
	b.Var = (1 - b.alpha) * (b.Var + b.alpha*d*d)
	if b.n < math.MaxUint32 {
		b.n++
	}
}

// Ready reports whether enough observations have accumulated for Z to mean
// anything. Scoring against a cold baseline is how you manufacture false
// positives on freshly created accounts.
func (b *Baseline) Ready(min uint32) bool { return b.n >= min }

// Z returns the number of standard deviations x sits above the mean, floored
// at zero. The variance floor prevents a division blow-up on agents whose
// history is perfectly constant.
func (b *Baseline) Z(x float64) float64 {
	sd := math.Sqrt(b.Var)
	if sd < 1e-9 {
		sd = 1e-9
	}
	z := (x - b.Mean) / sd
	if z < 0 {
		return 0
	}
	return z
}
