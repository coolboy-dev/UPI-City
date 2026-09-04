// Command grade scores any detector against a recorded run, whoever wrote it
// and whatever language it is written in.
//
// ─── What this makes possible ───────────────────────────────────────────────
//
// Everything else in this project grades detectors that live in this
// repository. That is a closed loop in a second sense: not only were the fraud
// and the detector designed by the same person, so was the grader. A number
// produced that way is an assertion about one author's care, and there is no
// way for a reader to check it except by reading the code.
//
// This command opens that loop. A recorded run is already two files — the
// payments, and the answers, deliberately kept apart. Hand the payments to any
// program at all, take back a score per payment, and grade it here against the
// answers it never saw. The entrant does not have to be written in Go, does not
// have to import anything from this project, and does not have to be trusted.
//
// ─── Why the file boundary is stronger than the import rule ─────────────────
//
// internal/detect is barred from importing internal/truth, and a test walks the
// dependency graph to enforce it. That is a real guarantee and it covers
// exactly one thing: code compiled into this binary.
//
// An external entrant gets handed events.jsonl. labels.jsonl is never passed to
// it, never mentioned to it, and lives in a process it does not share. There is
// no rule to obey and therefore no rule to break — which is what makes the
// guarantee survive contact with a detector nobody here reviewed.
//
// ─── The one thing this cannot check ────────────────────────────────────────
//
// Nothing here proves an entrant computed its features causally. A program that
// scores payment N using payment N+1 will look excellent and mean nothing, and
// it would be graded happily. That is a real limit, stated rather than papered
// over: the contract in detectors/README.md asks for causal features, and the
// held-out split is what makes violating it unprofitable rather than impossible.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yug/upi-city/internal/chaos"
	"github.com/yug/upi-city/internal/detect"
	"github.com/yug/upi-city/internal/metrics"
	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/record"
	"github.com/yug/upi-city/internal/risk"
	"github.com/yug/upi-city/internal/runner"
	"github.com/yug/upi-city/internal/truth"
)

// reviewBudget is the share of traffic a human queue is assumed to absorb.
// Every entrant is reported at the same one, because precision and recall are
// meaningless to compare between detectors that flagged wildly different
// volumes — the classic way to win a benchmark without detecting anything.
const reviewBudget = 0.01

// entrant is one detector's submitted answers.
type entrant struct {
	name   string
	scores map[obs.TxID]float64
	// declared is what the entrant says about itself, read from a sidecar
	// meta file if present. Purely descriptive — it changes no number, and it
	// exists so a leaderboard can mark which rows were allowed to see labels.
	declared entrantMeta
	report   metrics.Report
	// budget is the operating point at a fixed review budget, computed by
	// rank. See atBudget for why the tau grid cannot do this job.
	budget metrics.Operating
}

// entrantMeta is the optional scores-NAME.meta.json written beside a score
// file.
type entrantMeta struct {
	Detector  string `json:"detector"`
	SawLabels bool   `json:"saw_labels"`
	Note      string `json:"note"`
}

// fileLabels answers ground-truth questions from the recorded label file.
//
// Identical in spirit to the real-data path's version: truth arrives as a
// lookup table rather than from a world that generated it, which is what lets
// one scoring path serve simulated runs, recorded runs and external datasets.
type fileLabels struct {
	rows      map[obs.TxID]record.LabelRow
	arch      map[obs.AgentID]truth.Archetype
	intensity map[obs.Tick]float64
}

func (l fileLabels) Label(id obs.TxID) truth.Label         { return l.rows[id].Label }
func (l fileLabels) Incident(id obs.TxID) truth.IncidentID { return l.rows[id].Incident }

// Archetype is answered from a map built by joining labels to events on the
// sender, because the label file records an archetype per TRANSACTION while
// the interface asks per AGENT. Rebuilding the join here rather than storing
// agent archetypes in the recording keeps events.jsonl free of anything a
// detector must not see.
func (l fileLabels) Archetype(id obs.AgentID) truth.Archetype { return l.arch[id] }

// Intensity is how far an attack had ramped. The recording stores it per
// transaction and this interface asks per tick, so the join is done once at
// load time; a tick with no fraud on it reports zero.
func (l fileLabels) Intensity(t obs.Tick) float64 { return l.intensity[t] }

