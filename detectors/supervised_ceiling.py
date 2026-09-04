#!/usr/bin/env python3
"""The entrant that is allowed to cheat, on purpose.

─── Why a leaderboard needs a ceiling ─────────────────────────────────────────

A benchmark where everything scores near chance has two possible readings, and
from the outside they are indistinguishable:

    1. the detectors are bad
    2. the benchmark is broken — the fraud is not actually findable, or the
       grading code is wrong

Trivial baselines only settle the bottom of the scale. They establish that
chance is 0.036, so a low score is genuinely low. They say nothing about
whether a HIGH score was ever available, which is the reading that matters
when the honest result is "nothing worked."

So this entrant is handed the answer key for a DIFFERENT run and asked to
generalise to the graded one. If it scores well, the fraud in this traffic is
learnable, the labels are consistent, and the grader can tell signal from
noise — which turns every low score beneath it into a finding rather than a
symptom.

─── Why the training data is a different seed ─────────────────────────────────

Training and scoring on the same run would produce a spectacular number that
means nothing: the model would have memorised which transaction ids are fraud.
Fitting on seed 7 and reporting on seed 42 is the same discipline the isotonic
calibrator already follows — fit on seeds 1-5, report on 6-10 — pointed at a
different question.

The engine is deterministic, so the two runs are genuinely different worlds
rather than two samples of one, and nothing about seed 42 was available while
this model was being fitted.

─── What this is NOT ──────────────────────────────────────────────────────────

Not a proposal to deploy gradient boosting, and not a competitor. It is a
measuring stick, and the leaderboard prints it with a marker saying so. A
production fraud team does have labels — months late, via chargebacks — so this
is not a fantasy, but it is a different problem from scoring a payment as it
settles, which is what every other row on the board is doing.
"""

import argparse
import json
import sys

import features


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--train", default="results/train-seed-07",
                    help="run to LEARN from — its labels are read")
    ap.add_argument("--score", default="results/seed-42",
                    help="run to be graded on — its labels are never opened")
    ap.add_argument("--out", default=None)
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args()

    if args.train.rstrip("/") == args.score.rstrip("/"):
        sys.exit("refusing to train and score on the same run: the model would "
                 "memorise it and the resulting number would mean nothing")

    out = args.out or f"{args.score}/scores-supervised_ceiling.jsonl"

    try:
        import numpy as np
        from sklearn.ensemble import HistGradientBoostingClassifier
    except ImportError:
        sys.exit("scikit-learn not available — run inside `nix develop`")

    # ── learn from a different world ──────────────────────────────────────
    print(f"training on {args.train} (labels ARE read here)", file=sys.stderr)
    tr_ids, tr_rows = features.build_features(f"{args.train}/events.jsonl")
    tr_labels = features.load_labels(f"{args.train}/labels.jsonl")
    X = np.asarray(tr_rows, dtype=np.float64)
    y = np.asarray([tr_labels.get(t, 0) for t in tr_ids], dtype=np.int32)
    print(f"  {len(X):,} payments, {int(y.sum()):,} fraudulent "
          f"({y.mean():.2%})", file=sys.stderr)

    model = HistGradientBoostingClassifier(
        max_iter=200,
        learning_rate=0.1,
        # Fraud is ~4% here, so the majority class would otherwise dominate
        # the loss and the model would learn to predict "clean" everywhere.
        class_weight="balanced",
        random_state=args.seed,
    )
    model.fit(X, y)

    # ── score a world it has never seen ───────────────────────────────────
    print(f"scoring {args.score} (labels NOT opened)", file=sys.stderr)
    sc_ids, sc_rows = features.build_features(f"{args.score}/events.jsonl")
    Xs = np.asarray(sc_rows, dtype=np.float64)
    proba = model.predict_proba(Xs)[:, 1]

    features.write_scores(out, sc_ids, proba)
    features.write_meta(
        out,
        detector="sklearn HistGradientBoostingClassifier",
        saw_labels=True,
        note=(f"trained on {args.train} with labels, scored on {args.score} "
              f"which it never saw; establishes what is achievable, not what "
              f"a fair entrant achieved"),
    )
    print(f"wrote {len(sc_ids):,} scores to {out}", file=sys.stderr)


if __name__ == "__main__":
    main()
