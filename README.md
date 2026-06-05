# alert-tester

Check your alerts' performance before pushing them to prod.

## Key Features
- Smart local caching; you can rerun tests quickly
- Chunked eval to avoid query timeouts and result-breaking steps
- Threshold sweeps without refetching data

## Build

```
make build
```

## Usage

```
atest grafana [flags]
```

### Required flags

| Flag | Description |
|------|-------------|
| `--grafana-url <url>` | Grafana instance URL |
| `--datasource <uid>` | Prometheus datasource UID |
| `-q, --query <promql>` | PromQL expression (repeatable; see [Clauses](#clauses)) |
| `--for <duration>[,<duration>...]` | `for:` durations to test (comma-separated) |
| `--from <timestamp>` | Start of evaluation window |
| `--to <timestamp>` | End of evaluation window |

### Clauses

An alert is built from one or more clauses joined by `--and`/`--or`. A clause is
a `--query` optionally followed by a comparator flag:

| Flag | Meaning |
|------|---------|
| `--gt <v[,v...]>` | `query > v` |
| `--ge <v[,v...]>` | `query >= v` |
| `--lt <v[,v...]>` | `query < v` |
| `--le <v[,v...]>` | `query <= v` |
| `--eq <v[,v...]>` | `query == v` |
| `--ne <v[,v...]>` | `query != v` |
| `-a, --and` | Join the next clause with AND |
| `-o, --or` | Join the next clause with OR |

A comparator can take a comma-separated list to sweep multiple thresholds in one
invocation (e.g. `--gt 0.1,0.5,1.0`).

Examples:

```bash
# Single clause, threshold in the PromQL
-q 'rate(http_errors_total[5m]) > 0.01'

# Same alert, threshold as a flag (enables sweeps; see "Local evaluation" below)
-q 'rate(http_errors_total[5m])' --gt 0.01

# Sweep three thresholds
-q 'rate(http_errors_total[5m])' --gt 0.005,0.01,0.05

# Multi-clause
-q 'up == 0' --and -q 'rate(restarts[5m])' --gt 0.1
```

### Optional flags

| Flag | Default | Description |
|------|---------|-------------|
| `--bearer-token <token>` | `ATEST_GRAFANA_BEARER_TOKEN` env var | Auth token for Grafana |
| `--chunk-size <duration>` | `1h` | Chunk duration for splitting queries |
| `--step <duration>` | `30s` | Query step |
| `--eval-interval <duration>` | `60s` | Prometheus evaluation interval for `for:` logic |
| `--incident-group-by <labels>` | — | Comma- or slash-separated labels for incident grouping (e.g. `cluster,namespace`) |
| `--delay-resolution-by <duration>` | `0` | Wait some more after alert stops firing before declaring it resolved (on Azure Prom this defaults to `5m`) |
| `--allow-high-cardinality` | — | Disable the per-chunk 1000-series safety abort |
| `--no-progress` | — | Suppress the progress meter (TTY-rewrite line on a terminal, dots otherwise) |
| `--no-cache` | — | Ignore existing cache (still writes new results) |
| `--cache-dir <path>` | `~/.cache/atest` | Cache directory |
| `-v, --verbose` | — | Per-chunk cache hit messages and per-firing detail |

### Example

```bash
export ATEST_GRAFANA_BEARER_TOKEN=<your-token>

./atest grafana \
  --grafana-url https://grafana.example.com \
  --datasource my-prometheus-uid \
  -q 'rate(http_errors_total[5m]) > 0.01' \
  --for 5m,10m,1h \
  --from 2025-05-01 --to 2025-05-08 \
  --incident-group-by cluster,namespace
```

### Output

```
type: grafana, source: https://grafana.example.com, datasource: my-prometheus-uid
starttime: 2025-05-01T00:00:00Z, endtime: 2025-05-08T00:00:00Z (duration: 168h)
preroll: 2h (querying from 2025-04-30T22:00:00Z)
step: 30s, eval-interval: 1m, chunk-size: 1h
expr: rate(http_errors_total[5m]) > 0.01

goal: 169 chunks
fetching: 169
query complete: 169 chunks (168 cached in 42ms, 1 fetched in 1.2s), 3 series returned, 3024 samples total, max chunk cardinality 3

analysis:
  for 5m: 47 firings, 38 grouped firings, 3 incidents
  for 10m: 12 firings, 10 grouped firings, 2 incidents
  for 1h: never
```

### Caching

Query results are cached to disk by `(grafana-url, datasource, expr, time-range, step)`. Re-running the same query is instant. Overlapping time ranges reuse cached chunks. Use `--no-cache` to force fresh queries (still writes to cache for future runs).

The PromQL `expr` is normalized through Prometheus's parser before hashing, so
whitespace and formatting differences hit the same cache entry. `-q 'm > 5'`
and `-q 'm' --gt 5` produce the same cache key (the comparator flag is emitted
inline; see `docs/design.md` for the precedence reasoning and the cases this
form rejects).

### Local evaluation (use sparingly)

A comparator inside the PromQL — `-q 'metric > 5'` — pushes the threshold to
Grafana, which returns only the series and samples where the condition holds.
This is the cheap, default path.

A comparator as a flag — `-q 'metric' --gt 5` — fetches the **raw** series and
applies the threshold locally. This is what makes threshold sweeps possible
without refetching, and is what enables multi-clause evaluation. But the raw
query returns every series the expression produces, not just those that cross
the threshold, so the cached payload can be **orders of magnitude larger**.

In one real example on a heavy alert over 24h:

|       | series | cache size |
|-------|--------|------------|
| Push-down (`-q 'expr > 0.5'`) | 3  | 34 KB |
| Local eval (`-q 'expr' --gt 0.5`) | 51 | 11 MB |

Rule of thumb: use push-down by default. Reach for the comparator flag only
when you specifically want to sweep thresholds, or to build a multi-clause
expression with `--and`/`--or`. For multi-clause, each clause is fetched once
in raw form and combined locally per timestamp — expect a few times the cost
of a single push-down query, not 100×.

### Timestamps

Supported formats: `2025-05-01T00:00:00Z` (RFC3339), `2025-05-01T00:00:00`, `2025-05-01`.

### Durations

Prometheus-style: `15s`, `5m`, `1h`, `1d`.
