# UPI City — task runner.
# Enter the toolchain first:  nix develop

seed  := "42"
ticks := "20000"
agents := "300"

default:
    @just --list

# WATCH THE NETWORK LIVE: single binary, embedded UI, open localhost:8080
live agents="300" tps="60":
    CGO_ENABLED=0 go run ./cmd/upicity -agents {{agents}} -tps {{tps}}

# build the single portable binary (CGO off: net/http otherwise wants gcc)
build:
    CGO_ENABLED=0 go build -o upicity ./cmd/upicity
    CGO_ENABLED=0 go build ./...

# vet + unit tests, including the ground-truth firewall check
test:
    go vet ./...
    go test ./...

# THE DETERMINISM GATE: same seed must produce the same event stream
gate:
    #!/usr/bin/env bash
    set -euo pipefail
    a=$(go run ./cmd/harness -seed {{seed}} -ticks 10000 -quiet)
    b=$(go run ./cmd/harness -seed {{seed}} -ticks 10000 -quiet)
    echo "run 1: $a"
    echo "run 2: $b"
    if [ "$a" != "$b" ]; then echo "FAIL: same seed produced different streams"; exit 1; fi
    echo "PASS: identical stream hash"

# one quiet run, no chaos, no metrics
run:
    go run ./cmd/harness -seed {{seed}} -ticks {{ticks}} -agents {{agents}} -metrics=false

# THE FULL REPORT: PR trade, baselines, latency, FP breakdown, negative control
report scenario="fraud-ring":
    go run ./cmd/harness -seed {{seed}} -ticks 40000 -scenario {{scenario}}

# inject a scenario and show score separation by ground-truth group
chaos scenario="fraud-ring":
    go run ./cmd/harness -seed {{seed}} -ticks 40000 -scenario {{scenario}} -scores -metrics=false

# every registered scenario, back to back
scenarios:
    #!/usr/bin/env bash
    set -euo pipefail
    for s in fraud-ring bank-outage; do
      echo "########## $s"
      go run ./cmd/harness -seed {{seed}} -ticks 40000 -scenario "$s" -scores -metrics=false
      echo
    done

# 10 seeds in parallel with the spread, plus SVG figures -> results/
bench seeds="10":
    go run ./cmd/bench -seeds {{seeds}} -ticks 40000

# compare detector configurations on BYTE-IDENTICAL traffic, plus calibration
compare seeds="10":
    go run ./cmd/compare -seeds {{seeds}} -fit-seeds 5 -ticks 40000

# pre-generate incident narratives so a live demo never waits on the model
pregenerate model="qwen3.5:9b":
    #!/usr/bin/env bash
    set -euo pipefail
    go run ./cmd/explain -seeds 1,2,3,42 -scenario fraud-ring -model {{model}} -timeout 180s
    go run ./cmd/explain -seeds 42,7   -scenario bank-outage -model {{model}} -timeout 180s

# show what the numeral guard rejects, and the facts it was checking against
explain-debug seed="1" model="qwen3:1.7b":
    go run ./cmd/explain -seeds {{seed}} -model {{model}} -verbose -out /tmp/explain-debug.json

# bundle everything into one self-contained HTML page you can open
report-html:
    go run ./cmd/report

# regenerate every artefact from scratch: metrics, comparison, figures, page
all:
    just bench
    just compare
    just pregenerate
    just propagation
    just validate
    just generalise
    just drift
    just feedback
    just report-html

# REALISM: check the simulated traffic against published NPCI/RBI figures
validate seed="42":
    go run ./cmd/validate -seed {{seed}} -ticks 40000

# GENERALISATION: the attack the detectors were never tuned against
#
# Reported per-detector, because the aggregate hides the finding: the one
# detector that looks useless on the ring is the only one above chance here.
generalise seed="42":
    @echo "=== scam-payout — built AFTER the detectors were frozen, never tuned against ==="
    @for d in velocity fanout cycle velocity,fanout,cycle; do \
        printf "  %-24s " "$d"; \
        go run ./cmd/harness -seed {{seed}} -ticks 40000 -scenario scam-payout -detectors "$d" 2>/dev/null \
          | grep -E "^AUC-PR|^random" | awk '{printf "%s ", $2}'; echo; \
    done
    @echo "  (columns: AUC-PR, then the chance floor)"
    @echo
    @echo "=== the ring they WERE built for, same seed ==="
    @for d in velocity fanout cycle velocity,fanout,cycle; do \
        printf "  %-24s " "$d"; \
        go run ./cmd/harness -seed {{seed}} -ticks 40000 -scenario fraud-ring -detectors "$d" 2>/dev/null \
          | grep -E "^AUC-PR|^random" | awk '{printf "%s ", $2}'; echo; \
    done

# AGEING: does the detector decay as legitimate behaviour moves?
drift:
    go run ./cmd/drift

# FEEDBACK: what an analyst review queue can and cannot teach
feedback:
    go run ./cmd/feedback

# ADVERSARY: let the attacker tune itself against a fixed policy
adversary trials="32" seeds="2":
    go run ./cmd/adversary -trials {{trials}} -seeds {{seeds}} -ticks 25000

# ABLATION: does guilt-by-association help? three arms, identical traffic
propagation seeds="6":
    go run ./cmd/propagation -seeds {{seeds}} -ticks 40000

# re-run every seed and diff against the committed results
reproduce:
    #!/usr/bin/env bash
    set -euo pipefail
    tmp=$(mktemp -d)
    go run ./cmd/bench -seeds 10 -ticks 40000 -out "$tmp" >/dev/null
    if diff -q results/summary.json "$tmp/summary.json"; then
      echo "PASS: reproduced summary.json exactly"
    else
      echo "FAIL: results drifted from what is committed"; exit 1
    fi

# per-archetype wealth: catches an economy that has quietly gone insolvent
diag:
    go run ./cmd/harness -seed {{seed}} -ticks 100000 -diag -metrics=false

# record one run to results/seed-NN/ (events + labels + world, replayable)
record seed=seed scenario="fraud-ring":
    go run ./cmd/harness -seed {{seed}} -ticks {{ticks}} -scenario {{scenario}} -out results/seed-{{seed}}

# REPLAY a recorded run through the identical pipeline — demo insurance
# Records one first if none exists, so this works from a fresh clone.
replay dir="results/seed-42" tps="300":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -f "{{dir}}/world.json" ]; then
      echo "no recording at {{dir}} — making one first"
      just record
    fi
    CGO_ENABLED=0 go run ./cmd/upicity -replay {{dir}} -tps {{tps}}

# 5,000-agent throughput, and the worker sweep that shows sharding does not help
scale agents="5000":
    #!/usr/bin/env bash
    set -euo pipefail
    for w in 1 2 4 8 28; do
      printf "workers=%-3s " "$w"
      CGO_ENABLED=0 go run ./cmd/harness -seed 42 -ticks 2000 -agents {{agents}} \
        -workers "$w" -metrics=false | grep -E "wall clock"
    done

# prove the ground-truth firewall is real: add a leak, watch the build fail
prove-firewall:
    #!/usr/bin/env bash
    set -uo pipefail
    cat > internal/detect/leak_probe.go <<'EOF'
    package detect

    import "github.com/yug/upi-city/internal/truth"

    var probe = truth.LabelRingMule
    EOF
    echo "--- with a deliberate ground-truth import added to internal/detect:"
    go test ./internal/detect/ 2>&1 | grep -E "GROUND TRUTH|FAIL" | head -3
    rm -f internal/detect/leak_probe.go
    echo "--- after removing it:"
    go test ./internal/detect/ 2>&1 | tail -1