func main() {
	dir := flag.String("dir", "results/seed-42", "recorded run: events.jsonl + labels.jsonl + world.json")
	out := flag.String("out", "", "where to write leaderboard.json (default: -dir)")
	sweep := flag.String("sweep", "", "print a cross-difficulty table from already-graded subdirectories")
	flag.Parse()

	if *sweep != "" {
		printSweep(*sweep)
		return
	}

	if *out == "" {
		*out = *dir
	}

	events, maxAgent, err := loadEvents(filepath.Join(*dir, "events.jsonl"))
	if err != nil {
		log.Fatalf("reading events: %v", err)
	}
	if len(events) == 0 {
		log.Fatal("recording contains no events")
	}

	labels, err := loadLabels(filepath.Join(*dir, "labels.jsonl"))
	if err != nil {
		log.Fatalf("reading labels: %v", err)
	}
	if len(labels) == 0 {
		log.Fatalf("no labels in %s — a run without its answer key cannot be graded", *dir)
	}
	arch, intensity := joinTruth(events, labels)

	world, err := loadWorld(filepath.Join(*dir, "world.json"))
	if err != nil {
		log.Fatalf("reading world.json: %v", err)
	}
	msPerTick := world.MsPerTick
	if msPerTick == 0 {
		msPerTick = 100
	}

	lb := fileLabels{rows: labels, arch: arch, intensity: intensity}

	// ── The home entrant, scored through the SAME path as everyone else ────
	//
	// Re-running the built-in pipeline over the recording rather than reusing
	// the numbers the harness already wrote is deliberate. If the home
	// detector were graded from a different code path than its challengers,
	// any difference between them would be partly an artefact of that, and
	// indistinguishable from a real result.
	fmt.Fprintf(os.Stderr, "scoring the built-in detectors over %s ...\n", *dir)
	homeRows := scoreBuiltin(events, lb, maxAgent)

	// Ground truth is identical for every entrant; only the score column
	// changes. Building one row set and swapping that column is what
	// guarantees it.
	all := []*entrant{{name: "upi-city (fused)", scores: nil}}
	all[0].report = evaluate(homeRows, world.Incidents, world.Seed, msPerTick)
	all[0].budget = atBudget(homeRows, reviewBudget)

	files, err := filepath.Glob(filepath.Join(*dir, "scores-*.jsonl"))
	if err != nil {
		log.Fatal(err)
	}
	sort.Strings(files)
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "scores-"), ".jsonl")
		scores, err := loadScores(f)
		if err != nil {
			log.Fatalf("reading %s: %v", f, err)
		}
		e := &entrant{name: name, scores: scores, declared: loadMeta(f)}
		rows := withScores(homeRows, scores)
		e.report = evaluate(rows, world.Incidents, world.Seed, msPerTick)
		e.budget = atBudget(rows, reviewBudget)
		all = append(all, e)
		fmt.Fprintf(os.Stderr, "  %-28s %d scores\n", name, len(scores))
	}

	if len(all) == 1 {
		fmt.Fprintf(os.Stderr,
			"\nno external entrants found (looked for %s/scores-*.jsonl)\n"+
				"see detectors/README.md for how to submit one\n", *dir)
	}

	printLeaderboard(all, homeRows)
	writeLeaderboard(*out, all, homeRows)
}

// scoreBuiltin runs this project's own detectors over the recorded events.
func scoreBuiltin(events []obs.Event, lb runner.Labels, maxAgent obs.AgentID) metrics.Rows {
	sc := runner.NewScorer(runner.ScoreConfig{
		Pipeline: detect.NewDefault(),
		Fusion:   risk.DefaultFusion(),
		AgentCap: int(maxAgent) + 1,
	})
	for _, e := range events {
		if err := sc.Observe(e, lb); err != nil {
			log.Fatalf("scoring: %v", err)
		}
	}
	return sc.Rows()
}

