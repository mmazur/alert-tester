---
name: aro-hcp-test-alerts
description: Test one or more ARO HCP Prometheus alerts (or raw PromQL) against prod Grafana with `atest` over the previous Mon–Sun week, across uksouth/eastus2/australiaeast, embedding any recording rules inline. Produces a markdown report in ./reports/. Use when asked to test/characterize alerts, check how an alert would have fired last week, or vet alert PromQL before prod.
---

# aro-hcp-test-alerts

Run `./atest grafana` over a batch of alerts and write a report on how each one
behaved last week. This is a **standalone atest characterization** tool: how
often would these alerts have fired, in which regions, at which `for:` windows.

Work from the repo root. The helper `runner.sh` lives next to this file in `.claude/skills/aro-hcp-test-alerts/`.

## CRITICAL RULES — read first

1. **Never stage, commit or push run logs or reports.**
2. **Run exactly ONE `atest` at a time.** Never background an atest run, never
   run two in parallel, never put a batch in a `for` loop that fans out. Each
   `runner.sh` call must fully finish (you see its `RESULT …` line) before you
   start the next. Parallel atest causes Grafana timeouts and blocking. This is
   non-negotiable.
3. **`--grafana-url` and the datasource type (`hcps` or `services`) MUST be
   provided.** Do not guess either. See *Required inputs* — stop and obtain them
   before running anything.
4. **Failures are non-fatal.** If one (alert, region) fails after retries, mark
   it FAILED and keep going. The goal is a report from everything that worked,
   not an abort.
5. Temporary/per-run logs go in `./run/<batch>/`. The final report goes in
   `./reports/`.
