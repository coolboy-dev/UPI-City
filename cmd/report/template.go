package main

// pageTpl is the static report. Self-contained by design: no external
// stylesheet, no script, no font, no image reference. The figures are inlined
// SVG, so the file works from disk, from an email attachment, or served by the
// Go binary later without a single change.
const pageTpl = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>UPI City — detection report</title>
<style>
:root{
  --bg:#ffffff; --fg:#16181d; --muted:#5c626e; --line:#e4e6ea;
  --card:#fafbfc; --accent:#2563eb; --warn:#dc2626; --ok:#059669;
  --mono:ui-monospace,SFMono-Regular,Menlo,monospace;
}
@media (prefers-color-scheme:dark){
  :root{--bg:#0f1114;--fg:#e6e8ec;--muted:#9aa1ad;--line:#252932;
        --card:#161920;--accent:#60a5fa;--warn:#f87171;--ok:#34d399;}
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);
     font:15px/1.65 ui-sans-serif,system-ui,-apple-system,sans-serif;}
.wrap{max-width:940px;margin:0 auto;padding:48px 24px 96px}
h1{font-size:26px;margin:0 0 6px;letter-spacing:-.02em}
h2{font-size:19px;margin:52px 0 8px;letter-spacing:-.01em}
h3{font-size:15px;margin:28px 0 6px}
p{margin:10px 0}
.sub{color:var(--muted);margin:0 0 8px}
.note{color:var(--muted);font-size:13.5px;max-width:70ch}
code,.mono{font-family:var(--mono);font-size:13px}
hr{border:0;border-top:1px solid var(--line);margin:36px 0}

.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px;margin:18px 0}
.stat{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px 14px}
.stat .k{font-size:11.5px;text-transform:uppercase;letter-spacing:.05em;color:var(--muted)}
.stat .v{font-size:22px;font-family:var(--mono);margin-top:3px}
.stat .r{font-size:12px;color:var(--muted);font-family:var(--mono)}

.tablewrap{overflow-x:auto;margin:14px 0}
table{border-collapse:collapse;width:100%;font-size:13.5px;min-width:560px}
th,td{text-align:right;padding:7px 10px;border-bottom:1px solid var(--line);
      font-family:var(--mono);white-space:nowrap}
th{font-family:inherit;font-size:12px;color:var(--muted);font-weight:600;
   text-transform:uppercase;letter-spacing:.04em}
td:first-child,th:first-child{text-align:left;font-family:inherit}
tr.best td{background:color-mix(in srgb,var(--accent) 9%,transparent);font-weight:600}
tr.dud td{color:var(--muted)}

figure{margin:20px 0 0;padding:0}
figure svg{max-width:100%;height:auto;display:block;
           border:1px solid var(--line);border-radius:8px;background:var(--bg)}
figcaption{color:var(--muted);font-size:13.5px;margin-top:8px;max-width:70ch}

.callout{border-left:3px solid var(--accent);background:var(--card);
         padding:12px 16px;margin:18px 0;border-radius:0 6px 6px 0}
.callout.warn{border-left-color:var(--warn)}
.callout strong{font-weight:650}
.pass{color:var(--ok);font-weight:600}
.fail{color:var(--warn);font-weight:600}
.badge{display:inline-block;margin-left:8px;padding:1px 7px;border-radius:3px;
       font-size:10px;letter-spacing:.05em;text-transform:uppercase;font-family:var(--mono);
       vertical-align:middle}