// withScores clones the row set and replaces the score column.
//
// Everything else — label, archetype, incident, amount, tick — is ground truth
// or observable fact and is identical for every entrant by construction. A
// payment the entrant did not score counts as zero, which is the honest
// reading: declining to speak is a decision, and a detector that stays silent
// on the fraud should not be rewarded for it.
func withScores(base metrics.Rows, scores map[obs.TxID]float64) metrics.Rows {
	out := make(metrics.Rows, len(base))
	copy(out, base)
	for i := range out {
		s := scores[out[i].TxID]
		out[i].Score = s
		out[i].Raw = s
		out[i].Detector = ""
		out[i].Contributions = nil
	}
	return out
}

func evaluate(rows metrics.Rows, inc []truth.Incident, seed, msPerTick uint64) metrics.Report {
	return metrics.Evaluate(rows, inc, seed, msPerTick)
}

// atBudget picks the operating point by RANK rather than by threshold: take
// the highest-scoring `budget` share of traffic and measure what that catches.
//
// ─── Why not reuse metrics.AtReviewBudget ───────────────────────────────────
//
// That function searches a fixed tau grid of 0.00, 0.01 … 1.00 for the point
// whose flag rate fits the budget. That works for the built-in detectors,
// whose scores are spread across the range, and breaks silently for an entrant
// whose scores are confident.
//
// The supervised ceiling is exactly that case: 3.3% of its scores exceed 0.99
// and its maximum is 0.9997. At tau=0.99 it flags 3.3% of traffic, over
// budget; at tau=1.00 it flags nothing at all. No point on the grid can
// express "the top 1%", so the search returned a zero row and a detector with
// AUC-PR 0.937 was reported at precision 0.000 — which reads as a broken
// detector rather than a grid too coarse to describe it.
//
// A review budget is a rank concept to begin with. An analyst team says "we
// can look at one percent of traffic", not "we can look at everything above
// 0.99", so ranking is both the correct implementation and the honest one.
//
// Ties are broken by TxID so the cut is deterministic, and rows scored exactly
// zero are never flagged however much budget is left over: zero means the
// entrant declined to speak, and spending budget on silence would credit a
// detector for an arbitrary slice of the traffic it ignored.
func atBudget(rows metrics.Rows, budget float64) metrics.Operating {
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := rows[idx[a]], rows[idx[b]]
		if ra.Score != rb.Score {
			return ra.Score > rb.Score
		}
		return ra.TxID < rb.TxID
	})

	k := int(float64(len(rows)) * budget)
	var op metrics.Operating
	flagged := make([]bool, len(rows))
	for rank := 0; rank < k && rank < len(idx); rank++ {
		if rows[idx[rank]].Score <= 0 {
			break
		}
		flagged[idx[rank]] = true
	}

	var legit int
	for i, r := range rows {
		fraud := r.Fraudulent()
		if !fraud {
			legit++
		}
		switch {
		case flagged[i] && fraud:
			op.TP++
		case flagged[i] && !fraud:
			op.FP++
		case !flagged[i] && fraud:
			op.FN++
		default:
			op.TN++
		}
	}
	if op.TP+op.FP > 0 {
		op.Precision = float64(op.TP) / float64(op.TP+op.FP)
	}
	if op.TP+op.FN > 0 {
		op.Recall = float64(op.TP) / float64(op.TP+op.FN)
	}
	if op.Precision+op.Recall > 0 {
		op.F1 = 2 * op.Precision * op.Recall / (op.Precision + op.Recall)
	}
	if legit > 0 {
		op.FPPer1kLegit = float64(op.FP) * 1000 / float64(legit)
	}
	if len(rows) > 0 {
		op.FlagRate = float64(op.TP+op.FP) / float64(len(rows))
	}
	// The score at the cut, reported so a reader can see where the budget
	// actually landed for this entrant.
	if k > 0 && k <= len(idx) {
		op.Tau = rows[idx[k-1]].Score
	}
	return op
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

func loadEvents(path string) ([]obs.Event, obs.AgentID, error) {
	var out []obs.Event
	var max obs.AgentID
	err := eachLine(path, func(line []byte) error {
		var r record.EventRow
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		if r.Frm > max {
			max = r.Frm
		}
		if r.To > max {
			max = r.To
		}
		out = append(out, obs.Event{
			Tick: r.T, SettleTick: r.S, TxID: r.ID,
			From: r.Frm, To: r.To, Bank: r.Bnk, AmountP: r.Amt,
			Status: obs.Status(r.St), LatencyMs: r.Lat,
			DeviceID: r.Dev, IsNewFrom: r.New,
		})
		return nil
	})
	return out, max, err
}

