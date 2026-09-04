# Bring your own detector

UPI City grades fraud detectors. This directory is where detectors it did not
write come to be graded.

The interface is two files and three fields. Your detector does not have to be
written in Go, does not have to import anything from this project, and does not
have to be trusted.

```
events.jsonl  →  [ your detector, any language ]  →  scores.jsonl
                                                           ↓
labels.jsonl  ──────────────────────────────────→  just grade
```

---

## The contract

### 1. You are given `events.jsonl`

One settled payment per line, in settlement order.

```json
{"t":4102,"s":4103,"id":88213,"f":47,"to":9,"b":2,"a":34500,"st":0,"l":120,"d":771,"n":false}
```

| field | meaning |
|---|---|
| `t`  | tick the payment was **initiated** |
| `s`  | tick it **settled** — this is "now" for an online detector |
| `id` | transaction id; the only thing joining the three files |
| `f`  | payer account id |
| `to` | payee account id |
| `b`  | bank id |
| `a`  | amount in **paise** (integer — ₹345.00 is `34500`) |
| `st` | `0` success, non-zero failure |
| `l`  | settlement latency, milliseconds |
| `d`  | device id |
| `n`  | true if the payer has never paid this payee before |

That is the whole world you get. There is no label field, and there is no
hidden one.

### 2. You return `scores.jsonl`

One line per payment. Any order.

```json
{"tx":88213,"s":0.83}
```

`s` is suspicion in `[0,1]`. **Not** a yes/no decision — thresholding happens
later, offline, which is what lets the grader sweep every operating point and
draw the trade-off curve for the run being demonstrated rather than a different
one. Values outside `[0,1]` are clamped and reported.

A payment you do not score counts as zero. That is deliberate: declining to
speak is a decision, and a detector silent on the fraud should not be credited
for its silence.

Name the file `scores-YOURNAME.jsonl` and drop it beside the recording.

### 3. Optionally declare yourself

`scores-YOURNAME.meta.json`:

```json
{"detector": "sklearn IsolationForest", "saw_labels": false, "note": "..."}
```

This changes no number. It exists so the leaderboard can mark entrants that
were shown the answer key, because a ceiling presented as a competitor is the
most misleading row a benchmark can print.

---

## Run it

```bash
nix develop
just record          # make a run: events.jsonl + labels.jsonl, kept apart
just entrants        # score it with the reference entrants
just grade           # the leaderboard
just headtohead      # watch two detectors disagree, live
```

---

## Adapting your detector to this data

Your detector has to understand the shape of the traffic before it can work
here. This is not tuning — you cannot score data you cannot read — and it is
worth being precise about the difference.

**Units.** Money is integer paise. Time is ticks, not seconds; one tick is one
step of the simulation, and the recorded `world.json` carries `ms_per_tick`.
Account ids are dense integers, not strings.

**Traffic density, which is the one that actually bites.** In this simulator
agents transact continuously. A typical account sends many payments inside a
few hundred ticks, so any window you pick will contain plenty of history.

Real payment traffic does not look like that, and this project has already been
burned by exactly this assumption. Its own velocity detector requires 12
payments from one account inside a 300-tick window before it will speak — free
here, and on 590,540 real card payments only **3 cards out of 13,553** could
ever satisfy it. The detector did not error. It went silent, and every test
still passed.

So: check what your detector's windows and minimum-activity gates imply about
how busy an account is, and know that a number which is free in this simulator
may be impossible outside it. That failure is silent in both directions.

**Causality.** Score payment N using payments 1..N−1 and nothing after. The
built-in detectors are held to this by construction — they are handed one event
at a time and cannot see the rest of the file. Nothing in the grader can check
that you did the same, which is stated plainly rather than papered over: a
detector that peeks at the future will look superb and mean nothing. The
held-out split below is what makes it unprofitable rather than impossible.

---

## The rule about tuning

There is a line, and it matters more than any other paragraph here.

**Adapting** is reading the format correctly. Amounts are paise, so divide by
100. Windows are in ticks. Fine, necessary, do it.

**Tuning** is trying twenty settings and keeping whichever scored highest.
That does not build a better detector; it finds the settings that fit *this
recording*. Point it at another run and it collapses.

So the recordings come in two kinds:

- **Practice** — tune freely. The answer key is included, and it is yours to
  use however you like.
- **Graded** — you get `events.jsonl`. You do not get `labels.jsonl`. Your
  score comes from here.

