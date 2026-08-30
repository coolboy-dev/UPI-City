package sim

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yug/upi-city/internal/truth"
)

// BalanceStat summarises the wealth of one archetype at a point in time.
type BalanceStat struct {
	Archetype truth.Archetype
	N         int
	MinP      int64
	MedianP   int64
	MaxP      int64
	TotalP    int64
	Broke     int // agents too poor to make a typical payment
}

// Balances reports wealth by archetype.
//
// This is a flow-balance check, not decoration. An agent-based economy can
// look healthy for ten thousand ticks and then seize up once one archetype
// drains, and the failure presents as a rising decline rate rather than as an
// error. Since every metric downstream is computed over long multi-seed runs,
// a slow leak here would quietly turn the benchmark into a study of
// insolvency instead of one of fraud detection.
func (w *World) Balances() []BalanceStat {
	const brokeThresholdP = 50_000 // ₹500

	by := map[truth.Archetype][]int64{}
	for i := 1; i < len(w.Agents); i++ {
		a := &w.Agents[i]
		by[a.Archetype] = append(by[a.Archetype], a.BalanceP)
	}

	order := []truth.Archetype{
		truth.ArchConsumer, truth.ArchMule, truth.ArchBot,
		truth.ArchMegaMerchant, truth.ArchPayroll, truth.ArchSupplyPair,
	}
	out := make([]BalanceStat, 0, len(order))
	for _, arch := range order {
		v := by[arch]
		if len(v) == 0 {
			continue
		}
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		st := BalanceStat{
			Archetype: arch,
			N:         len(v),
			MinP:      v[0],
			MedianP:   v[len(v)/2],
			MaxP:      v[len(v)-1],
		}
		for _, b := range v {
			st.TotalP += b
			if b < brokeThresholdP {
				st.Broke++
			}
		}
		out = append(out, st)
	}
	return out
}

// BalanceReport renders Balances as a table in rupees.
func (w *World) BalanceReport() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-14s %5s %14s %14s %14s %7s\n",
		"archetype", "n", "min ₹", "median ₹", "max ₹", "broke")
	for _, s := range w.Balances() {
		fmt.Fprintf(&b, "%-14s %5d %14s %14s %14s %7d\n",
			s.Archetype, s.N,
			rupees(s.MinP), rupees(s.MedianP), rupees(s.MaxP), s.Broke)
	}
	return b.String()
}

func rupees(p int64) string {
	r := p / 100
	switch {
	case r >= 10_000_000:
		return fmt.Sprintf("%.2f cr", float64(r)/10_000_000)
	case r >= 100_000:
		return fmt.Sprintf("%.2f L", float64(r)/100_000)
	default:
		return fmt.Sprintf("%d", r)
	}
}