6. **Do not run extra atest queries on your own initiative.** Run exactly the
   batch the user asked for. Do NOT add probes, sanity checks, "diagnose this
   NODATA," sweeps over extra regions, extra `for:` values, extra label
   breakdowns, or any other extra atest invocations beyond the requested batch
   — even if the report template suggests diagnosing NODATA, even if a result
   looks suspicious, even if a probe "would only take a minute." Each atest
   call costs the user real Grafana load and wall-clock minutes. If a result
   needs explanation you can't give from the existing logs, *write that
   honestly into the report* ("not separately probed; likely cause is X but
   unconfirmed") and stop. Ask the user before running anything extra.

## Required inputs (enforce both)

**Grafana URL** — never hardcoded in this skill.
- If the user gave one, use it.
- Otherwise discover it: invoke the `ops:aro-hcp-env-info` skill (installed) and
  take the prod HCP Grafana URL from its output.
- If that skill is unavailable, stop and tell the user to install the ops
  plugin from `https://github.com/openshift-online/aro-ai-tools`, then retry.

**Datasource type** — `hcps` or `services`. The same region has both, so don't
guess from the metric name. **Derive it from where the alert's rule file is
wired up**, not by asking and not by probing Grafana:

```bash
# Given the rule file the alert lives in (e.g. prometheus-prometheusRule.yaml):
grep -rn "<ruleFileBasename>" ~/aro/ARO-HCP/observability/ \
  --include=*.yaml --include=*.yml --include=*.json \
  --include=*.jsonnet --include=*.libsonnet | grep -v "_test.yaml"
```

The kustomization(s) that reference the file encode the datasource in their
name: `*-services.yaml` → `services-<region>`, `*-hcps.yaml` (or an `hcps`
overlay) → `hcps-<region>`. If every referencing file is `*-services.yaml`, the
alert deploys against **services**; likewise for `hcps`. The datasource UID per
region is then `<type>-<region>` (e.g. `services-uksouth`).

Only if the grep is genuinely ambiguous (no referencing file, or the file is
pulled into both an `hcps` and a `services` bundle) fall back to **asking**
(AskUserQuestion). If a batch genuinely mixes types across alerts, derive each
alert's type from its own rule file. Do **not** ask the user for a type you can
determine from the repo, and do **not** burn an atest run or a raw Grafana
query just to discover it.

**Bearer token** — Azure Managed Grafana tokens expire on the order of an hour,
which is shorter than long batches take. **Refresh the token immediately before
each `runner.sh` invocation**, not once per batch. Run them as one command so
they can't drift apart:

```bash
export ATEST_GRAFANA_BEARER_TOKEN=$(az account get-access-token \
  --resource ce34e7e5-485f-4d76-964f-b3d2b16d1e4f \
  --query accessToken -o tsv) && \
.claude/skills/aro-hcp-test-alerts/runner.sh ... # one alert+region
```

A stale token surfaces as Grafana HTTP 500 with an HTML body (not 401), and on a
batch run that means everything after the first ~hour silently fails. Refreshing
per-run is cheap (one `az` call, sub-second when already logged in).

**atest binary** — use `./atest`. If missing, `make build` first.

## Defaults

- **Regions:** `uksouth`, `eastus2`, `australiaeast` (override only if asked).
- **Window:** the previous complete Mon–Sun (7d), unless the user gives a window.
  If the user asks for multiple weeks, run one continuous multi-week window
  unless they explicitly ask for a per-week breakdown.
  Compute it in UTC:
  ```bash
  dow=$(date -u +%u)                                   # 1=Mon … 7=Sun
  this_mon=$(date -u -d "-$((dow-1)) days" +%Y-%m-%d)  # Monday of THIS week
  from=$(date -u -d "$this_mon -7 days" +%Y-%m-%dT00:00:00Z)
  to=$(date   -u -d "$this_mon"          +%Y-%m-%dT00:00:00Z)
  ```
- **Standard flags:** `--delay-resolution-by 5m --chunk-size 1h`. (Azure Managed
  Prometheus resolves on a 5m delay; matching it avoids over-counting. Override
  per alert only if its rule sets a different resolve time.)
- **Per-firing durations:** when the user asks how long firings last (or for any
  report that should characterize firing duration), forward `--verbose` to atest
  via `runner.sh ... -- --verbose`. Without it the log only carries firing
  *counts*; with it, each firing prints a `start -> end (duration, max=…)` line
  under its series. `--verbose` is cache-safe — re-running an already-fetched
  window just re-emits the analysis from cache in seconds, so add it and re-run
  rather than refetching from scratch.

## Step 1 — collect the alerts from the source (freeform)

There is no fixed argument shape. The source can be any of, possibly mixed:

- **An alert name** → grep the definition:
  `grep -rl "alert: <Name>" ARO-HCP/observability/alerts/` then read the
  matching `*-prometheusRule.yaml` block.
- **A PrometheusRule YAML path** → extract every `- alert:` block in it; test
  each alert.
- **A PR** (number/branch/URL) → in `ARO-HCP`, diff against the base
  branch, find added/changed `- alert:` blocks under `observability/alerts/`,
  and test those.
- **Raw PromQL** pasted inline → test it as-is; treat its `for:` as absent and
  its group-by as `cluster` unless the user says otherwise.

For each alert capture: **name**, **expr**, **`for:`** (may be absent),
**aggregation/identity labels** for grouping, and whether the expr references
**recording rules**.

## Step 2 — `for:` values to test

- If the alert **declares** a `for:` value B:
  - Test **B, 5m, 10m, 15m, 20m, 30m, 45m** (unless B overlaps already)
- If **absent**: test **5m, 10m, 15m, 20m, 30m, 45m**.
- Comma-join and pass them together in one `--for` — atest evaluates all off one
  fetch; never split into separate runs.

## Step 3 — incident grouping

Pass `--incident-group-by` so the report can show grouped firings + incident
counts. Derive the labels from the alert's own aggregation (`sum by (…)` /
`max by (…)` in the expr) and identity labels — typically including `cluster`.
If the alert has a `correlationId`/`icmCorrelationId` annotation listing labels,
prefer those. Default to `cluster` when nothing else is clear.

