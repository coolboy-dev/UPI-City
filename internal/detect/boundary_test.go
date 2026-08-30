package detect

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDetectorsCannotSeeGroundTruth is the ground-truth firewall, enforced by
// the compiler rather than by discipline.
//
// Every headline number this project reports — precision, recall, false
// positive rate, detection latency — is only meaningful if the detection layer
// could not have consulted the answers. Reviewing code for that property by
// eye does not scale and does not survive a refactor: the dangerous leak is
// not `if tx.Label == Fraud`, it is a feature quietly derived from
// label-conditioned state three call levels down.
//
// So the claim is made structural. This walks the real dependency graph. If
// internal/detect ever gains a path to internal/sim or internal/truth, the
// build fails, and it fails at the moment the import is added rather than
// after the numbers have been published.
func TestDetectorsCannotSeeGroundTruth(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	const (
		simPkg   = "github.com/yug/upi-city/internal/sim"
		truthPkg = "github.com/yug/upi-city/internal/truth"
		chaosPkg = "github.com/yug/upi-city/internal/chaos"
	)

	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		switch strings.TrimSpace(p) {
		case truthPkg:
			t.Errorf("GROUND TRUTH LEAK: internal/detect depends on %s.\n"+
				"Detectors must never be able to reference labels; every metric "+
				"in this project depends on that separation.", truthPkg)
		case simPkg:
			t.Errorf("LEAK RISK: internal/detect depends on %s.\n"+
				"The engine holds labelled transactions, so this opens a path to "+
				"ground truth. Detectors may only consume internal/obs.", simPkg)
		case chaosPkg:
			t.Errorf("LEAK RISK: internal/detect depends on %s.\n"+
				"Scenario definitions reveal what the injected attack is.", chaosPkg)
		}
	}
}

// TestObsIsSelfContained checks the other side of the firewall: the
// observable surface must not reach back into the engine, or the boundary
// above could be satisfied while still exposing labels.
func TestObsIsSelfContained(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "../obs").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "github.com/yug/upi-city/internal/") &&
			p != "github.com/yug/upi-city/internal/obs" {
			t.Errorf("internal/obs must import nothing else from internal/, but depends on %s", p)
		}
	}
}