.b-llm{background:#1e3a5f;color:#93c5fd}
.b-cache{background:#1e3a2f;color:#166534}
.b-template{background:#2a2620;color:#8a6d3b}
@media (prefers-color-scheme:dark){.b-cache{color:#86efac}.b-template{color:#d6bb8a}}
.foot{margin-top:64px;padding-top:20px;border-top:1px solid var(--line);
      color:var(--muted);font-size:13px}
</style>
</head>
<body>
<div class="wrap">

<h1>UPI City — detection report</h1>
<p class="sub">{{.Summary.Seeds}} seeds · fraud ring injected into a simulated UPI-style payment network</p>
<p class="note">Every figure below is computed against ground truth the detectors were never able to see.
The detection package cannot import the label type; a dependency test fails the build if that ever changes.</p>

<h2>Headline</h2>
<div class="grid">
  <div class="stat"><div class="k">AUC-PR</div><div class="v">{{f3 .Summary.AUCPR.Mean}}</div>
    <div class="r">{{f3 .Summary.AUCPR.Min}} – {{f3 .Summary.AUCPR.Max}}</div></div>
  <div class="stat"><div class="k">Precision</div><div class="v">{{f3 .Summary.Precision.Mean}}</div>
    <div class="r">{{f3 .Summary.Precision.Min}} – {{f3 .Summary.Precision.Max}}</div></div>
  <div class="stat"><div class="k">Recall</div><div class="v">{{f3 .Summary.Recall.Mean}}</div>
    <div class="r">{{f3 .Summary.Recall.Min}} – {{f3 .Summary.Recall.Max}}</div></div>
  <div class="stat"><div class="k">FP / 1k legit</div><div class="v">{{f2 .Summary.FPPer1k.Mean}}</div>
    <div class="r">{{f2 .Summary.FPPer1k.Min}} – {{f2 .Summary.FPPer1k.Max}}</div></div>
  <div class="stat"><div class="k">Latency</div><div class="v">{{f1 .Summary.LatencySec.Mean}}s</div>
    <div class="r">{{f1 .Summary.LatencySec.Min}}s – {{f1 .Summary.LatencySec.Max}}s</div></div>
  <div class="stat"><div class="k">Prevalence</div><div class="v">{{pct .Summary.Prevalence.Mean}}</div>
    <div class="r">{{pct .Summary.Prevalence.Min}} – {{pct .Summary.Prevalence.Max}}</div></div>
</div>

<div class="callout">
<strong>Read the spread, not the mean.</strong>
AUC-PR {{f3 .Summary.AUCPR.Mean}} against a prevalence floor of
{{f3 (index .Baselines 0).Stat.Mean}} is roughly {{ratio .Summary.AUCPR.Mean (index .Baselines 0).Stat.Mean}}×
chance. But precision ranges from {{f3 .Summary.Precision.Min}} to {{f3 .Summary.Precision.Max}}
and latency from {{f1 .Summary.LatencySec.Min}}s to {{f1 .Summary.LatencySec.Max}}s depending only
on which world was simulated — so quoting the best of those would be a choice about presentation
rather than a measurement, which is why no single run is cited anywhere on this page.
</div>

<h3>Trivial scorers, for scale</h3>
<div class="tablewrap"><table>
<thead><tr><th>scorer</th><th>AUC-PR</th><th>min</th><th>max</th></tr></thead>
<tbody>
<tr class="best"><td>these detectors</td><td>{{f3 .Summary.AUCPR.Mean}}</td>
  <td>{{f3 .Summary.AUCPR.Min}}</td><td>{{f3 .Summary.AUCPR.Max}}</td></tr>
{{range .Baselines}}<tr><td>{{.Name}}</td><td>{{f3 .Stat.Mean}}</td>
  <td>{{f3 .Stat.Min}}</td><td>{{f3 .Stat.Max}}</td></tr>{{end}}
</tbody></table></div>

<h3>Integrity checks</h3>
<div class="tablewrap"><table>
<thead><tr><th>check</th><th>result</th></tr></thead>
<tbody>
<tr><td>incidents never detected</td><td>{{.Summary.NeverDetected}} of {{.Summary.IncidentsTotal}}</td></tr>
<tr><td>label-permutation control</td>
  <td>{{if eq .Summary.PermutationPassed .Summary.Seeds}}<span class="pass">{{.Summary.PermutationPassed}}/{{.Summary.Seeds}} passed</span>{{else}}<span class="fail">{{.Summary.PermutationPassed}}/{{.Summary.Seeds}} passed</span>{{end}}</td></tr>
</tbody></table></div>
<p class="note">The permutation control validates the <em>metric</em>: shuffling labels collapses
performance to chance, so the figure genuinely depends on ground truth. It does not prove the
detectors never saw labels — a detector that read the answer would behave identically under
permutation. That claim is enforced separately, by the dependency test.</p>

{{with .Head}}
<hr>
<h2>The decision layer — allow / review / block</h2>
<p class="note">Three outcomes, not two. Wrongly <em>reviewing</em> a payment costs an analyst a few
seconds and the customer never knows; wrongly <em>blocking</em> one is a support call, a chargeback
and possibly a lost customer. A benchmark that collapses them into a single "flag" hides the only
distinction that matters operationally.</p>

{{if .DeployedOK}}
<div class="callout">
<strong>At a {{pct .ReviewBudget}} review budget with a {{pct .MinBlockPrec}} block-precision floor,
this policy catches {{pct .Deployed.Caught}} of fraudulent transactions</strong> — blocking
{{.Deployed.FraudBlocked}} outright at {{pct .Deployed.BlockPrecision}} precision, queueing
{{.Deployed.Reviewed}} for review, and letting {{pct .Deployed.Missed}} through undetected.
It wrongly blocks {{f2 .Deployed.FalseBlockPer1k}} legitimate payments per thousand.
</div>

<div class="tablewrap"><table>
<thead><tr><th>action</th><th>volume</th><th>of which fraud</th><th>precision</th></tr></thead>
<tbody>
<tr><td>block (tau &ge; {{f2 .Deployed.Policy.TauBlock}})</td><td>{{.Deployed.Blocked}}</td>
  <td>{{.Deployed.FraudBlocked}}</td><td>{{pct .Deployed.BlockPrecision}}</td></tr>
<tr><td>review (tau &ge; {{f2 .Deployed.Policy.TauReview}})</td><td>{{.Deployed.Reviewed}}</td>
  <td>{{.Deployed.FraudReviewed}}</td><td>{{pct .Deployed.ReviewPrecision}}</td></tr>
<tr><td>allow</td><td>{{.Deployed.Allowed}}</td><td>{{.Deployed.FraudAllowed}}</td><td>—</td></tr>
</tbody></table></div>
{{else}}
<div class="callout warn"><strong>No deployable policy</strong> at a {{pct .ReviewBudget}} review
budget with block precision above {{pct .MinBlockPrec}}. That is a result, not an error: at this
operating quality the detector cannot block safely without either a bigger review queue or a lower
bar for stopping a customer's payment.</div>
{{end}}

<h3>What a bigger review queue buys</h3>
<div class="tablewrap"><table>
<thead><tr><th>review budget</th><th>fraud caught</th><th>block precision</th><th>false blocks / 1k</th></tr></thead>
<tbody>
{{range .Budgets}}<tr>
<td>{{pct .Budget}}</td>
{{if .Feasible}}<td>{{pct .Caught}}</td><td>{{pct .BlockPrecision}}</td><td>{{f2 .FalseBlockPer1k}}</td>
{{else}}<td colspan="3" style="text-align:left;color:var(--muted)">no safe policy at this budget</td>{{end}}
</tr>{{end}}
</tbody></table></div>

<h3>How loud must an attack be?</h3>
<p class="note">A single recall figure averages over the whole episode, including the loud part, and
so overstates performance against an adversary who simply operates more quietly.</p>
<div class="tablewrap"><table>
<thead><tr><th>attack intensity</th><th>fraud tx</th><th>caught</th><th>recall</th></tr></thead>
<tbody>
{{range .ByIntensity}}{{if .Fraud}}<tr><td>{{f1 .Lo}} – {{f1 .Hi}}</td>
<td>{{.Fraud}}</td><td>{{.Caught}}</td><td>{{pct .Recall}}</td></tr>{{end}}{{end}}
</tbody></table></div>
{{end}}

{{if .Propagation}}
<hr>
<h2>An idea that did not work</h2>
<p class="note">Guilt by association is intuitive: an account dealing mostly with accounts you
already suspect is worth a second look, and laundering rings are densely connected to themselves.
It was built, measured on byte-identical traffic, and it does not pay for itself. It ships behind
a flag, off by default.</p>

<div class="tablewrap"><table>
<thead><tr><th>propagation</th><th>AUC-PR</th><th>precision</th><th>recall</th><th>merchant FP / 1k</th></tr></thead>
<tbody>
{{range .Propagation}}<tr{{if lt .AUCPR 0.05}} class="dud"{{end}}>
<td>{{.Name}}</td><td>{{f3 .AUCPR}}</td><td>{{f3 .Precision}}</td>
<td>{{f3 .Recall}}</td><td>{{f2 .MerchantFP1k}}</td></tr>{{end}}
</tbody></table></div>

<div class="callout warn"><strong>The failure was structural, not statistical.</strong> A large
merchant transacts with hundreds of people, so under an unnormalised sum it inherits risk from
every fraudster who ever bought something from it — becoming maximally suspicious purely by being
popular, then radiating that manufactured suspicion back onto every ordinary customer it serves.
Naive propagation flagged <strong>93% of legitimate merchant traffic</strong> while collapsing
detection to chance.</div>

<p class="note">Degree normalisation and hub damping fix the merchant harm completely — 930 false
positives per thousand down to zero. Detection is <em>still</em> worse than the control, because
the direct detectors already catch the ring and propagation only adds suspicion to the innocent
accounts standing next to it. Both halves of that are reported: the fix works, and the feature
still is not worth having.</p>
{{end}}

{{if .Explanations}}
<hr>
<h2>Audit trail — incident narratives</h2>
<p class="note">The one place a language model is used, and it is deliberately downstream of
everything that matters. Detection stays entirely in rules and statistics, because those are
measurable and reproducible. The model never scores a transaction; it writes up a decision that
has already been made, so the record a compliance reviewer reads is in English rather than in
detector variables.</p>

<div class="callout warn"><strong>Every number the model writes is checked against the facts it
was given, and the output is discarded if any figure was invented.</strong> This is not a
formality. Measured over six incidents, <code>qwen3:1.7b</code> passed the check once and
<code>qwen3.5:9b</code> passed every time. One rejected output, against facts stating 2,481
transactions totalling ₹40,30,400 over 600 seconds, read <em>"24,810 transactions … 86,37,301
rupees … 1 minute of activity"</em> — fluent, confident, and wrong by a factor of ten in three
places. A hallucinated figure in a compliance record is quotable in a way that clumsy prose is
not, so the template stands instead whenever the guard fires.</p></div>

{{range .Explanations}}
<div class="callout">
<strong>{{.Headline}}</strong>
<span class="badge b-{{.Source}}">{{.Source}}{{if .Elapsed}} · {{.Elapsed}}{{end}}</span>
<p style="margin:8px 0 6px">{{.Narrative}}</p>
<p style="margin:0;color:var(--muted)">▸ {{.Action}}</p>
</div>
{{end}}
{{end}}

{{if .Comparison}}
<hr>
<h2>Detector configurations, on byte-identical traffic</h2>
<p class="note">Same seeds, same world, one thing changed at a time. Precision, recall and
false positives are reported at a fixed 1% review budget rather than at best F1 — for a weak
detector, "flag everything" genuinely maximises F1, which is arithmetically correct and
operationally absurd.</p>
<div class="tablewrap"><table>
<thead><tr><th>configuration</th><th>AUC-PR</th><th>precision</th><th>recall</th><th>FP/1k</th><th>latency</th></tr></thead>
<tbody>
{{range .Comparison}}
<tr class="{{if isBest . $.BestConfig}}best{{else if lt .AUCPR 0.02}}dud{{end}}">
  <td>{{.Name}}</td><td>{{f3 .AUCPR}}</td><td>{{f3 .Precision}}</td>
  <td>{{f3 .Recall}}</td><td>{{f2 .FPPer1k}}</td><td>{{f1 .LatencyS}}s</td></tr>
{{end}}
</tbody></table></div>
<div class="callout"><strong>Weighted fusion beats loudest-wins on every axis.</strong>
Taking the maximum score across detectors lets the noisiest one decide the answer; an earlier
version scored <em>worse</em> with three detectors than with the cycle detector alone. A weighted
sum with a strongly negative bias requires signals to agree, which is what corroboration means.</div>
{{end}}

{{range .Comparison}}{{if .Bins}}
<h3>Calibration — expected error {{f4 .ECE}}</h3>
<p class="note">Fitted on seeds 1–5, reported on seeds 6–10 the calibrator never saw. A score of
0.3 should mean "about 30% of transactions like this are fraud". Isotonic regression is monotone,
so calibration cannot reorder anything and therefore cannot improve detection — its value is
entirely interpretive.</p>
<div class="tablewrap"><table>
<thead><tr><th>score bucket</th><th>n</th><th>predicted</th><th>observed</th></tr></thead>
<tbody>
{{range .Bins}}{{if .N}}<tr><td>{{f2 .Lo}} – {{f2 .Hi}}</td><td>{{.N}}</td>
  <td>{{f3 .Predicted}}</td><td>{{f3 .Observed}}</td></tr>{{end}}{{end}}
</tbody></table></div>
{{end}}{{end}}

<hr>
<h2>Figures</h2>
{{range .Figures}}
<figure>{{.SVG}}<figcaption><strong>{{.Title}}.</strong> {{.Note}}</figcaption></figure>
{{end}}

{{if .Seeds}}
<hr>
<h2>Per seed</h2>
<div class="tablewrap"><table>
<thead><tr><th>seed</th><th>prevalence</th><th>AUC-PR</th><th>precision</th><th>recall</th><th>FP/1k</th><th>latency</th><th>missed</th></tr></thead>
<tbody>
{{range .Seeds}}<tr>
<td>{{.Seed}}</td><td>{{pct .Prevalence}}</td><td>{{f3 .AUCPR}}</td>
<td>{{f3 .BestF1.Precision}}</td><td>{{f3 .BestF1.Recall}}</td>
<td>{{f2 .BestF1.FPPer1kLegit}}</td><td>{{f1 .Latency.MedianSeconds}}s</td>
<td>{{.Latency.NeverDetected}}</td></tr>{{end}}
</tbody></table></div>
{{end}}

<div class="callout warn">
<strong>What these numbers are not.</strong> They measure detector performance under
<em>this simulator's</em> assumptions. They are not a claim about real UPI traffic. What the
simulator provides is the ability to compute precision, recall and detection latency
<em>at all</em> — which is impossible on unlabelled production data.
</div>

<p class="foot">Static report — {{.Generated}}. Regenerate with <code>just bench &amp;&amp; just compare &amp;&amp; just report-html</code>.
Every number here is reproducible from a committed seed: <code>just reproduce</code>.</p>

</div>
</body>
</html>
`
