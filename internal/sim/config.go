package sim

import "github.com/yug/upi-city/internal/obs"

// Config fully determines a run. Same Config plus same Seed must produce the
// same event stream, byte for byte — that is what makes replay, detector
// comparison, and calibration possible at all.
type Config struct {
	Seed      uint64
	NumAgents int
	Ticks     obs.Tick

	// MsPerTick is the notional wall-clock duration of one tick, used to
	// convert tick counts into the seconds reported in latency metrics.
	MsPerTick uint64

	// Population mix. Counts are derived from NumAgents by these fractions;
	// the remainder become consumers.
	FracMegaMerchant float64
	FracPayroll      float64
	FracSupplyPair   float64
	// FracRecruitable reserves ordinary-looking agents that chaos scenarios
	// can later recruit as mules or bots. Until recruited they behave exactly
	// like consumers, so their presence does not itself leak anything.
	FracRecruitable float64

	NumBanks int

	// SettleTicks is the base settlement delay. Transactions are not instant:
	// funds are held on the sender at initiation and land on the receiver
	// only once the transaction clears.
	SettleTicks obs.Tick
	// MaxSettleTicks bounds the settlement ring buffer.
	MaxSettleTicks obs.Tick

	// NewAccountTicks is the age below which an account is reported as new
	// via Event.IsNewFrom.
	NewAccountTicks obs.Tick

	// Festival surge: a network-wide temporal confound. Every agent's rate
	// rises together, so a global velocity threshold fires on the whole
	// population while a per-agent baseline barely moves. This is what makes
	// obs.Baseline earn its place rather than being ceremony.
	SurgePeriod   obs.Tick
	SurgeDuration obs.Tick
	// SurgeOffset keeps the run from BEGINNING inside a surge. Starting in
	// one means every agent is elevated while every baseline is still cold,
	// which manufactures a network-wide false positive out of nothing.
	SurgeOffset     obs.Tick
	SurgeMultiplier float64

	// Opening balances, per archetype, in paise.
	//
	// These are not cosmetic. The network has to stay roughly in flow
	// balance: consumers spend and are paid salary, merchants receive and
	// refund a little, payroll companies are externally funded. Get this
	// wrong and agents run dry, the decline rate explodes, and the resulting
	// traffic is a study of a broken economy rather than of fraud detection.
	OpeningBalanceP  int64 // consumers and recruitable accounts
	MerchantBalanceP int64
	PayrollBalanceP  int64
	PairBalanceP     int64
}

// DefaultConfig is the 300-agent development configuration.
func DefaultConfig() Config {
	return Config{
		Seed:             42,
		NumAgents:        300,
		Ticks:            10000,
		MsPerTick:        100,
		FracMegaMerchant: 0.02,
		FracPayroll:      0.01,
		FracSupplyPair:   0.04, // pairs, so this many agents total
		FracRecruitable:  0.10,
		NumBanks:         5,
		SettleTicks:      3,
		MaxSettleTicks:   64,
		NewAccountTicks:  2000,
		SurgePeriod:      20000,
		SurgeDuration:    2000,
		SurgeOffset:      12000,
		SurgeMultiplier:  2.5,
		OpeningBalanceP:  8_000_000,     // ₹80,000
		MerchantBalanceP: 500_000_000,   // ₹50 lakh
		PayrollBalanceP:  8_000_000_000, // ₹8 crore — externally funded
		PairBalanceP:     200_000_000,   // ₹2 crore
	}
}

// Seconds converts a tick count to notional seconds, for latency reporting.
func (c Config) Seconds(t obs.Tick) float64 {
	return float64(t) * float64(c.MsPerTick) / 1000.0
}
