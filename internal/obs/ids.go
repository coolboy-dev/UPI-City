// Package obs defines the observable surface of the simulation: the only
// view of network activity that detectors are ever given.
//
// This package MUST NOT import anything else from internal/. That constraint
// is what makes the ground-truth firewall checkable rather than aspirational —
// see internal/detect/boundary_test.go.
package obs

// Tick is simulation time. One tick is Config.MsPerTick milliseconds of
// notional wall-clock.
type Tick uint64

// AgentID indexes densely into World.Agents. Dense integer IDs (rather than
// strings or a map) are what keep the tick loop allocation-free and its
// iteration order deterministic.
type AgentID uint32

// TxID is assigned in strictly increasing creation order.
type TxID uint64

// BankID indexes densely into World.Banks.
type BankID uint8

// Status is the terminal outcome of a transaction.
type Status uint8

const (
	StatusSuccess Status = iota
	StatusFailed
	StatusTimeout
)

func (s Status) String() string {
	switch s {
	case StatusSuccess:
		return "success"
	case StatusFailed:
		return "failed"
	case StatusTimeout:
		return "timeout"
	}
	return "unknown"
}
