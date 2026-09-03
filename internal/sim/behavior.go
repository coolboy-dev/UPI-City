package sim

import (
	"math/rand/v2"

	"github.com/yug/upi-city/internal/obs"
)

// ---------------------------------------------------------------------------
// The population is built so that every fraud archetype has a legitimate
// near-twin that trips the same naive signal. A detector is only interesting
// to the extent it can separate the members of these pairs:
//
//   signal            fraud                legitimate      discriminator
//   ───────────────── ──────────────────── ─────────────── ─────────────────
//   high velocity     mule fan-in          MegaMerchant    counterparty novelty
//   one-to-many burst ring disbursal       Payroll         amount dispersion
//   circular flow     layering cycle       SupplyPair      traversal speed,
//                                                          net-flow asymmetry
//
// The hard negatives are NOT decoration and are not bolted on later: without
// them the false-positive rate is structurally zero and every number this
// project reports is worthless.
// ---------------------------------------------------------------------------

// Consumer is ordinary retail traffic: occasional small payments to merchants,
// drawn from a modest personal set of places they shop.
type Consumer struct {
	rate  float64
	haunt []obs.AgentID // the handful of merchants this person actually uses
}

func (c *Consumer) Name() string { return "consumer" }

func (c *Consumer) Decide(self AgentView, w *Snapshot, rng *rand.Rand, t obs.Tick, dst []Intent) []Intent {
	// A compromised account drains toward the ring's entry point. Handled
	// here rather than in Mule because the victims of an account takeover are
	// ordinary consumers — which is exactly what makes the resulting fan-in
	// hard to tell from a merchant's.
	if self.Fraud == FraudTakeover && self.RingNext != 0 {
		// Drained in chunks rather than one transfer, for the same reason the
		// mules structure: a single large movement out of a consumer account
		// is the easiest thing in the world to flag on amount alone.
		if self.BalanceP < 50_000 || rng.Float64() >= w.Attack.TakeoverRate {
			return dst
		}
		frac := 0.06 + rng.Float64()*0.12
		amt := int64(float64(self.BalanceP) * frac)
		if amt < 20_000 {
			amt = 20_000
		}
		if amt > self.BalanceP {
			return dst
		}
		return append(dst, Intent{To: self.RingNext, AmountP: amt})
	}

	// A scam victim makes ONE large voluntary payment to a fraudster. No
	// chain, no repetition, no cycle — the money simply leaves. Their balance
	// falls after paying, so this self-limits without needing state in a
	// phase that must stay pure.
	if self.Fraud == FraudScamVictim && self.RingNext != 0 {
		if self.BalanceP < 100_000 || rng.Float64() >= 0.05 {
			return dst
		}
		frac := 0.35 + rng.Float64()*0.45
		return append(dst, Intent{To: self.RingNext, AmountP: int64(float64(self.BalanceP) * frac)})
	}

	if rng.Float64() >= c.rate*w.Surge*w.Drift {
		return dst
	}
	// Mostly familiar merchants, occasionally somewhere new. This is what
	// gives legitimate agents a low but non-zero counterparty-novelty rate —
	// a detector keyed on novelty alone would flag every holiday shopper.
	var to obs.AgentID
	if len(c.haunt) > 0 && rng.Float64() < 0.85 {
		to = c.haunt[rng.IntN(len(c.haunt))]
	} else {
		to = w.PickMerchant(rng)
	}
	if to == self.ID {
		return dst
	}
	// Drift moves the amount distribution too, not only the rate. A detector
	// whose baseline tracks rate but not amount would otherwise appear immune.
	amt := lognormalPaise(rng, consumerMedianRupees*w.Drift, consumerSigma)
	// A payment app shows the user their balance, so people do not attempt
	// what they plainly cannot pay. Without this, an agent that drifts near
	// zero retries thousands of times and single-handedly produces most of
	// the run's declines — noise that looks like an infrastructure signal.
	if amt > self.BalanceP {
		return dst
	}
	return append(dst, Intent{To: to, AmountP: amt})
}