func loadLabels(path string) (map[obs.TxID]record.LabelRow, error) {
	rows := map[obs.TxID]record.LabelRow{}
	err := eachLine(path, func(line []byte) error {
		var r record.LabelRow
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		rows[r.TxID] = r
		return nil
	})
	return rows, err
}

// joinTruth re-derives the two per-agent and per-tick views the metrics layer
// needs from the per-transaction rows on disk.
//
// The recording deliberately stores neither: an agent-archetype table would be
// a compact description of who the mules are, and writing one next to the
// events is exactly the sort of convenience that turns into a leak.
func joinTruth(events []obs.Event, labels map[obs.TxID]record.LabelRow) (
	map[obs.AgentID]truth.Archetype, map[obs.Tick]float64,
) {
	arch := make(map[obs.AgentID]truth.Archetype, 512)
	intensity := make(map[obs.Tick]float64, 1024)
	for _, e := range events {
		r, ok := labels[e.TxID]
		if !ok {
			continue
		}
		arch[e.From] = r.Archetype
		// Intensity belongs to the tick the payment was CREATED on, matching
		// how the live scorer reads it: the ramp is a property of the
		// attacker, not of when the network happened to settle the payment.
		if r.Intensity > intensity[e.Tick] {
			intensity[e.Tick] = r.Intensity
		}
	}
	return arch, intensity
}

