package server

import (
	"fmt"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/sim"
	"github.com/yug/upi-city/internal/truth"
)

// liveSource wraps a running simulation.
type liveSource struct{ w *sim.World }

// NewLiveSource returns a Source backed by a fresh world.
func NewLiveSource(cfg sim.Config) Source { return &liveSource{w: sim.New(cfg)} }

func (l *liveSource) Step() []obs.Event { return l.w.Step() }
func (l *liveSource) Now() obs.Tick     { return l.w.Now }
func (l *liveSource) Done() bool        { return false }
func (l *liveSource) Live() bool        { return true }

func (l *liveSource) Label(id obs.TxID) truth.Label           { return l.w.Truth.Label(id) }
func (l *liveSource) IncidentOf(id obs.TxID) truth.IncidentID { return l.w.Truth.IncidentOf(id) }
func (l *liveSource) Incidents() []truth.Incident             { return l.w.Truth.Incidents() }

func (l *liveSource) Inject(name string, cfg chaos.Config) error {
	s, ok := chaos.New(name)
	if !ok {
		return fmt.Errorf("unknown scenario %q", name)
	}
	cfg.StartTick = l.w.Now + 5
	l.w.SetScenario(s, cfg)
	return nil
}

func (l *liveSource) Nodes() []Node {
	out := make([]Node, 0, len(l.w.Agents))
	for i := 1; i < len(l.w.Agents); i++ {
		a := &l.w.Agents[i]
		out = append(out, Node{
			ID: a.ID, X: a.X, Y: a.Y, R: radiusFor(a.Archetype),
			Kind: a.Archetype.String(), Bank: uint8(a.Bank),
		})
	}
	return out
}

// World exposes the underlying simulation, for the scenario intensity readout.
func (l *liveSource) World() *sim.World { return l.w }

func radiusFor(a truth.Archetype) float32 {
	switch a {
	case truth.ArchMegaMerchant:
		return 7
	case truth.ArchPayroll:
		return 5.5
	case truth.ArchSupplyPair:
		return 4
	}
	return 2.2
}
