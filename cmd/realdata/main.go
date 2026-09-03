// Command realdata scores an externally-labelled payment dataset using the
// same detection and metrics code that scores the simulator.
//
// ─── Why this exists ────────────────────────────────────────────────────────
//
// Everything else in this project measures a detector against fraud the
// project itself invented. That is the only way to know true recall — on
// production traffic you never learn what you missed — but it is also a closed
// loop: the fraud and the detector were designed by the same person, so their
// agreement is the expected outcome rather than evidence of skill.
//
// This command opens the loop. The transactions are real, the labels are
// chargebacks a payment processor actually recorded, and neither was authored
// here. The score that comes out is the one number in this repository that no
// amount of care in the simulator could have produced.
//
// ─── What it can and cannot measure ─────────────────────────────────────────
//
// The dataset has a payer and no payee. There is no counterparty column, and
// R_emaildomain is a recipient *domain* on 23% of rows, not an account. So:
//
//	velocity  scored — it baselines a payer against its own history
//	fanout    NOT scorable — fan-in is a property of the receiving account
//	cycle     NOT scorable — a cycle needs edges, and there are none
//
// Two of three detectors sit out, and that is reported rather than hidden.
// Feeding a constant placeholder receiver would be worse than useless: every
// payment would appear to land on one account, fanout would see the largest
// mule hub ever constructed, and the resulting number would be an artefact of
// the placeholder. No public dataset carries a real payer→payee payment graph
// with fraud labels, which is the reason a simulator exists here at all.
//
// The UPI realism check is also deliberately not run. Those are Indian
// payment statistics and this is US card-not-present traffic; comparing them
// would be meaningless.
package main

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yug/upi-city/internal/detect"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/risk"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/truth"
)

// fileLabels is ground truth that arrives as a lookup table rather than from a
// world that generated it.
//
// Everything beyond the label itself is absent from the source and is reported
// as absent. An archetype would be an invention — the dataset does not say
// what kind of customer anyone is — and an attack ramp is meaningless for
// fraud nobody injected.
type fileLabels struct {
	fraud map[obs.TxID]bool
}

func (l fileLabels) Label(id obs.TxID) truth.Label {
	if l.fraud[id] {
		// The dataset records that a chargeback happened, not which scheme
		// produced it. LabelRingMule is this project's generic "fraudulent"
		// and carries no claim about the mechanism.
		return truth.LabelRingMule
	}
	return truth.LabelNormal
}

func (l fileLabels) Incident(obs.TxID) truth.IncidentID    { return 0 }
func (l fileLabels) Archetype(obs.AgentID) truth.Archetype { return truth.ArchConsumer }
func (l fileLabels) Intensity(obs.Tick) float64            { return 0 }

// txn is one parsed row, before sorting.
type txn struct {
	id     obs.TxID
	tick   obs.Tick
	payer  obs.AgentID
	amount int64 // minor units (cents)
	fraud  bool
}