func loadScores(path string) (map[obs.TxID]float64, error) {
	out := map[obs.TxID]float64{}
	var bad int
	err := eachLine(path, func(line []byte) error {
		var r struct {
			TxID  obs.TxID `json:"tx"`
			Score float64  `json:"s"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		// Scores outside [0,1] are clamped rather than rejected. The grading
		// code is rank-based for AUC-PR but threshold-based everywhere else,
		// and a stray -3 would silently move an entrant to the bottom of every
		// sweep for a reason that has nothing to do with detection.
		switch {
		case r.Score < 0:
			r.Score, bad = 0, bad+1
		case r.Score > 1:
			r.Score, bad = 1, bad+1
		}
		out[r.TxID] = r.Score
		return nil
	})
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "  note: clamped %d out-of-range scores in %s\n", bad, filepath.Base(path))
	}
	return out, err
}

func loadMeta(scorePath string) entrantMeta {
	p := strings.TrimSuffix(scorePath, ".jsonl") + ".meta.json"
	b, err := os.ReadFile(p)
	if err != nil {
		return entrantMeta{}
	}
	var m entrantMeta
	_ = json.Unmarshal(b, &m)
	return m
}

func loadWorld(path string) (record.WorldFile, error) {
	var w record.WorldFile
	b, err := os.ReadFile(path)
	if err != nil {
		return w, err
	}
	return w, json.Unmarshal(b, &w)
}

func eachLine(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

func printLeaderboard(all []*entrant, rows metrics.Rows) {
	prev := rows.Prevalence()

	fmt.Printf("\n═══ LEADERBOARD — %s payments, %s fraudulent (%.2f%%) ═══\n\n",
		comma(len(rows)), comma(rows.Positives()), prev*100)

	// Ranked by AUC-PR, which is the one figure that does not depend on where
	// an entrant happened to put its threshold. Everything to its right does,
	// and is reported at a fixed 1% review budget so the columns compare.
	ranked := make([]*entrant, len(all))
	copy(ranked, all)
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].report.AUCPR > ranked[j].report.AUCPR
	})

	fmt.Printf("%-26s %8s %9s %11s %8s %8s\n",
		"entrant", "AUC-PR", "vs chance", "precision", "recall", "FP/1k")
	fmt.Println(strings.Repeat("─", 76))

	for _, e := range ranked {
		lift := 0.0
		if prev > 0 {
			lift = e.report.AUCPR / prev
		}
		op := e.budget
		mark := "  "
		if e.declared.SawLabels {
			mark = "! " // saw the answer key; a ceiling, not a competitor
		}
		fmt.Printf("%s%-24s %8.4f %8.1fx %11.3f %8.3f %8.2f\n",
			mark, e.name, e.report.AUCPR, lift, op.Precision, op.Recall, op.FPPer1kLegit)
	}

	// The trivial scorers, from the same report, so the floor is on the same
	// page as the entrants rather than in a different document.
	fmt.Println(strings.Repeat("─", 76))
	for _, b := range all[0].report.Baselines {
		lift := 0.0
		if prev > 0 {
			lift = b.AUCPR / prev
		}
		fmt.Printf("  %-24s %8.4f %8.1fx\n", b.Name, b.AUCPR, lift)
	}

	fmt.Printf("\n  every entrant graded on the SAME %s payments, by the same code.\n",
		comma(len(rows)))
	fmt.Printf("  precision/recall/FP are at a fixed 1%% review budget, so the\n")
	fmt.Printf("  columns compare; AUC-PR is threshold-free.\n")

	for _, e := range all {
		if e.declared.SawLabels {
			fmt.Printf("\n  ! %s was given the answer key. It is the CEILING —\n"+
				"    what is achievable on this traffic, not a fair competitor.\n", e.name)
			if e.declared.Note != "" {
				fmt.Printf("    %s\n", e.declared.Note)
			}
		}
	}
	fmt.Println()
}

func writeLeaderboard(dir string, all []*entrant, rows metrics.Rows) {
	type row struct {
		Name       string  `json:"name"`
		AUCPR      float64 `json:"aucpr"`
		VsChance   float64 `json:"vs_chance"`
		Precision  float64 `json:"precision_at_1pct"`
		Recall     float64 `json:"recall_at_1pct"`
		FPPer1k    float64 `json:"fp_per_1k_at_1pct"`
		SawLabels  bool    `json:"saw_labels"`
		Note       string  `json:"note,omitempty"`
	}
	prev := rows.Prevalence()
	out := struct {
		Transactions int     `json:"transactions"`
		Fraudulent   int     `json:"fraudulent"`
		Prevalence   float64 `json:"prevalence"`
		Entrants     []row   `json:"entrants"`
	}{len(rows), rows.Positives(), prev, nil}

	for _, e := range all {
		lift := 0.0
		if prev > 0 {
			lift = e.report.AUCPR / prev
		}
		op := e.budget
		out.Entrants = append(out.Entrants, row{
			Name: e.name, AUCPR: e.report.AUCPR, VsChance: lift,
			Precision: op.Precision, Recall: op.Recall, FPPer1k: op.FPPer1kLegit,
			SawLabels: e.declared.SawLabels, Note: e.declared.Note,
		})
	}
	if err := record.WriteJSON(dir, "leaderboard.json", out); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  wrote %s\n\n", filepath.Join(dir, "leaderboard.json"))
}

// printSweep renders one leaderboard per difficulty as a single table.
//
// ─── Why the curve is the product, not the number ───────────────────────────
//
// A benchmark that reports one score cannot distinguish a good detector from
// an easy attack, and every reader is left to trust that whoever chose the
// scenario chose fairly. Sweeping difficulty removes the need to trust anyone:
// a detector that holds its lift as the attacker gets quieter has earned
// something, and one that collapses between two adjacent columns has revealed
// exactly which assumption it was standing on.
//
// Lift against chance is the column to read, not raw AUC-PR. Prevalence falls
// as the attack gets quieter — a subtler ring simply moves less money through
// fewer accounts — so raw AUC-PR drops for two unrelated reasons at once and
// reading it directly would credit difficulty for rarity.
func printSweep(root string) {
	type board struct {
		Transactions int     `json:"transactions"`
		Fraudulent   int     `json:"fraudulent"`
		Prevalence   float64 `json:"prevalence"`
		Entrants     []struct {
			Name      string  `json:"name"`
			AUCPR     float64 `json:"aucpr"`
			VsChance  float64 `json:"vs_chance"`
			SawLabels bool    `json:"saw_labels"`
		} `json:"entrants"`
	}

	levels := chaos.DifficultyNames()
	boards := map[string]board{}
	var present []string
	for _, lv := range levels {
		b, err := os.ReadFile(filepath.Join(root, lv, "leaderboard.json"))
		if err != nil {
			continue
		}
		var bd board
		if err := json.Unmarshal(b, &bd); err != nil {
			continue
		}
		boards[lv] = bd
		present = append(present, lv)
	}
	if len(present) == 0 {
		log.Fatalf("no graded difficulties under %s — run `just difficulty` first", root)
	}

	// Entrant order is taken from the first level so every row lines up, and
	// an entrant missing from a later level shows a gap rather than shifting
	// the column under it.
	var names []string
	seen := map[string]bool{}
	for _, lv := range present {
		for _, e := range boards[lv].Entrants {
			if !seen[e.Name] {
				seen[e.Name] = true
				names = append(names, e.Name)
			}
		}
	}

	const w = 16 // column width: "0.544 (15.1x)" plus padding

	fmt.Printf("\n═══ CREDIBILITY UNDER INCREASING DIFFICULTY ═══════════════════════\n\n")
	fmt.Printf("  AUC-PR, and in brackets its lift over chance.\n")
	fmt.Printf("  BOTH are shown because neither is sufficient on its own: a quieter\n")
	fmt.Printf("  attack moves less money through fewer accounts, so prevalence falls\n")
	fmt.Printf("  as difficulty rises and lift alone would credit a detector for the\n")
	fmt.Printf("  fraud simply having become rarer. 1.0x is chance.\n\n")

	fmt.Printf("%-26s", "entrant")
	for _, lv := range present {
		fmt.Printf("%*s", w, lv)
	}
	fmt.Println()
	fmt.Println(strings.Repeat("─", 26+w*len(present)))

	for _, n := range names {
		mark := "  "
		for _, e := range boards[present[0]].Entrants {
			if e.Name == n && e.SawLabels {
				mark = "! "
			}
		}
		fmt.Printf("%s%-24s", mark, n)
		for _, lv := range present {
			found := false
			for _, e := range boards[lv].Entrants {
				if e.Name == n {
					fmt.Printf("%*s", w, fmt.Sprintf("%.3f (%.1fx)", e.AUCPR, e.VsChance))
					found = true
					break
				}
			}
			if !found {
				fmt.Printf("%*s", w, "—")
			}
		}
		fmt.Println()
	}

	fmt.Println(strings.Repeat("─", 26+w*len(present)))
	fmt.Printf("%-26s", "  prevalence (chance)")
	for _, lv := range present {
		fmt.Printf("%*s", w, fmt.Sprintf("%.2f%%", boards[lv].Prevalence*100))
	}
	fmt.Println()
	fmt.Printf("%-26s", "  fraudulent payments")
	for _, lv := range present {
		fmt.Printf("%*s", w, comma(boards[lv].Fraudulent))
	}
	fmt.Println()

	fmt.Printf("\n  Each level turns a knob known to defeat a specific signal:\n")
	fmt.Printf("    hops        ⇒ past the cycle search depth of 4, a ring is invisible\n")
	fmt.Printf("    mule rate   ⇒ no burst, so nothing for velocity to measure\n")
	fmt.Printf("    amount      ⇒ payments sized like ordinary traffic\n")
	fmt.Printf("    ring size   ⇒ too few payers to look unlike a merchant\n")
	fmt.Printf("    ramp        ⇒ slow enough that per-agent baselines absorb it\n")

	// The whole reason a ceiling is on this table. Without it, a column where
	// everything sits at chance is ambiguous between "the detectors failed"
	// and "there was nothing there to find", and no amount of care in the
	// unsupervised rows can resolve it.
	hardest := present[len(present)-1]
	for _, e := range boards[hardest].Entrants {
		if !e.SawLabels {
			continue
		}
		fmt.Printf("\n  Read the ceiling row before concluding anything from a 1.0x above it.\n")
		fmt.Printf("  At %q it still reaches AUC-PR %.3f, so the fraud is present and\n", hardest, e.AUCPR)
		fmt.Printf("  findable there — an entrant at chance in that column failed to find\n")
		fmt.Printf("  fraud that was demonstrably available, which is a different and much\n")
		fmt.Printf("  stronger statement than \"it scored badly\".\n")
		break
	}
	fmt.Println()
}

func comma(n int) string {
	s := fmt.Sprintf("%d", n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
