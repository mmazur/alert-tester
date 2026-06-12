<!--
TEMPLATE for /aro-hcp-test-alerts reports. Copy this file to
reports/<YYYY-MM-DD>-<batch-name>.md and fill every {{...}} placeholder.
Delete sections that don't apply. Delete this comment block in the final report.

Audience: people who care about how the ALERTS behaved — not how atest works.
- No tables: prefer nested bullet lists. Tables get ugly fast and editors
  bicker over column widths.
- If an alert produced no usable data in a region, say so plainly in that
  alert's own section in one sentence. Don't explain the atest internals there.
- Always include firing counts and grouped-firing/incident counts where atest
  produced them. Do not replace counts with symbolic summaries like `F`/`.` or
  yes/no grids unless the counts are also present nearby.
- All atest-internal noise (NODATA diagnosis, transient errors, retries,
  token-refresh fixes, runner classification quirks, log paths, what was
  probed) goes in the LAST section, "Internal Run Info (Ignore)". The reader
  should be able to skip it entirely.
- "modified" means the alert's expression was rewritten before testing
  (e.g. recording rules inlined). Always state whether each alert was modified.
- NODATA does NOT require running extra probe queries unless the user asked
  for diagnosis. Each probe is another atest call against prod — don't burn
  that on your own initiative.
-->

# Alert test report — {{batch-name}}

- **Generated:** {{YYYY-MM-DD HH:MM TZ}}
- **Window:** {{from}} → {{to}} ({{N}} days)
- **Grafana:** {{grafana-url}}
- **Datasource type:** {{hcps | services}}
- **Regions:** {{uksouth, eastus2, australiaeast}}

If the user asks for a multi-week window, report one continuous multi-week
evaluation unless they explicitly asked for per-week breakdowns.

## Summary

{{1–3 sentences on what was tested and the headline finding (e.g. "X fires
constantly in eastus2", "Y never fires anywhere", "all alerts quiet in
window").}}

Per alert, one bullet:

- **{{AlertName}}** — modified: {{yes/no}}. {{One line on overall behavior across regions, e.g. "fired in 2/3 regions; quiet in australiaeast." or "no usable data in any region."}}
- **{{AlertName2}}** — modified: {{yes/no}}. {{…}}

*A "**modified**" alert is one whose PromQL was rewritten before testing —
typically because the alert references Prometheus recording rules that atest
can't see, so their definitions were inlined recursively until only raw metrics
remained. Modified alerts are approximate (e.g. long-lookback records like
`avg_over_time(foo[30d:5m])` can't be faithfully reconstructed from a 7-day
fetch); their numbers are indicative, not exact.*

## Alerts under test

### {{AlertName}}

- **Source:** {{alert name in ~/aro/ARO-HCP/observability/alerts/<file>.yaml | pasted PromQL | PR #1234 | path}}
- **Declared `for:`** {{e.g. 1m → tested 1m,10m,15m,20m | 15m → tested 15m,30m,45m,60m | absent → tested 5m,10m,15m,30m}}
- **`--incident-group-by`:** {{labels | none}}
- **Modified:** {{no — uses raw metrics only | yes — recording rules <names> were inlined}}
- **Thresholds / comparisons:** {{inline threshold | local comparison `--lt 2736`, `--lt 5472`, `--lt 8208` | threshold sweep details}}
- **Expression evaluated:**
  ```promql
  {{the exact -q expression passed to atest; if using local comparison, omit the threshold here and list it above}}
  ```
- **Results per region:**
  - **uksouth** — {{e.g. "`--lt 2736`: 57 firings @ for=15m, 13 @ for=30m (1 grouped incident); `--lt 5472`: no firings at any tested for: value" | "no firings at any tested threshold/for value" | "no usable data — <one-sentence reason>" | "run did not complete"}}
  - **eastus2** — {{…}}
  - **australiaeast** — {{…}}

{{If the alert had no usable data in some region(s), give the reason in ONE
sentence here, plain English, no atest jargon. E.g.:
"In australiaeast the underlying metric `apiserver_validating_admission_policy_check_total`
isn't emitted, so this alert had nothing to evaluate."
Save the technical detail for "Internal Run Info (Ignore)".}}

### {{AlertName2}}

{{same shape}}

## Observations

{{Optional. Things worth a human's eye: alerts that look mistuned (fire
constantly or never anywhere), big per-region divergence, recording-rule
approximations that materially changed the picture, etc. Skip this section
entirely if there's nothing to add.}}

## Internal Run Info (Ignore)

{{Everything an alert author doesn't need to know. Skip with a clear conscience
unless you're debugging atest itself or investigating an oddity.}}

- **Batch:** `{{batch-name}}`
- **atest:** `{{git describe / commit, or "./atest" build date}}`
- **Standard flags:** `--delay-resolution-by 5m --chunk-size 1h` ({{any per-alert overrides}})
- **Threshold/comparison flags:** {{none | `--lt 2736`, `--lt 5472`, `--lt 8208` run as separate invocations for cache reuse and per-threshold logs}}
- **Logs:** `run/{{batch-name}}/<NN>-<AlertName>-<region>.log` (plus `probe-*-<region>.log` for any diagnostic queries).
- **Failures during the run:** {{"none" | bullet per FAILED (alert, region) with reason from the runner RESULT line and log path}}
- **Retries / escalations:** {{"none" | note any transient HTTP errors that resolved on retry, --allow-high-cardinality escalations, --chunk-size shrinks}}
- **NODATA diagnosis** (only when probes were actually run):
  - **{{AlertName}} @ {{region(s)}}** — {{e.g. "base metric exists for `policy=image-registry-allowlist-policy` but only with `enforcement_action=audit` and `validation_result≠denied`; no `deny/denied` series in window. Probe: `run/<batch>/probe-action-result-<region>.log`."}}
- **Anything else surprising about the run mechanics** (e.g. expired token mid-batch, runner classifier missed a status code, etc.)
