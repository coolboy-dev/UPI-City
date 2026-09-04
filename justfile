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

# score REAL, externally-labelled payments through the same pipeline
# needs data/train_transaction.csv — see data/SOURCES.md for the fetch
realdata:
    go run ./cmd/realdata -secs-per-tick 1 -out results/real-ieee

# the same real data at three time scales, to show the result is not an
# artefact of how real seconds were mapped onto ticks
realdata-sweep:
    #!/usr/bin/env bash
    set -euo pipefail
    go run ./cmd/realdata -secs-per-tick 1   -out results/real-ieee
    go run ./cmd/realdata -secs-per-tick 12  -out results/real-ieee/scale-12
    go run ./cmd/realdata -secs-per-tick 288 -out results/real-ieee/scale-288

# ─── The testbed: grade detectors this project did not write ────────────────
#
# A recording is already two files — the payments, and the answers, kept
# deliberately apart. Hand the payments to any program at all, take back a
# score per payment, and grade it against answers it was never given.

# run the reference entrants over a recording (needs the python in `nix develop`)
#
# train= is a SEPARATE recording, deliberately not one of the seed-NN
# directories the benchmark writes: those hold committed 40,000-tick metrics
# that back published numbers, and a 20,000-tick training run dropped on top of
# one would silently replace a reported result with an unrelated one.
entrants dir="results/seed-42" train="results/train-seed-07":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -f "{{dir}}/events.jsonl" ]; then
      echo "no recording at {{dir}} — making one first"
      just record
    fi
    if [ ! -f "{{train}}/events.jsonl" ]; then
      echo "the ceiling needs a SECOND run to learn from — recording {{train}}"
      go run ./cmd/harness -seed 7 -ticks {{ticks}} -scenario fraud-ring -out {{train}}
    fi
    cd detectors
    # An off-the-shelf anomaly detector, unsupervised, never shown a label.
    python3 sklearn_iforest.py --events ../{{dir}}/events.jsonl \
      --out ../{{dir}}/scores-sklearn_iforest.jsonl
    # The entrant that IS given the answer key — for a different run — so a
    # low score below it reads as a finding rather than a broken harness.
    python3 supervised_ceiling.py --train ../{{train}} --score ../{{dir}}

# PyOD: six off-the-shelf anomaly detectors, entered separately
#
# Installed into a venv that borrows numpy/scipy/scikit-learn/numba from Nix,
# because pip's manylinux wheels cannot find libstdc++ on NixOS and the failure
# looks like a broken detector rather than a broken install.
pyod dir="results/seed-42":
    #!/usr/bin/env bash
    set -euo pipefail
    cd detectors
    [ -x .venv-pyod/bin/python ] || ./setup_pyod.sh
    ./.venv-pyod/bin/python pyod_zoo.py --dir "../{{dir}}"

# grade every entrant found beside a recording, on identical traffic
grade dir="results/seed-42":
    go run ./cmd/grade -dir {{dir}}

# the whole testbed loop: record, run the entrants, publish the leaderboard
leaderboard:
    #!/usr/bin/env bash
    set -euo pipefail
    just entrants
    just grade

# WATCH TWO DETECTORS DISAGREE — same payments, same instant, same budget
# rival="" picks the first scores-*.jsonl found in the recording.
headtohead dir="results/seed-42" rival="sklearn_iforest" tps="300":
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -f "{{dir}}/scores-{{rival}}.jsonl" ]; then
      echo "no scores for {{rival}} in {{dir}} — running the entrants first"
      just entrants
    fi
    CGO_ENABLED=0 go run ./cmd/upicity -replay {{dir}} -rival {{rival}} -tps {{tps}}

# ─── The difficulty dial ────────────────────────────────────────────────────
#
# One score cannot tell you whether a detector is good or whether the fraud was
# easy — those are indistinguishable from a single number. Sweeping difficulty
# turns the score into a curve, and a curve is falsifiable.

# record, score and grade every entrant at each difficulty level
difficulty root="results/difficulty" train="results/difficulty-train":
    #!/usr/bin/env bash
    set -euo pipefail
    for d in easy standard hard brutal; do
      echo "═══ $d ═══════════════════════════════════════════════════════"
      # The graded run, and a SEPARATE run at the same difficulty for the
      # ceiling to learn from. Same difficulty, different seed: a ceiling
      # trained on easy fraud would not be a ceiling for hard fraud.
      go run ./cmd/harness -seed 42 -ticks {{ticks}} -scenario fraud-ring \
        -difficulty "$d" -out "{{root}}/$d" -metrics=false
      go run ./cmd/harness -seed 7 -ticks {{ticks}} -scenario fraud-ring \
        -difficulty "$d" -out "{{train}}/$d" -metrics=false
      ( cd detectors
        python3 sklearn_iforest.py --events "../{{root}}/$d/events.jsonl" \
          --out "../{{root}}/$d/scores-sklearn_iforest.jsonl"
        python3 supervised_ceiling.py --train "../{{train}}/$d" --score "../{{root}}/$d"
        [ -x .venv-pyod/bin/python ] || ./setup_pyod.sh
        ./.venv-pyod/bin/python pyod_zoo.py --dir "../{{root}}/$d" )
      go run ./cmd/grade -dir "{{root}}/$d" >/dev/null
    done
    just difficulty-table

# the cross-difficulty curve: where does each detector stop working?
difficulty-table root="results/difficulty":
    go run ./cmd/grade -sweep {{root}}

# WHICH knob breaks the detector? one variable at a time, same seed
difficulty-ablate seed="42":
    #!/usr/bin/env bash
    set -euo pipefail
    run(){ printf "  %-26s " "$1"; shift; \
      go run ./cmd/harness -seed {{seed}} -ticks {{ticks}} -scenario fraud-ring "$@" 2>/dev/null \
      | grep -E "^AUC-PR" | awk '{printf "AUC-PR %s\n", $2}'; }
    echo "=== one knob at a time, against the standard control ==="
    run "standard (control)"
    run "5 hops"          -hops 5
    run "mule rate 0.04"  -mule-rate 0.04
    run "amount 600"      -mule-amount 600
    run "ring size 8"     -ring-size 8
    run "ramp 4000"       -chaos-ramp 4000
    echo "=== all of them together ==="
    run "hard"            -difficulty hard
