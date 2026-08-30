package obs

// Event is the observable record of one settled transaction. It is the ONLY
// representation of network activity that crosses into the detection layer.
//
// ┌───────────────────────────────────────────────────────────────────────┐
// │ DO NOT ADD GROUND-TRUTH FIELDS TO THIS STRUCT.                        │
// │                                                                       │
// │ No Label. No Archetype. No IncidentID. No "IsFraud". Nothing derived  │
// │ from any of those.                                                    │
// │                                                                       │
// │ Every metric this project reports — precision, recall, detection      │
// │ latency — is only meaningful because detectors cannot see the labels. │
// │ A single leaked field silently invalidates the entire submission, and │
// │ it will still look like it works.                                     │
// │                                                                       │
// │ The labelled counterpart is sim.Transaction, which embeds this type.  │
// └───────────────────────────────────────────────────────────────────────┘
type Event struct {
	// Tick is when the transaction was initiated.
	Tick Tick
	// SettleTick is when it became observable. Detectors treat this as "now":
	// a real monitoring system cannot see a payment before it clears.
	SettleTick Tick

	TxID TxID
	From AgentID
	To   AgentID

	// Bank is the PSP that routed the transaction.
	Bank BankID

	// AmountP is in paise. Money is always an integer here — float
	// accumulation in balances would break determinism across runs.
	AmountP int64

	Status    Status
	LatencyMs uint16

	// DeviceID is a legitimately observable signal: real processors see
	// device fingerprints, and shared devices are a genuine fraud indicator.
	DeviceID uint32

	// IsNewFrom reports whether the sender's account age is below the
	// new-account threshold. Also legitimately observable.
	IsNewFrom bool
}

// StreamHash is a running FNV-1a hash over an event stream. It is the
// determinism gate: two runs with the same seed must produce the same hash.
type StreamHash uint64

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// NewStreamHash returns the FNV-1a offset basis.
func NewStreamHash() StreamHash { return fnvOffset64 }

func (h StreamHash) mix(v uint64) StreamHash {
	for i := 0; i < 8; i++ {
		h ^= StreamHash(v & 0xff)
		h *= fnvPrime64
		v >>= 8
	}
	return h
}

// Add folds one event into the hash. Every field participates, so a
// divergence anywhere in the stream is caught.
func (h StreamHash) Add(e Event) StreamHash {
	h = h.mix(uint64(e.Tick))
	h = h.mix(uint64(e.SettleTick))
	h = h.mix(uint64(e.TxID))
	h = h.mix(uint64(e.From))
	h = h.mix(uint64(e.To))
	h = h.mix(uint64(e.Bank))
	h = h.mix(uint64(e.AmountP))
	h = h.mix(uint64(e.Status))
	h = h.mix(uint64(e.LatencyMs))
	h = h.mix(uint64(e.DeviceID))
	if e.IsNewFrom {
		h = h.mix(1)
	} else {
		h = h.mix(0)
	}
	return h
}