Twenty attempts on the practice set buy nothing on the graded set, because the
graded set is different traffic from a different seed. That is the entire
defence, and it is the same discipline this project already applies to its own
isotonic calibrator, which is fitted on seeds 1–5 and reported on seeds 6–10.

---

## Why the answers are in a separate file

`internal/detect` is barred from importing `internal/truth`, and a test walks
the real dependency graph and fails the build on violation. That is a genuine
guarantee, and it covers exactly one thing: code compiled into this binary.

An external entrant is handed `events.jsonl` and nothing else. `labels.jsonl`
is never passed to it and lives in a process it does not share. There is no
rule to obey, and therefore no rule to break.

That is the difference between a project and a testbed. The import rule
protects code the author reviewed; the file boundary protects against detectors
nobody here has read.

---

## The reference entrants

Two are included, and they exist for different reasons.

### `sklearn_iforest.py` — an ordinary competitor

Off-the-shelf `IsolationForest` over ten obvious causal features: how big, how
often, how new, how concentrated. Unsupervised, so it never sees a label at
all. Fitted on the first 20% of the stream, then frozen.

Nothing about it is tuned, and no score was consulted while writing it. It is
here to establish what a generic anomaly detector achieves out of the box, so
that every other row is measured against a real alternative rather than against
nothing.

**If you want a better number, improve the features.** That is the invitation.
The interface is three fields wide and the grader does not care who is calling
it.

### `supervised_ceiling.py` — the entrant allowed to cheat

Gradient boosting, trained **with labels** on one run and scored on a different
one.

A leaderboard where everything sits near chance has two readings that look
identical from outside: the detectors are bad, or the benchmark is broken.
Trivial baselines settle only the bottom of the scale — they prove chance is
0.036, so a low score is genuinely low. They say nothing about whether a high
score was ever available.

The ceiling settles that. It reaches **AUC-PR 0.937 against a 0.036 base
rate**, having been fitted on seed 7 and graded on seed 42, which it never saw.
So the fraud in this traffic is learnable, the labels are consistent, and the
grader can tell signal from noise. Every low score beneath it is a finding
rather than a symptom.

It is marked `!` on the leaderboard and it is not a competitor.

---

## What the reference run says

```
entrant                      AUC-PR vs chance   precision   recall    FP/1k
────────────────────────────────────────────────────────────────────────────
! supervised_ceiling         0.9369     26.0x       0.998    0.276     0.03
  upi-city (fused)           0.5439     15.1x       0.959    0.266     0.43
  sklearn_iforest            0.0433      1.2x       0.001    0.000    10.36
────────────────────────────────────────────────────────────────────────────
  random                     0.0362      1.0x
  always-flag                0.0361      1.0x
  amount-rank                0.1359      3.8x
```

123,622 payments, 4,463 fraudulent. Every entrant graded on the same payments
by the same code; precision, recall and FP are at a fixed 1% review budget, so
the columns compare.

Two things worth reading twice.

**Off-the-shelf anomaly detection barely clears chance here, and loses to
sorting by amount.** IsolationForest at 1.2× sits below the `amount-rank`
baseline at 3.8×. Fraud in this network is a *topology* — money moving in
cycles through accounts that fan in — and a generic outlier detector over
per-transaction features has no way to see that. This is a real result about
what anomaly detection is for, not a bug in the entrant.

**The gap between the home detector and the ceiling is the honest measure of
how much is left on the table.** 15× against 26×: the fused detectors find
something genuine, and a model with labels finds substantially more.

---

## Head to head, and why the thresholds differ

`just headtohead` replays a recording with two detectors scoring the same
payments at the same instant, colouring each one by who caught it. Red is fraud
neither detector reached — the category no per-detector scoreboard can ever
show you, because each one is only measured against what it caught.

The challenger is **not** judged at the same threshold. It is judged at the
score that makes it flag the same *share* of traffic.

This matters, and the first version got it wrong. A raw score means whatever
the detector that produced it decided it should mean. This project's fused
score is a weighted sum with a strongly negative bias, so 0.15 is already
selective. A normalised IsolationForest score is diffuse, and 0.15 lands near
the middle of its distribution. Judged at one shared threshold the challenger
flagged 15,487 legitimate payments against this project's 141 — a number that
measures where two authors put their scales, not which detector is better.

Matched on flag rate, the same run reads:

```
both caught       2        my false positives     145
only UPI City  1773        its false positives    219
only sklearn      1
BOTH MISSED    1194
```

Same analyst budget, and the question becomes the one a risk team actually
asks: given the same amount of human attention, who finds more fraud?