## Step 4 — embed recording rules inline (the "constructed" alerts)

If an alert's expr references a recorded metric (a name defined by `- record:`
somewhere in `ARO-HCP/observability/alerts/*.yaml` — usually names with
`:` in them), atest can't see those records in Grafana. Reconstruct the alert by
**substituting each record's `expr` inline**, recursively (records can reference
other records), until only raw metrics remain.

- This is **approximate** and you must say so in the report. In particular,
  long-lookback records (e.g. `avg_over_time(foo[30d:5m])`,
  `count_over_time(...[3d])`) cannot be faithfully evaluated from a 7-day fetch —
  the embedded form will under/over-count vs prod. Call this out per alert.
- These reconstructed exprs are the **"constructed" alerts**. They are the most
  likely to fail (huge lookbacks, high cardinality, timeouts), so **run them
  FIRST** — fail fast, and a failure here doesn't hold up the simple alerts.
- Keep raw threshold inline in the `-q` expression (e.g. `… > 0`). Do **not**
  use `--gt/--lt/…` (those are for threshold sweeps, not what we want here).

## Step 5 — order the work

**Region-major.** For each region, run all alerts in turn before moving to the
next region. So: `region 1: alert 1, alert 2, … alert N; region 2: alert 1, …`.
This way if the user stops the run mid-batch, they still have a complete
picture of one region rather than a thin slice across all of them.

Within each region, run all **modified** (recording-rule-embedded) alerts
first, then plain alerts — modified alerts are likeliest to fail (timeouts,
high cardinality), so fail fast and don't let a bad one block the simple ones.

Number the runs so logs sort: `<NN>-<AlertShortName>-<region>.log`.

## Step 5a — before you launch: cache-key planning

Before kicking off the batch, look at the alerts together and notice
**multiple alerts that share one PromQL formula and only differ by threshold
and/or `for:`**. This is extremely common (burn-rate alerts: same SLI, four
threshold tiers; queue-depth alerts: same expression, different limits).

The cache key is `(grafana-url, datasource, expr, time-range, step)` — so
two alerts with thresholds inlined into different `-q` strings hit
different cache files even when the underlying fetch is identical. This
silently multiplies cold-cache fetch time.

Solution: when N alerts share one raw expression, lift the threshold out of
`-q` into the `--gt`/`--lt`/`--ge`/`--le` flag and run them as **N separate
runner.sh invocations against the same `-q '<formula>'`**. Each invocation
passes its own single threshold. Result: the first invocation pays the
full fetch cost; the remaining N-1 hit the cache and are seconds, not
minutes.

DO NOT bundle multiple thresholds into one invocation as
`--gt 0.05,0.15,0.30,0.72` — atest's threshold-sweep mode produces a single
combined report shape that's awkward to slot back into per-alert results, and
each alert has its own `for:` and `--incident-group-by` that may differ. The
"separate iterations, shared cache" pattern keeps each run a clean per-alert
report and still gets the full speedup from caching.

(Exception: when the alerts truly are identical except for the threshold AND
the user explicitly asked for a threshold sweep, then `--gt 0.05,0.15,…` in
one run is fine. The default for "test these 4 PR alerts" is separate runs.)

## Step 5b — local-comparator sanity check

Before using a local comparator flag (`--gt`/`--lt`/`--ge`/`--le`), check
whether the raw query and threshold still mean what the user intends:

- The raw `-q` expression must be meaningful without the comparator. For example,
  `count_over_time(metric[1d])` can be fetched raw and compared locally, but an
  already-boolean expression like `foo > 0` or a top-level `and`/`or` expression
  usually should keep its comparison inline unless the query is intentionally
  rewritten with parentheses.
