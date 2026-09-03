package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yug/upi-city/internal/obs"
	"github.com/yug/upi-city/internal/truth"
)

// The header is the real IEEE-CIS column order, truncated after the columns
// this command reads. Rows exercise the cases that actually bite: a
// three-decimal amount from currency conversion, a fraud label, and a row
// with an unparseable payer.
const sampleCSV = `TransactionID,isFraud,TransactionDT,TransactionAmt,ProductCD,card1
2987000,0,86400,68.5,W,13926
2987240,1,90193,37.098,C,13413
2987002,0,86469,59.0,W,4663
2987999,0,90500,25.0,C,
`

func writeSample(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sample.csv")
	if err := os.WriteFile(p, []byte(sampleCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReadCSVMapsFieldsAndRounds pins the conversion every real-data number
// rests on. A silent change here would move the published figures without
// touching a line of detection code.
func TestReadCSVMapsFieldsAndRounds(t *testing.T) {
	rows, err := readCSV(writeSample(t))
	if err != nil {
		t.Fatalf("readCSV: %v", err)
	}
	// The row with an empty card1 has no payer and cannot be scored.
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (one row has no payer)", len(rows))
	}

	byID := map[obs.TxID]txn{}
	for _, r := range rows {
		byID[r.id] = r
	}

	// $68.50 must become 6850 minor units, not 6849 or 685.
	if got := byID[2987000].amount; got != 6850 {
		t.Errorf("amount 68.5 became %d, want 6850", got)
	}
	// Three decimals appear on converted amounts; rounding, not truncation,
	// is what keeps money integral without losing half a unit every time.
	if got := byID[2987240].amount; got != 3710 {
		t.Errorf("amount 37.098 became %d, want 3710 (rounded)", got)
	}
	if !byID[2987240].fraud {
		t.Error("isFraud=1 did not survive parsing")
	}
	if byID[2987000].fraud {
		t.Error("isFraud=0 became fraudulent")
	}
	if got := byID[2987240].payer; got != obs.AgentID(13413) {
		t.Errorf("payer %d, want 13413", got)
	}
	if got := byID[2987000].tick; got != obs.Tick(86400) {
		t.Errorf("tick %d, want 86400", got)
	}
}

// TestReadCSVRejectsTheWrongFile guards against pointing this at
// test_transaction.csv, which is the same shape but carries no isFraud column
// at all. Scoring it would produce a confident, fully-populated report in
// which every transaction is legitimate.
func TestReadCSVRejectsTheWrongFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nolabels.csv")
	body := "TransactionID,TransactionDT,TransactionAmt,card1\n2987000,86400,68.5,13926\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCSV(p); err == nil {
		t.Fatal("a file with no isFraud column must be refused, not scored as all-legitimate")
	}
}

// TestFileLabelsInventNothing checks the Labels implementation reports absence
// as absence. Archetype and intensity are simulator concepts; claiming them
// for external data would put invented structure into the one result whose
// value is that it has none.
func TestFileLabelsInventNothing(t *testing.T) {
	l := fileLabels{fraud: map[obs.TxID]bool{7: true}}
	if l.Label(7) != truth.LabelRingMule {
		t.Error("a fraudulent id must report as fraudulent")
	}
	if l.Label(8) != truth.LabelNormal {
		t.Error("an unlisted id must report as normal")
	}
	if l.Incident(7) != 0 {
		t.Error("external data has no injected incidents")
	}
	if l.Intensity(1234) != 0 {
		t.Error("external data has no attack ramp")
	}
}

// TestOmittedNamesTheUntestedDetectors keeps the report honest about coverage:
// a run that scores one of three detectors must say which two it did not.
func TestOmittedNamesTheUntestedDetectors(t *testing.T) {
	got := omitted([]string{"velocity"})
	if len(got) != 2 || got[0] != "fanout" || got[1] != "cycle" {
		t.Fatalf("omitted = %v, want [fanout cycle]", got)
	}
	if len(omitted([]string{"velocity", "fanout", "cycle"})) != 0 {
		t.Fatal("nothing is omitted when all three run")
	}
}