func main() {
	csvPath := flag.String("csv", "data/train_transaction.csv", "IEEE-CIS train_transaction.csv")
	out := flag.String("out", "results/real-ieee", "directory for metrics.json")
	dets := flag.String("detectors", "velocity", "comma-separated detectors to run")
	recordTo := flag.String("record", "", "also write events.jsonl + labels.jsonl here")
	secsPerTick := flag.Float64("secs-per-tick", 1,
		"real seconds per tick; sets the time scale the detectors observe")
	flag.Parse()

	if *secsPerTick <= 0 {
		log.Fatal("-secs-per-tick must be positive")
	}

	rows, err := readCSV(*csvPath)
	if err != nil {
		log.Fatalf("reading %s: %v", *csvPath, err)
	}
	if len(rows) == 0 {
		log.Fatal("no usable rows")
	}

	// Settlement order. The detectors are online and treat SettleTick as
	// "now", so out-of-order delivery silently changes what they observe.
	// Ties break on id to keep the run reproducible.
	slices.SortStableFunc(rows, func(a, b txn) int {
		if a.tick != b.tick {
			return int(a.tick) - int(b.tick)
		}
		if a.id < b.id {
			return -1
		} else if a.id > b.id {
			return 1
		}
		return 0
	})

	// Per-agent state is indexed by agent id, so it must be sized from the
	// largest id present — NOT from the row count, which is both larger and
	// unrelated.
	var maxPayer obs.AgentID
	base := rows[0].tick
	for _, r := range rows {
		if r.payer > maxPayer {
			maxPayer = r.payer
		}
	}

	names := strings.Split(*dets, ",")
	pipe := detect.NewNamed(names)
	if len(pipe.Names()) == 0 {
		log.Fatalf("no valid detectors in %q", *dets)
	}

	cfg := runner.ScoreConfig{
		Pipeline: pipe,
		Fusion:   risk.DefaultFusion(),
		AgentCap: int(maxPayer) + 1,
	}
	if *recordTo != "" {
		ev, err := record.Create(*recordTo, "events.jsonl")
		if err != nil {
			log.Fatal(err)
		}
		defer ev.Close()
		// Labels go to their own file, never mixed into the events a detector
		// reads. Same separation the simulated path enforces.
		lb, err := record.Create(*recordTo, "labels.jsonl")
		if err != nil {
			log.Fatal(err)
		}
		defer lb.Close()
		cfg.EventW, cfg.LabelW = ev, lb
	}

	sc := runner.NewScorer(cfg)
	labels := fileLabels{fraud: make(map[obs.TxID]bool, len(rows)/16)}
	for _, r := range rows {
		if r.fraud {
			labels.fraud[r.id] = true
		}
	}

	start := time.Now()
	for _, r := range rows {
		// Zero-base the clock so warmup is measured from the first payment.
		//
		// Bank, DeviceID, LatencyMs and IsNewFrom are left at zero because the
		// source does not carry them. No detector reads any of those fields,
		// so this removes nothing that is being measured — but inventing
		// plausible values would quietly put simulator-shaped structure into
		// data whose whole purpose is to have none.
		tick := obs.Tick(float64(r.tick-base) / *secsPerTick)
		e := obs.Event{
			Tick:       tick,
			SettleTick: tick,
			TxID:       r.id,
			From:       r.payer,
			To:         0, // no payee in this dataset; see the package comment
			AmountP:    r.amount,
			Status:     obs.StatusSuccess,
		}
		if err := sc.Observe(e, labels); err != nil {
			log.Fatalf("scoring: %v", err)
		}
	}
	elapsed := time.Since(start)

	rep := metrics.Evaluate(sc.Rows(), nil, 0, uint64(*secsPerTick*1000))
	if err := record.WriteJSON(*out, "metrics.json", rep); err != nil {
		log.Fatal(err)
	}

	sum, err := checksum(*csvPath)
	if err != nil {
		log.Fatal(err)
	}
	meta := map[string]any{
		"source":            *csvPath,
		"sha256":            sum,
		"dataset":           "IEEE-CIS Fraud Detection (Vesta), train_transaction.csv",
		"labels":            "isFraud — chargebacks recorded by the processor",
		"detectors_scored":  pipe.Names(),
		"detectors_omitted": omitted(pipe.Names()),
		"omitted_because":   "the dataset has a payer but no payee, so there is no transaction graph",
		"secs_per_tick":     *secsPerTick,
		"transactions":      len(rows),
		"findings_raised":   sc.Findings(),
		"span_days":         float64(rows[len(rows)-1].tick-base) / 86400,
	}
	if err := record.WriteJSON(*out, "meta.json", meta); err != nil {
		log.Fatal(err)
	}

	printReport(rep, pipe.Names(), len(rows), sc.Findings(), *secsPerTick, elapsed, *out)
}

