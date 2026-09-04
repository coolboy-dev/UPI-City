"""Causal feature extraction from a UPI City events file.

Shared by every reference entrant so they differ in exactly one thing: the
model. Two detectors built on different features are not a comparison, they are
two anecdotes, and the leaderboard would be reporting feature engineering while
appearing to report models.

─── The causality rule ────────────────────────────────────────────────────────

Scoring payment N may use payments 1..N-1 and nothing after. Every accumulator
below is read BEFORE the current payment is folded in, so a payment never
contributes to its own description.

This is the rule that is easiest to break by accident and hardest to notice
when you do: a model fitted on statistics that already contain the answer looks
superb on the file it was fitted to and collapses on anything else. UPI City's
own detectors are held to the same rule by construction — they are handed one
event at a time, in settlement order, and cannot see the rest of the file.

─── What is deliberately NOT here ─────────────────────────────────────────────

No feature reads labels.jsonl. That file is not opened by anything in this
directory, and the grader runs in a separate process. The absence is the
guarantee — there is no rule for an entrant to obey, because the answers are
simply not present.
"""

import json
import math
from collections import defaultdict, deque


# Matches the built-in velocity detector's window, so entrants see comparable
# evidence rather than a wider or narrower slice of history. Not tuned; no
# score was consulted in choosing it.
RATE_WINDOW = 300

FEATURE_NAMES = [
    "log_amount",
    "payer_txn_count",
    "log_ticks_since_last",
    "log_amount_vs_payer_mean",
    "payer_rate_in_window",
    "payee_txn_count",
    "payee_distinct_payers",
    "payee_new_payer_ratio",
    "is_new_counterparty",
    "status",
]


def build_features(path, progress=None):
    """Stream an events file, returning (tx_ids, feature_rows).

    One forward pass, bounded memory per agent. Returns plain Python lists;
    callers convert to numpy so this module stays importable without it.
    """
    tx_ids = []
    rows = []

    payer_count = defaultdict(int)
    payer_last_tick = {}
    payer_amt_mean = defaultdict(float)
    payer_recent = defaultdict(deque)  # ticks within RATE_WINDOW

    payee_count = defaultdict(int)
    payee_payers = defaultdict(set)

    with open(path) as f:
        for n, line in enumerate(f):
            if not line.strip():
                continue
            e = json.loads(line)

            tick = e["s"]
            payer = e["f"]
            payee = e["to"]
            amt = e["a"]

            # ── read state as it stands BEFORE this payment ───────────────
            n_sent = payer_count[payer]
            mean_amt = payer_amt_mean[payer]
            since_last = tick - payer_last_tick.get(payer, tick)

            recent = payer_recent[payer]
            cutoff = tick - RATE_WINDOW
            while recent and recent[0] < cutoff:
                recent.popleft()
            rate = len(recent)

            # Size relative to what THIS payer normally sends. A flat
            # "large payment" feature permanently marks every big merchant,
            # which is the mistake the built-in velocity detector documents
            # at length.
            amt_ratio = (amt / mean_amt) if mean_amt > 0 else 1.0

            pc = payee_count[payee]
            pp = len(payee_payers[payee])

            rows.append([
                math.log1p(amt),
                float(n_sent),
                math.log1p(max(since_last, 0)),
                math.log1p(amt_ratio),
                float(rate),
                float(pc),
                float(pp),
                # Fan-in novelty: how much of this receiver's traffic comes
                # from accounts it has never been paid by. A merchant's
                # customers return; a collection mule's do not. This is the
                # single signal the real-card dataset structurally could not
                # supply, and the reason a simulator exists in this project.
                (pp / pc) if pc else 1.0,
                1.0 if e.get("n") else 0.0,
                float(e.get("st", 0)),
            ])
            tx_ids.append(e["id"])

            # ── fold in, visible to LATER payments only ───────────────────
            payer_count[payer] = n_sent + 1
            payer_last_tick[payer] = tick
            # EWMA, not a running mean: bounded memory, and it tracks a
            # genuinely changing spend level rather than averaging it away.
            payer_amt_mean[payer] = amt if mean_amt == 0 else 0.98 * mean_amt + 0.02 * amt
            recent.append(tick)
            payee_count[payee] = pc + 1
            payee_payers[payee].add(payer)

            if progress and n and n % 250_000 == 0:
                progress(n)

    return tx_ids, rows


def load_labels(path):
    """Read labels.jsonl into {tx_id: is_fraud}.

    ONLY the supervised ceiling may call this, and it is why that entrant is
    marked as having seen the answer key. A competing entrant that called this
    would be scoring its own homework.
    """
    out = {}
    with open(path) as f:
        for line in f:
            if not line.strip():
                continue
            r = json.loads(line)
            out[r["tx"]] = 1 if r.get("l", 0) != 0 else 0
    return out


def write_scores(path, tx_ids, scores, zero_before=0):
    """Write scores.jsonl: one {"tx", "s"} per payment, in file order."""
    with open(path, "w") as f:
        for i, tx in enumerate(tx_ids):
            s = 0.0 if i < zero_before else float(scores[i])
            s = 0.0 if s < 0 else (1.0 if s > 1 else s)
            f.write(json.dumps({"tx": tx, "s": round(s, 6)}) + "\n")


def write_meta(score_path, detector, saw_labels=False, note=""):
    """Declare what this entrant is, beside its scores.

    Changes no number. It exists so the leaderboard can mark which rows were
    allowed to see the answer key, because a ceiling presented as a competitor
    is the most misleading row a benchmark can print.
    """
    meta = {"detector": detector, "saw_labels": saw_labels, "note": note}
    with open(score_path.replace(".jsonl", ".meta.json"), "w") as f:
        json.dump(meta, f, indent=2)
        f.write("\n")