- The comparator threshold must be in the query's possible value range. For
  `count_over_time(metric[1d])` at a 30s scrape interval, the maximum healthy
  value is about `24 * 3600 / 30 = 2880`; `--lt 5472` against the 1d query is
  therefore always true and is not a useful test. For longer lookbacks, scale
  the expected sample count with the range (`2d` => 5760, `3d` => 8640) and then
  compute the intended SLO threshold (e.g. 95% => 5472 for `2d`).
- If the local comparator probably does not make sense, warn the user before
  running and explain the likely outcome. Do not spend prod Grafana time on a
  threshold that is obviously always true/false unless the user confirms they
  intentionally want that characterization.

## Step 6 — run, strictly sequentially

Pick a batch name (kebab, e.g. `kas-burn-2026-06-01`). For **each** (alert,
region), one at a time, **refresh the token and call the helper as one
command**, then **wait for the `RESULT` line**:

```bash
export ATEST_GRAFANA_BEARER_TOKEN=$(az account get-access-token \
  --resource ce34e7e5-485f-4d76-964f-b3d2b16d1e4f --query accessToken -o tsv) && \
.claude/skills/aro-hcp-test-alerts/runner.sh \
  --atest ./atest \
  --grafana-url "$URL" \
  --datasource "${TYPE}-${region}" \
  --expr '<full expr, threshold inline, records embedded>' \
  --for 15m,30m,45m,60m \
  --from "$from" --to "$to" \
  --incident-group-by cluster,namespace \
  --label '<AlertName> @ <region>' \
  --log "run/<batch>/<NN>-<AlertName>-<region>.log"
```

`runner.sh` handles, for that single run:
- transient Grafana errors (429/500/502/503/504/timeouts) → re-run with
  exponential-ish backoff (5s, 15s, 30s, then 60s); atest reuses cached
  chunks and only refetches the failures (default 3 retries, then FAILED —
  it cannot loop forever);
- series-cap abort → re-run with `--allow-high-cardinality`;
- step-widening abort → re-run with a smaller `--chunk-size`;
- and always exits 0 with a final line:
  `RESULT <OK|NODATA|FAILED> label=… log=… [reason=…] [escalations=…]`.

Collect every RESULT line. **Never launch the next run until the current one
printed RESULT.** Do not wrap these in a background job or a parallel construct.
Always tell the user the run directory used for per-run logs (for example,
`run/<batch>/`) so they can inspect raw outputs themselves, even if you are not
generating a final report.

> If you want a hands-off sequential sweep, write the ordered list of runner.sh
> invocations into one bash script that calls them one after another (no `&`, no
> `xargs -P`, no `wait`-on-many) and run that single script in the foreground.
> That is still one atest at a time.

## Step 7 — parse results and write the report

From each log read the `analysis:` block. Lines look like:
- `- for 15m: 57 firings, 57 grouped firings (~incidents)` (current binary), or
  `- for 15m: 57 firings, 57 grouped firings, 1 incidents` (older logs) — accept
  both; pull firings, grouped, and incidents when present.
- `sustained >=90% of window: 1 firings, 1 grouped firings (likely permafailing)`
  → flag this prominently in the report's Notable findings section; these alerts
  are likely to start firing immediately if deployed as-is.
- `- for 1h: never` → zero firings at that `for:`.
- `no data returned` → query returned no series at all (often the wrong
  datasource type — note it, and consider that the alert may simply be quiet).

Copy `reports/TEMPLATE.md` to `reports/<YYYY-MM-DD>-<batch>.md` and fill it:
window, grafana url, datasource type, regions; one results row per (alert,
region, for); the recording-rule caveat for every constructed alert; and a
Notable findings section for sustained/permafiring risks; and a Failures section
listing each FAILED run with its reason + log path.

Then give the user a short summary: run directory, report path if generated,
counts (tested / fired / never / no-data / failed), and anything notable.

## Reminders

- One atest at a time. Always.
- Enforce grafana-url + datasource type up front.
- Constructed alerts run first.
- Don't abort on failure — record it and move on.
- Never commit reports or logs.