// readCSV parses only the five columns that mean anything here. The file has
// 394; the rest are Vesta's engineered features, which are exactly the kind of
// thing this project's detectors are not allowed to depend on.
func readCSV(path string) ([]txn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rd := csv.NewReader(f)
	rd.ReuseRecord = true
	rd.FieldsPerRecord = -1

	head, err := rd.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	need := []string{"TransactionID", "isFraud", "TransactionDT", "TransactionAmt", "card1"}
	for _, n := range need {
		if _, ok := col[n]; !ok {
			return nil, fmt.Errorf("column %q missing — is this the IEEE-CIS transaction file?", n)
		}
	}

	var out []txn
	var skipped int
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		id, e1 := strconv.ParseUint(rec[col["TransactionID"]], 10, 64)
		dt, e2 := strconv.ParseUint(rec[col["TransactionDT"]], 10, 64)
		amt, e3 := strconv.ParseFloat(rec[col["TransactionAmt"]], 64)
		card, e4 := strconv.ParseUint(rec[col["card1"]], 10, 32)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || amt < 0 {
			// A row missing a payer, a time or an amount cannot be scored.
			// Counted and reported rather than silently dropped.
			skipped++
			continue
		}
		out = append(out, txn{
			id:    obs.TxID(id),
			tick:  obs.Tick(dt),
			payer: obs.AgentID(card),
			// Amounts carry three decimals on some rows (currency conversion),
			// so round to minor units rather than truncating — integer money
			// is a determinism requirement everywhere else in this project.
			amount: int64(math.Round(amt * 100)),
			fraud:  rec[col["isFraud"]] == "1",
		})
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "skipped %d unparseable rows\n", skipped)
	}
	return out, nil
}

// omitted names the detectors this dataset structurally cannot exercise.
func omitted(active []string) []string {
	var out []string
	for _, n := range []string{"velocity", "fanout", "cycle"} {
		if !slices.Contains(active, n) {
			out = append(out, n)
		}
	}
	return out
}

func checksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func printReport(r metrics.Report, active []string, n, findings int, spt float64, elapsed time.Duration, out string) {
	fmt.Printf("\n═══ REAL DATA — IEEE-CIS (Vesta), %s transactions ═══════════\n\n", comma(n))
	fmt.Printf("  detectors scored    %s\n", strings.Join(active, ", "))
	fmt.Printf("  not scorable        %s (no payee in this dataset)\n",
		strings.Join(omitted(active), ", "))
	fmt.Printf("  time scale          1 tick = %gs (velocity window = %gs)\n", spt, 300*spt)
	// Findings is the first thing to read when a detector scores at chance:
	// zero findings is a detector that never fired, which is a completely
	// different failure from one that fired and was wrong.
	fmt.Printf("  findings raised     %s\n", comma(findings))
	fmt.Printf("  scored in           %s\n\n", elapsed.Round(time.Millisecond))

	fmt.Printf("  transactions        %s\n", comma(r.Transactions))
	fmt.Printf("  fraudulent          %s  (%.2f%%)\n", comma(r.Fraudulent), r.Prevalence*100)
	fmt.Printf("  AUC-PR              %.4f\n", r.AUCPR)
	fmt.Printf("  best F1             %.4f  at tau=%.2f\n", r.BestF1.F1, r.BestF1.Tau)
	fmt.Printf("    precision         %.4f\n", r.BestF1.Precision)
	fmt.Printf("    recall            %.4f\n", r.BestF1.Recall)
	fmt.Printf("    FP per 1k legit   %.2f\n", r.BestF1.FPPer1kLegit)

	fmt.Printf("\n  baselines on the SAME real transactions\n")
	for _, b := range r.Baselines {
		fmt.Printf("    %-14s    %.4f\n", b.Name, b.AUCPR)
	}
	fmt.Printf("\n  wrote %s/metrics.json and meta.json\n\n", out)
}

func comma(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