// MegaMerchant is a large legitimate business.
//
// HARD NEGATIVE for the velocity and fanout detectors, and the twin of a mule
// hub — but on the INBOUND side. Its defining trait is that hundreds of
// distinct consumers pay it, which is exactly the fan-in shape a collection
// mule presents. What separates them is that a merchant's payers RECUR (the
// same customers return) while a mule's are almost all newly seen, and that a
// merchant retains what it receives while a mule forwards it within minutes.
//
// Outbound here is only refunds and small settlements, at a small fraction of
// inbound. Modelling a merchant as a heavy net payer is not just unrealistic,
// it bankrupts it within a few thousand ticks and starves the whole network.
type MegaMerchant struct {
	rate     float64
	partners []obs.AgentID // large but FIXED: recurrence is the discriminator

	// settleTo closes the economy. Consumer spending accumulates in
	// merchants; payroll companies pay it back out as salary but take no
	// revenue. Without a return path the payroll accounts drain — slowly
	// enough to look fine over 10k ticks and then decline everything by 30k —
	// and the long runs the metrics depend on quietly become a study of
	// insolvency. Merchants settling revenue upstream makes the flow closed.
	settleTo   []obs.AgentID
	settleRate float64
}

func (m *MegaMerchant) Name() string { return "mega_merchant" }

func (m *MegaMerchant) Decide(self AgentView, w *Snapshot, rng *rand.Rand, t obs.Tick, dst []Intent) []Intent {
	// Upstream revenue settlement: infrequent, large, to a fixed counterparty.
	if len(m.settleTo) > 0 && rng.Float64() < m.settleRate {
		to := m.settleTo[rng.IntN(len(m.settleTo))]
		if to != self.ID && self.BalanceP > 0 {
			dst = append(dst, Intent{To: to, AmountP: self.BalanceP / 4})
		}
	}

	if len(m.partners) == 0 || rng.Float64() >= m.rate*w.Surge {
		return dst
	}
	to := m.partners[rng.IntN(len(m.partners))]
	if to == self.ID {
		return dst
	}
	return append(dst, Intent{To: to, AmountP: lognormalPaise(rng, merchantMedianRupees, merchantSigma)})
}

// PayrollDisburser pays a fixed roster of employees on a fixed cycle.
//
// HARD NEGATIVE for the fanout detector: a textbook one-to-many burst, which
// is also exactly what a ring looks like when it disburses laundered funds to
// its members. The discriminator is amount DISPERSION — salaries are tightly
// clustered, laundering disbursals are not.
type PayrollDisburser struct {
	period    obs.Tick
	offset    obs.Tick
	employees []obs.AgentID
	baseP     int64

	// bench and churn model hiring and attrition. A roster frozen for the
	// whole run is trivially learned after one cycle and stops being a hard
	// negative at all — it scores exactly zero forever, which flatters the
	// detector rather than testing it. Real companies replace a few people
	// every cycle, so a few recipients are always genuinely new.
	bench []obs.AgentID
	churn int
}

// rosterFor returns the roster for payroll cycle k. Pure: the same cycle
// always yields the same roster, so the decide phase stays free of mutation.
func (p *PayrollDisburser) rosterFor(k int, out []obs.AgentID) []obs.AgentID {
	out = append(out[:0], p.employees...)
	if p.churn == 0 || len(p.bench) == 0 || len(out) == 0 {
		return out
	}
	for j := 0; j < p.churn; j++ {
		n := k*p.churn + j
		out[n%len(out)] = p.bench[n%len(p.bench)]
	}
	return out
}

func (p *PayrollDisburser) Name() string { return "payroll" }

func (p *PayrollDisburser) Decide(self AgentView, w *Snapshot, rng *rand.Rand, t obs.Tick, dst []Intent) []Intent {
	if p.period == 0 || t%p.period != p.offset {
		return dst
	}
	roster := p.rosterFor(int(t/p.period), make([]obs.AgentID, 0, len(p.employees)))
	for _, e := range roster {
		if e == self.ID {
			continue
		}
		// ±4%: salaries differ by grade but are nowhere near lognormal.
		jitter := 1.0 + (rng.Float64()-0.5)*0.08
		dst = append(dst, Intent{To: e, AmountP: int64(float64(p.baseP) * jitter)})
	}
	return dst
}

// SupplyChainPair is a supplier and a distributor settling in both directions.
//
// HARD NEGATIVE for the cycle detector. It forms a genuine, persistent cycle
// in the transaction graph — precisely the topology a layering ring produces.
// Cycle EXISTENCE is therefore not a fraud signal. What separates them is that
// this cycle is slow (settling over hundreds of ticks) and net-balanced (value
// returns), whereas a layering cycle is fast and net-directional with value
// retained at each hop.
//
// Groups of THREE, not two. Triangular trade credit is common in real supply
// chains, and it matters here for an uncomfortable reason: the cycle detector
// excludes 2-cycles outright, so a population of only pairs would let that
// exclusion look like insight while nothing legitimate ever tested it. A
// 3-cycle forces the detector to discriminate on speed and value retention,
// which is what it actually claims to do.
type SupplyChainPair struct {
	partner obs.AgentID
	period  obs.Tick
	offset  obs.Tick
	baseP   int64

	// trueUpEvery makes an occasional settlement much larger than usual —
	// a quarter-end reconciliation. Without it these agents transact at one
	// constant size, so their own baseline makes every payment unremarkable
	// and they can never trip an amount-significance check.
	trueUpEvery int
	trueUpScale float64
}

