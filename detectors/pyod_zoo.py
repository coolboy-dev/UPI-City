#!/usr/bin/env python3
"""Six off-the-shelf anomaly detectors from PyOD, each entered separately.

─── The hypothesis this is built to test ──────────────────────────────────────

The first entrant here, scikit-learn's IsolationForest, scored 1.2x chance —
barely above random, and below a baseline that just sorts payments by size.

The obvious reading is that the entrant was weak. The more interesting reading
is that the entire FAMILY it belongs to cannot work here, and those two are
distinguishable by experiment.

Fraud in this network is not an unusual payment. It is money moving in a circle
through a chain of accounts, where every individual hop looks completely
ordinary — ordinary size, ordinary timing, ordinary counterparty. The crime is a
shape in the graph, and a detector that scores each row on its own features has
no representation in which that shape exists.

If that is right, then swapping IsolationForest for a different row-wise
detector should change nothing, because the limitation is not the algorithm. So
this file runs six genuinely different ones over identical features:

    IForest   random splits — isolation depth
    LOF       local density against nearest neighbours
    HBOS      per-feature histograms, assumes independence
    KNN       raw distance to the k-th neighbour
    PCA       reconstruction error off the principal subspace
    CBLOF     distance to the nearest large cluster

Six different notions of "unusual". If all six land at chance, the conclusion is
about the approach rather than any one implementation — and that is a stronger,
more useful finding than a single bad score.

─── What would falsify it ─────────────────────────────────────────────────────

One of them scoring well. That would mean the signal IS reachable from
per-transaction features and the first entrant simply missed it, which would be
a genuine discovery by the testbed and would deserve to be reported as loudly
as the negative result.

The features are held identical across all six precisely so the comparison
means something: same inputs, same causal ordering, same warmup, same scaling.
Only the model differs.

─── Rules, unchanged ──────────────────────────────────────────────────────────

None of these ever open labels.jsonl. All features are causal — payment N is
described using payments 1..N-1 only. Each model is fitted on a warmup prefix
and then frozen, so no model scores a payment using a fit that saw its future.
"""

import argparse
import sys
import time

import features


# Fitting on the full stream would be both slow and a peek at the future, so
# every model is fitted on the warmup prefix. LOF and KNN are additionally
# subsampled for fitting because both are superlinear in the fitted set and
# 25,000 rows of neighbour search is minutes of wall clock for no extra signal.
NEIGHBOUR_FIT_CAP = 6000


def build_models(seed):
    """Return [(name, estimator, fit_cap)], or exit with a clear message."""
    try:
        from pyod.models.iforest import IForest
        from pyod.models.lof import LOF
        from pyod.models.hbos import HBOS
        from pyod.models.knn import KNN
        from pyod.models.pca import PCA
        from pyod.models.cblof import CBLOF
    except ImportError as e:
        sys.exit(f"PyOD not available ({e}) — run detectors/setup_pyod.sh first, "
                 f"then use .venv-pyod/bin/python")

    return [
        # Defaults throughout. Nothing here is tuned, and no score was consulted
        # while choosing a parameter — a tuned entrant would be measuring how
        # long its author spent rather than what the algorithm can do.
        ("pyod_iforest", IForest(n_estimators=200, random_state=seed, n_jobs=-1), None),
        ("pyod_hbos",    HBOS(),                                                  None),
        ("pyod_pca",     PCA(random_state=seed),                                  None),
        ("pyod_cblof",   CBLOF(random_state=seed, n_jobs=-1),                     None),
        ("pyod_lof",     LOF(n_jobs=-1),                          NEIGHBOUR_FIT_CAP),
        ("pyod_knn",     KNN(n_jobs=-1),                          NEIGHBOUR_FIT_CAP),
    ]


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--dir", default="results/seed-42",
                    help="recording to score; reads events.jsonl, never labels.jsonl")
    ap.add_argument("--warmup", type=float, default=0.2,
                    help="fraction of the stream used to fit, then frozen")
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--only", default="",
                    help="comma-separated subset of model names to run")
    args = ap.parse_args()

    import numpy as np

    events = f"{args.dir}/events.jsonl"
    print(f"reading {events}", file=sys.stderr)
    tx_ids, rows = features.build_features(events)
    if not rows:
        sys.exit("no events found")
    X = np.asarray(rows, dtype=np.float64)

    n_fit = min(max(1000, int(len(X) * args.warmup)), len(X))
    print(f"{len(X):,} payments, {X.shape[1]} features; "
          f"fitting on the first {n_fit:,} ({args.warmup:.0%})", file=sys.stderr)

    # Standardise on the warmup slice only. Several of these models are
    # distance-based, so a feature measured in paise would otherwise dominate
    # every neighbour computation purely by being numerically large — and using
    # the whole file's mean and variance would let the tail set the scale for
    # its own beginning, which is a peek at the future even though no label is
    # involved.
    mu = X[:n_fit].mean(axis=0)
    sd = X[:n_fit].std(axis=0)
    sd[sd == 0] = 1.0
    Xs = (X - mu) / sd

    wanted = {s.strip() for s in args.only.split(",") if s.strip()}
    for name, model, fit_cap in build_models(args.seed):
        if wanted and name not in wanted:
            continue

        fit_rows = Xs[:n_fit]
        note = ""
        if fit_cap and n_fit > fit_cap:
            # Deterministic stride rather than a random draw: the subsample has
            # to be reproducible, and an evenly spaced one also spans the whole
            # warmup period instead of clustering wherever the RNG landed.
            step = n_fit // fit_cap
            fit_rows = Xs[:n_fit:step]
            note = f" (fitted on {len(fit_rows):,} of {n_fit:,}, superlinear)"

        t0 = time.time()
        try:
            model.fit(fit_rows)
            raw = model.decision_function(Xs)
        except Exception as e:
            print(f"  {name:<16} FAILED: {e}", file=sys.stderr)
            continue
        elapsed = time.time() - t0

        # Scale to [0,1] using the warmup range only, for the same reason the
        # standardisation uses it: the run's own tail must not set its scale.
        lo, hi = float(raw[:n_fit].min()), float(raw[:n_fit].max())
        span = (hi - lo) or 1.0
        scaled = [(v - lo) / span for v in raw]

        out = f"{args.dir}/scores-{name}.jsonl"
        # Warmup rows were scored by a model fitted on them, which is not a
        # measurement — reported as zero, matching how UPI City's own detectors
        # stay silent until they have history.
        features.write_scores(out, tx_ids, scaled, zero_before=n_fit)
        features.write_meta(
            out,
            detector=f"PyOD {type(model).__name__}",
            saw_labels=False,
            note=(f"unsupervised, PyOD defaults, causal features, fitted on the "
                  f"first {args.warmup:.0%} then frozen{note}"),
        )
        nz = sum(1 for i in range(n_fit, len(scaled)) if scaled[i] > 0.01)
        print(f"  {name:<16} {elapsed:6.1f}s  {nz:>7,} above 0.01{note}",
              file=sys.stderr)

    print(f"wrote scores to {args.dir}/scores-pyod_*.jsonl", file=sys.stderr)


if __name__ == "__main__":
    main()
