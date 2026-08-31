package sim

import (
	"math"
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// World is the simulation. One World is one reproducible run.
//
// AgentID 0 is a reserved void agent that never acts and is never a valid
// target, so zero can be used as "no such agent" throughout without an
// extra validity flag on every reference.
type World struct {
	Cfg    Config
	Now    obs.Tick
	Agents []Agent
	Banks  []Bank
	Truth  *truth.Ledger

	seeds   Seeds
	bankRNG *rand.Rand

	// civilians are every ordinary account: consumers plus the recruitable
	// mules and bots, which are indistinguishable from consumers until a
	// chaos scenario activates them.
	//
	// Merchant partner lists and payroll rosters are drawn from THIS pool,
	// not from consumers alone. If recruitable accounts were excluded they
	// would receive no salary, drain, and fall silent — and that difference
	// in dormant behaviour would leak the archetype into the observable
	// stream no matter how carefully obs.Event is guarded.
	civilians []obs.AgentID

	snap Snapshot

	// intents[i] holds agent i's proposals for the current tick. Indexed by
	// agent so that Phase A can later be sharded across cores with each
	// worker writing only to its own range — the property that makes the
	// parallel run bit-identical to the serial one.
	intents [][]Intent

	// settle is a ring buffer of in-flight transactions indexed by
	// settlement tick. Fixed memory, no map, no sorting, fully deterministic.
	settle [][]Transaction

	events []obs.Event
	nextTx obs.TxID

	scenario       chaos.Scenario
	chaosCfg       chaos.Config
	incident       truth.IncidentID
	incidentClosed bool

	// retry holds payments that failed and will be re-attempted. A degraded
	// bank therefore produces a genuine retry storm across thousands of
	// legitimate accounts — the same velocity spike a mule hub produces,
	// which is exactly what makes the outage a useful hard negative rather
	// than a cosmetic red light.
	retry [][]Intent

	// workers shards the decide phase. One means serial.
	workers int
	// pool holds persistent decide-phase workers, woken once per tick.
	// Creating goroutines inside the loop cost more than the work they
	// divided; see decide().
	pool      []chan struct{}
	poolStart sync.WaitGroup
	poolTick  obs.Tick

	Stats Stats
}

// Stats are run-level counters, useful as a sanity check that a run actually
// did something before any metric is computed from it.
type Stats struct {
	Created  int
	Settled  int
	Failed   int
	TimedOut int
	Declined int // insufficient balance, never reached a bank
	Retried  int
}

// New builds a world from a config. Construction is fully deterministic.
func New(cfg Config) *World {
	w := &World{
		Cfg:   cfg,
		seeds: Seeds{Master: cfg.Seed},
	}
	w.bankRNG = w.seeds.For("bank")

	w.buildBanks()
	w.buildAgents()
	w.buildBehaviors()
	w.assignLayout()

	w.intents = make([][]Intent, len(w.Agents))
	w.retry = make([][]Intent, len(w.Agents))
	for i := range w.intents {
		w.intents[i] = make([]Intent, 0, 8)
	}

	ring := int(cfg.MaxSettleTicks) + 1
	w.settle = make([][]Transaction, ring)
	for i := range w.settle {
		w.settle[i] = make([]Transaction, 0, 16)
	}

	w.events = make([]obs.Event, 0, 1024)
	w.nextTx = 1

	// Parallel by default only where it pays. Below the threshold the serial
	// path runs regardless, so small runs are unaffected.
	w.workers = 1
	if cfg.NumAgents >= ParallelThreshold {
		w.workers = runtime.NumCPU()
	}
	return w
}

func (w *World) buildBanks() {
	rng := w.seeds.For("banks")
	names := []string{"HDFC", "ICICI", "SBI", "AXIS", "KOTAK", "PNB", "YES", "IDFC"}
	n := w.Cfg.NumBanks
	if n > len(names) {
		n = len(names)
	}
	w.Banks = make([]Bank, n)
	for i := range w.Banks {
		w.Banks[i] = Bank{
			ID:            obs.BankID(i),
			Name:          names[i],
			BaseLatencyMs: uint16(120 + rng.IntN(180)),
			BaseFailRate:  0.004 + rng.Float64()*0.006,
		}
	}
}

func (w *World) buildAgents() {
	cfg := w.Cfg
	n := cfg.NumAgents

	nMega := max(1, int(float64(n)*cfg.FracMegaMerchant))
	nPay := max(1, int(float64(n)*cfg.FracPayroll))
	nPair := int(float64(n) * cfg.FracSupplyPair)
	nPair -= nPair % 3 // triangles only
	nRecruit := int(float64(n) * cfg.FracRecruitable)

	w.Agents = make([]Agent, n+1) // index 0 is the void agent
	w.Truth = truth.NewLedger(n+1, n*40)

	rng := w.seeds.For("population")

	assign := func(id int, arch truth.Archetype) {
		a := &w.Agents[id]
		a.ID = obs.AgentID(id)
		a.Archetype = arch
		a.Bank = obs.BankID(rng.IntN(len(w.Banks)))
		a.BalanceP = w.Cfg.OpeningBalanceP
		a.DeviceID = uint32(id)
		a.BirthTick = 0
		// Founding accounts predate the run, with staggered vintages. A small
		// slice are genuinely recent, so "new account" means something
		// before any chaos scenario opens one.
		a.PriorAge = obs.Tick(rng.IntN(120_000))
		a.rng = w.seeds.ForAgent(uint32(id))
		w.Truth.SetArchetype(a.ID, arch)
	}

	id := 1
	for i := 0; i < nMega && id <= n; i, id = i+1, id+1 {
		assign(id, truth.ArchMegaMerchant)
		w.Agents[id].BalanceP = w.Cfg.MerchantBalanceP
	}
	for i := 0; i < nPay && id <= n; i, id = i+1, id+1 {
		assign(id, truth.ArchPayroll)
		w.Agents[id].BalanceP = w.Cfg.PayrollBalanceP
	}
	for i := 0; i < nPair && id <= n; i, id = i+1, id+1 {
		assign(id, truth.ArchSupplyPair)
		w.Agents[id].BalanceP = w.Cfg.PairBalanceP
	}
	// Recruitable accounts. Until a chaos scenario activates them they are
	// ordinary consumers in every observable respect.
	for i := 0; i < nRecruit && id <= n; i, id = i+1, id+1 {
		if i%4 == 0 {
			assign(id, truth.ArchBot)
		} else {
			assign(id, truth.ArchMule)
		}
	}
	for ; id <= n; id++ {
		assign(id, truth.ArchConsumer)
	}

	// Target pools, built in ascending ID order so they are stable.
	for i := 1; i <= n; i++ {
		switch w.Agents[i].Archetype {
		case truth.ArchMegaMerchant:
			w.snap.Merchants = append(w.snap.Merchants, obs.AgentID(i))
		case truth.ArchConsumer:
			w.snap.Consumers = append(w.snap.Consumers, obs.AgentID(i))
			w.civilians = append(w.civilians, obs.AgentID(i))
		case truth.ArchMule, truth.ArchBot:
			w.civilians = append(w.civilians, obs.AgentID(i))
		}
	}
}

func (w *World) buildBehaviors() {
	rng := w.seeds.For("behaviors")
	civ := w.civilians

	// Payroll rosters PARTITION the civilian population rather than sampling
	// it: everyone has exactly one employer. Random sampling leaves a tail of
	// unemployed accounts that spend down their opening balance and then
	// decline every payment, which is a study of a broken economy rather than
	// of fraud detection.
	var payrollIDs []obs.AgentID
	for i := 1; i < len(w.Agents); i++ {
		if w.Agents[i].Archetype == truth.ArchPayroll {
			payrollIDs = append(payrollIDs, obs.AgentID(i))
		}
	}
	rosters := make([][]obs.AgentID, len(payrollIDs))
	if len(payrollIDs) > 0 {
		for i, c := range civ {
			k := i % len(payrollIDs)
			rosters[k] = append(rosters[k], c)
		}
	}
	rosterOf := func(id obs.AgentID) []obs.AgentID {
		for i, p := range payrollIDs {
			if p == id {
				return rosters[i]
			}
		}
		return nil
	}

	pick := func(pool []obs.AgentID, k int) []obs.AgentID {
		if len(pool) == 0 || k <= 0 {
			return nil
		}
		if k > len(pool) {
			k = len(pool)
		}
		out := make([]obs.AgentID, 0, k)
		seen := make(map[obs.AgentID]bool, k)
		// Rejection sampling with a bounded number of attempts; the result is
		// then sorted so membership order never depends on draw order.
		for attempts := 0; len(out) < k && attempts < k*8; attempts++ {
			c := pool[rng.IntN(len(pool))]
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
		sortAgentIDs(out)
		return out
	}

	// Spend rate is set against salary income so the population stays solvent
	// over a long run. Roughly 160 payments per 10k ticks at a ~₹550 mean is
	// covered by the payroll cycle below.
	newConsumer := func() Consumer {
		return Consumer{
			rate:  0.010 + rng.Float64()*0.012,
			haunt: pick(w.snap.Merchants, 3+rng.IntN(4)),
		}
	}

	var tri []obs.AgentID
	for i := 1; i < len(w.Agents); i++ {
		a := &w.Agents[i]
		switch a.Archetype {
		case truth.ArchMegaMerchant:
			// A large but FIXED partner set: recurrence is what separates
			// this from a mule hub on the fan-out signal.
			// Refunds only. The merchant's hard-negative signal is its
			// INBOUND fan-in from hundreds of consumers, not its outbound.
			a.Behavior = &MegaMerchant{
				rate:       0.05 + rng.Float64()*0.10,
				partners:   pick(civ, 120+rng.IntN(120)),
				settleTo:   payrollIDs,
				settleRate: 0.006,
			}
		case truth.ArchPayroll:
			roster := rosterOf(a.ID)
			a.Behavior = &PayrollDisburser{
				period:    6000,
				offset:    obs.Tick(rng.IntN(6000)),
				employees: roster,
				baseP:     4_500_000 + int64(rng.IntN(2_000_000)),
				bench:     pick(civ, 24),
				churn:     max(1, len(roster)/12), // ~8% turnover per cycle
			}
		case truth.ArchSupplyPair:
			// Collect three, then wire them into a triangle.
			tri = append(tri, a.ID)
			if len(tri) < 3 {
				continue
			}
			period := obs.Tick(400 + rng.IntN(400))
			base := int64(8_000_000 + rng.IntN(6_000_000))
			// A → B → C → A, staggered in time: a genuine 3-cycle that is
			// slow and net-balanced. Value returns, so it is not layering —
			// and telling that apart from a ring is the detector's actual job.
			for k, id := range tri {
				w.Agents[id].Behavior = &SupplyChainPair{
					partner: tri[(k+1)%3],
					period:  period,
					offset:  obs.Tick(k) * period / 3,
					baseP:   base,
					// Rare and large: an annual reconciliation, not a routine
					// invoice. Frequent true-ups fold into the agent's own
					// baseline and stop being unusual for it at all.
					trueUpEvery: 8,
					trueUpScale: 12.0,
				}
			}
			tri = tri[:0]
		case truth.ArchBot:
			a.Behavior = &BotBurst{cover: newConsumer()}
		case truth.ArchMule:
			a.Behavior = &Mule{cover: newConsumer()}
		default:
			c := newConsumer()
			a.Behavior = &c
		}
	}
	// Supply agents left over from an incomplete triangle form no cycle;
	// make them ordinary consumers rather than leaving a nil behaviour.
	for _, id := range tri {
		c := newConsumer()
		w.Agents[id].Behavior = &c
		w.Agents[id].Archetype = truth.ArchConsumer
		w.Truth.SetArchetype(id, truth.ArchConsumer)
	}
}

// Drift is the slow permanent growth in legitimate activity at tick t.
//
// Compounding rather than linear, because population and usage growth compound
// — and because a linear ramp is trivially subtractable in a way that flatters
// an adaptive baseline.
func (w *World) Drift(t obs.Tick) float64 {
	if w.Cfg.DriftPerKTick == 0 {
		return 1
	}
	return math.Pow(1+w.Cfg.DriftPerKTick, float64(t)/1000)
}

// Surge is the network-wide activity multiplier: a temporal confound that
// lifts every agent's rate at once. A global velocity threshold fires on the
// entire population during a surge; a per-agent baseline barely notices.
func (w *World) Surge(t obs.Tick) float64 {
	if w.Cfg.SurgePeriod == 0 {
		return 1
	}
	if (t+w.Cfg.SurgePeriod-w.Cfg.SurgeOffset)%w.Cfg.SurgePeriod < w.Cfg.SurgeDuration {
		return w.Cfg.SurgeMultiplier
	}
	return 1
}

// Step advances the simulation one tick and returns the events that became
// observable during it.
//
// The four phases exist to keep the run deterministic while leaving the
// expensive phase parallelisable:
//
//	A  Decide  — pure, read-only, no mutation.        (parallelisable)
//	B  Apply   — all mutation, serial, fixed order.   (must stay serial)
//	C  Detect  — serial, fixed detector order.        (added on Day 2)
//	D  Emit    — settled transactions become visible.
func (w *World) Step() []obs.Event {
	t := w.Now
	w.snap.Tick = t
	w.snap.Surge = w.Surge(t)
	w.snap.Drift = w.Drift(t)
	w.snap.Attack = Attack{
		MuleRate:         w.chaosCfg.MuleRate,
		MuleAmountRupees: w.chaosCfg.MuleAmountRupees,
		TakeoverRate:     w.chaosCfg.TakeoverRate,
	}
	w.events = w.events[:0]

	// Chaos is applied before anything else this tick, through the single
	// effect path that journals the incident. The core loop's entire
	// knowledge of scenarios is this one call.
	w.stepChaos(t)

	// ---- Phase A: Decide (pure, shardable) ---------------------------
	w.decide(t)

	// ---- Phase B: Apply (serial, ascending agent order) --------------
	for i := 1; i < len(w.Agents); i++ {
		a := &w.Agents[i]
		// Retries first: a failed payment is re-attempted before new
		// activity, as a real client would.
		if r := w.retry[i]; len(r) > 0 {
			for _, in := range r {
				w.Stats.Retried++
				w.apply(a, in, t, true)
			}
			w.retry[i] = r[:0]
		}
		for _, in := range w.intents[i] {
			w.apply(a, in, t, false)
		}
	}

	// ---- Phase D: Emit -----------------------------------------------
	w.drain(t)

	w.Now++
	return w.events
}

// apply turns one intent into a transaction, or drops it. isRetry marks the
// result so a payment is re-attempted at most once.
func (w *World) apply(a *Agent, in Intent, t obs.Tick, isRetry bool) {
	if in.To == 0 || in.To == a.ID || in.AmountP <= 0 {
		return
	}
	if int(in.To) >= len(w.Agents) {
		return
	}
	if a.BalanceP < in.AmountP {
		w.Stats.Declined++
		return
	}

	bank := &w.Banks[a.Bank]
	status, latMs := bank.Route(w.bankRNG, t)

	id := w.nextTx
	w.nextTx++

	tx := Transaction{
		Event: obs.Event{
			Tick:      t,
			TxID:      id,
			From:      a.ID,
			To:        in.To,
			Bank:      a.Bank,
			AmountP:   in.AmountP,
			Status:    status,
			LatencyMs: latMs,
			DeviceID:  a.DeviceID,
			IsNewFrom: a.Age(t) < w.Cfg.NewAccountTicks,
		},
		// Labelling is derived from agent state by the engine. A scenario
		// author sets FraudState; they never touch this.
		Label:     a.Fraud.Label(),
		Incident:  a.Incident,
		Archetype: a.Archetype,
		Retried:   isRetry,
	}

	// Authorisation hold: debit immediately, credit on settlement. Without
	// the hold, an agent could spend the same balance many times within one
	// settlement window.
	a.BalanceP -= in.AmountP

	delay := bank.SettleDelay(w.Cfg.SettleTicks, w.Cfg.MsPerTick, t)
	if delay > w.Cfg.MaxSettleTicks {
		delay = w.Cfg.MaxSettleTicks
	}
	settleAt := t + delay
	tx.SettleTick = settleAt

	w.Truth.RecordTx(id, tx.Label, tx.Incident)
	idx := int(settleAt) % len(w.settle)
	w.settle[idx] = append(w.settle[idx], tx)
	w.Stats.Created++
}

// drain settles everything due at tick t and emits it.
func (w *World) drain(t obs.Tick) {
	idx := int(t) % len(w.settle)
	b := w.settle[idx]
	for i := range b {
		tx := &b[i]
		switch tx.Status {
		case obs.StatusSuccess:
			w.Agents[tx.To].BalanceP += tx.AmountP
			w.Stats.Settled++
		default:
			// Release the hold: a failed payment returns to the sender.
			w.Agents[tx.From].BalanceP += tx.AmountP
			if tx.Status == obs.StatusTimeout {
				w.Stats.TimedOut++
			} else {
				w.Stats.Failed++
			}
			// Re-attempt once. Bounded deliberately: an unbounded retry loop
			// during an outage would drown the run in traffic and turn a
			// throughput measurement into a measurement of the retry policy.
			if !tx.Retried {
				w.retry[tx.From] = append(w.retry[tx.From], Intent{To: tx.To, AmountP: tx.AmountP})
			}
		}
		w.events = append(w.events, tx.Observe())
	}
	w.settle[idx] = b[:0]
}

// Run advances the world by n ticks and returns the stream hash.
//
// The hash is the determinism gate: two runs with the same seed must agree.
func (w *World) Run(n obs.Tick) obs.StreamHash {
	h := obs.NewStreamHash()
	for i := obs.Tick(0); i < n; i++ {
		for _, e := range w.Step() {
			h = h.Add(e)
		}
	}
	return h
}

// Describe summarises the population, for the harness banner.
func (w *World) Describe() string {
	var mega, pay, pair, mule, bot, cons int
	for i := 1; i < len(w.Agents); i++ {
		switch w.Agents[i].Archetype {
		case truth.ArchMegaMerchant:
			mega++
		case truth.ArchPayroll:
			pay++
		case truth.ArchSupplyPair:
			pair++
		case truth.ArchMule:
			mule++
		case truth.ArchBot:
			bot++
		default:
			cons++
		}
	}
	return fmt.Sprintf(
		"%d agents across %d banks — %d consumer, %d mega-merchant, %d payroll, %d supply-pair, %d recruitable-mule, %d recruitable-bot",
		len(w.Agents)-1, len(w.Banks), cons, mega, pay, pair, mule, bot)
}

func sortAgentIDs(a []obs.AgentID) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