func (s *SupplyChainPair) Name() string { return "supply_pair" }

func (s *SupplyChainPair) Decide(self AgentView, w *Snapshot, rng *rand.Rand, t obs.Tick, dst []Intent) []Intent {
	if s.period == 0 || t%s.period != s.offset {
		return dst
	}
	jitter := 1.0 + (rng.Float64()-0.5)*0.3
	amt := float64(s.baseP) * jitter
	if s.trueUpEvery > 0 && int(t/s.period)%s.trueUpEvery == 0 {
		amt *= s.trueUpScale
	}
	return append(dst, Intent{To: s.partner, AmountP: int64(amt)})
}

// Mule is a recruitable account.
//
// While dormant it is INDISTINGUISHABLE from a consumer — same rate, same
// amounts, same targets. That matters: if the archetype itself were visible
// before recruitment, the population would leak ground truth regardless of
// how carefully the Event struct is guarded.
type Mule struct {
	cover Consumer
}

func (m *Mule) Name() string { return "mule" }

func (m *Mule) Decide(self AgentView, w *Snapshot, rng *rand.Rand, t obs.Tick, dst []Intent) []Intent {
	switch self.Fraud {
	case FraudMule, FraudCashout:
		// ─── STRUCTURING ────────────────────────────────────────────────
		//
		// The ring does NOT move a balance in one large transfer. It splits
		// the value into many payments drawn from the same range as ordinary
		// retail traffic.
		//
		// This is what real laundering does, and modelling it correctly is
		// what makes this benchmark worth anything. An earlier version had
		// mules forwarding most of a balance at once, and the consequence was
		// measurable: a one-line "amount > ₹50,000" rule scored an AUC-PR of
		// 0.563 against the detectors' 0.119. The fraud was not being caught
		// by graph structure at all, it was being caught by being
		// conspicuously large.
		//
		// That baseline is now scored as a rank over the amount distribution
		// rather than against a fixed rupee figure — see metrics.bigAmountBaseline
		// for why the constant form went silently blind once amounts were
		// fitted to published UPI figures.
		//
		// Structuring is the launderer's real trade-off, and it is the thesis
		// of this project: hiding from an amount threshold means making many
		// more transactions, which is exactly what velocity and cycle
		// detection exist to see. You cannot evade both at once.
		if self.RingNext == 0 || self.BalanceP < 50_000 {
			return dst
		}
		// Rates are set so the ring accounts for a low single-digit share of
		// network traffic. Prevalence is a benchmark parameter in disguise:
		// an early version of this had structuring push it to 13%, which
		// makes any detector look good and is nothing like a real payment
		// network. Target is 1.5-3%.
		rate := w.Attack.MuleRate
		if self.Fraud == FraudCashout {
			rate *= 0.6 // the cash-out end moves less often
		}
		if rng.Float64() >= rate {
			return dst
		}
		amt := lognormalPaise(rng, w.Attack.MuleAmountRupees, 0.9)
		if amt > self.BalanceP {
			return dst
		}
		return append(dst, Intent{To: self.RingNext, AmountP: amt})
	}
	return m.cover.Decide(self, w, rng, t, dst)
}

// BotBurst is a recruitable account that, once activated, hammers a single
// merchant with high-frequency low-value transactions from a shared device.
// Dormant behaviour is again ordinary consumer traffic.
type BotBurst struct {
	cover  Consumer
	target obs.AgentID
}

func (b *BotBurst) Name() string { return "bot" }

func (b *BotBurst) Decide(self AgentView, w *Snapshot, rng *rand.Rand, t obs.Tick, dst []Intent) []Intent {
	if self.Fraud != FraudBot {
		return b.cover.Decide(self, w, rng, t, dst)
	}
	to := b.target
	if to == 0 {
		to = w.PickMerchant(rng)
	}
	for i := 0; i < 4; i++ {
		if rng.Float64() < 0.7 {
			dst = append(dst, Intent{To: to, AmountP: int64(1000 + rng.IntN(4000))})
		}
	}
	return dst
}
