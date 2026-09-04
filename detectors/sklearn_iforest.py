#!/usr/bin/env python3
"""An off-the-shelf scikit-learn entrant, taking the exam like anyone else.

─── What this demonstrates ────────────────────────────────────────────────────

This file reads events.jsonl and writes scores.jsonl. That is the whole
contract. It never opens labels.jsonl, and it could not learn anything from
doing so — the grader is a separate process and the answer key is a separate
file that is simply never passed in.

That is the difference between this project's firewall and a code-level one. A
Go import rule constrains code written in this repository; a file boundary
constrains anybody's detector, in any language, with no cooperation required
and nothing to trust.

─── The rules this file follows ───────────────────────────────────────────────

1. Causal features only. Scoring payment N may use payments 1..N-1 and nothing
   after. Every feature below is accumulated in a single forward pass, so a
   future payment cannot influence a past score. Getting this wrong is the
   easiest way to publish a detector that looks excellent and is worthless.

2. Fit on a warmup prefix, freeze, then score. Refitting continuously would let
   the model adapt to an attack in progress — which is a real design choice,
   but it is also how UPI City's own velocity detector was once trained to
   accept a sustained ring, so it is not the default here.

3. No feature derived from the answer key. IsolationForest is unsupervised and
   never sees a label at all; it looks for statistically unusual rows.

─── Why the features are deliberately ordinary ────────────────────────────────

These are the features anyone would write in an afternoon: how big, how often,
how new, how concentrated. They are not tuned against the simulator and no
attempt was made to make them competitive.

That is the point. This entrant establishes what a generic anomaly detector
achieves out of the box, so that anything else on the leaderboard is measured
against a real alternative rather than against nothing. If you want a better
number, improve the features — the interface is three fields wide and the
grader does not care who is calling it.
"""

import argparse
import sys

import features


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--events", default="results/seed-42/events.jsonl",
                    help="the exam: payments, with no labels in it")
    ap.add_argument("--out", default="results/seed-42/scores-sklearn_iforest.jsonl",
                    help="where to write one score per payment")
    ap.add_argument("--warmup", type=float, default=0.2,
                    help="fraction of the stream used to fit, then frozen")
    ap.add_argument("--trees", type=int, default=200)
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args()

    try:
        import numpy as np
        from sklearn.ensemble import IsolationForest
    except ImportError:
        sys.exit("scikit-learn not available — run inside `nix develop`")

    print(f"reading {args.events}", file=sys.stderr)
    tx_ids, rows = features.build_features(args.events)
    if not rows:
        sys.exit("no events found")
    X = np.asarray(rows, dtype=np.float64)
    print(f"{len(X):,} payments, {X.shape[1]} features", file=sys.stderr)

    # Fit on the opening slice only. Scoring a payment with a model that was
    # fitted on payments that came after it is the subtlest form of leakage
    # available here, and it does not require touching a label to happen.
    n_fit = max(1000, int(len(X) * args.warmup))
    n_fit = min(n_fit, len(X))
    print(f"fitting IsolationForest on the first {n_fit:,} "
          f"({args.warmup:.0%}), then freezing", file=sys.stderr)

    model = IsolationForest(
        n_estimators=args.trees,
        contamination="auto",
        random_state=args.seed,
        n_jobs=-1,
    )
    model.fit(X[:n_fit])

    # score_samples is higher for NORMAL rows, so negate to get suspicion.
    raw = -model.score_samples(X)

    # Map to [0,1] using only the warmup slice's own range. Using the full
    # run's range would let the tail of the file set the scale for its own
    # beginning — harmless-looking, and still a peek at the future.
    lo = float(raw[:n_fit].min())
    hi = float(raw[:n_fit].max())
    span = (hi - lo) or 1.0

    scaled = [(v - lo) / span for v in raw]

    # The warmup rows were scored by a model fitted on them, which is not a
    # measurement. Reported as zero, matching how UPI City's own detectors stay
    # silent until they have history to judge against.
    features.write_scores(args.out, tx_ids, scaled, zero_before=n_fit)
    features.write_meta(
        args.out,
        detector="sklearn IsolationForest",
        saw_labels=False,
        note=(f"unsupervised; fitted on the first {args.warmup:.0%} of the "
              f"stream then frozen, causal features only"),
    )

    nz = sum(1 for i in range(n_fit, len(scaled)) if scaled[i] > 0.01)
    print(f"wrote {len(tx_ids):,} scores to {args.out} ({nz:,} above 0.01)",
          file=sys.stderr)


if __name__ == "__main__":
    main()
