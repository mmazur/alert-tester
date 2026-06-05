# Tests

This doc covers what's tested today, what *should* be tested with
synthetic data, and what would need a real Prometheus to verify.

## Today

Run the suite with:

```bash
go test ./...
```

Current automated coverage includes:

- `internal/eval/combine_test.go` for the multi-clause combiner.
- `internal/eval/for_test.go` for `for:` timing, NaN resets, and
  multiple firings on one series.
- `internal/eval/incident_test.go` for firing merges, grouped firings,
  and incidents.
- `internal/query/engine_test.go` for chunk splitting and per-series
  merging.
- `cmd/atest/main_test.go` for clause parsing, comparator validation,
  predicate boundaries, overlap-aware window trimming, render output,
  and fake-data grafana pipeline tests.

The fake-data pipeline tests cover:

- local threshold evaluation
- multiple `for:` durations with exact firing ranges
- multi-clause precedence
- resolution-delay merging
- grouped firings and incidents
- multi-clause sweep rejection

## What should be unit-tested with synthetic data

The eval package is pure functions over `[]model.Series` — easy to
fixture. Things worth covering:

### `eval.EvaluateFor` (for.go)

Covered today:

- `for: N` boundary behavior
- A NaN sample mid-run resets the run
- Multiple firings on the same series stay separate without merge logic

- `for: 0` fires on the first non-zero sample.
- A zero sample mid-run resets the run.
- A series whose samples are all `0` (e.g. `up == 0` when the target is
  down) still fires — Prometheus treats series presence, not value, as
  the fire signal. See issue #1.
- Subsample alignment: samples at irregular timestamps round to the
  eval-interval grid correctly.

### `filterFiringsToWindow` (cmd/atest/main.go)

Covered today:

- inside-window firing kept
- before-window firing dropped
- after-window firing dropped
- overlap from preroll kept
- firing that starts in-window and continues past `to` kept

- **A firing that began in the preroll and is still active at `from` is
  kept** — its `[FirstFired, LastFired]` overlaps the window even
  though `FirstFired < from`. Regression guard for issue #2: a
  sticky-from-the-start expression (e.g. `absent(missing_metric)`) was
  silently reported as "never firing" because the single continuous
  firing's `FirstFired` landed in the preroll and the old start-in-window
  test dropped it.

### `eval.MergeFirings` (incident.go)

Covered today:

- merge across a positive gap threshold
- `MaxValue` propagation across a merge

- `mergeGap = 0`: only truly overlapping firings merge.
- Three firings A-B-C where A→B is below gap and B→C is above gap
  produces two firings: merged(A,B) and C.

### `eval.CorrelatedFirings` / `eval.GroupIncidents` (incident.go)

Covered today:

- two series sharing one grouping key collapse to one correlated firing
- different grouping keys produce separate incidents
- one key can accumulate multiple firings while still being one incident

- `--incident-group-by` not set → `GroupIncidents` not called; caller
  reports raw firing count only.

### `eval.Combine` (combine.go) — already partially covered

Add:
- `and` where left has 3 timestamps and right has the middle one only:
  result has the middle one.
- `or` where both sides have the same timestamp on the same label set:
  one sample emitted, left value wins.
- Both sides empty → empty output, no panic.
- One side empty: `and` → empty; `or` → other side verbatim.
- Series with empty label sets (`{}`) match each other.
- `and` where left has a sample at T and right has NaN at T on the
  same label set: T is not in the result (Alertmanager parity — a
  missing sample on one side of `and` resolves). Implied by
  `NormalizeForCombine` dropping NaN before `Combine` runs, but worth
  one direct assertion so a future refactor doesn't silently change
  it.

### `eval.NormalizeForCombine`

- Zero samples dropped.
- NaN samples dropped.
- A series that becomes empty after normalization is dropped entirely.

### `parseClauses` (cmd/atest/main.go)

- Bare `-q expr`: one clause, no joins.
- `-q a --gt 5 --and -q b --lt 3`: two clauses, one `and` join, each
  with its predicate.
- Comparator before any `-q` → error.
- Two comparators on one `-q` → error.
- Trailing `--and`/`--or` → error.
- Consecutive `--and --or` → error.
- Sweep on multiple clauses → error (validated post-parse in
  `runGrafana`).

### `validateQueryForComparator`

- Top-level `and`/`or`/`unless` rejected.
- Top-level comparison (`x > 5` plus `--gt 7`) rejected.
- Arithmetic (`a/b`, `sum(rate(x[5m]))`) accepted.
- Parenthesized version of a rejected query accepted (`(a and b)`).

### Predicate operators (applyPredicate)

One test per operator (`--gt/--lt/--ge/--le/--eq/--ne`) on a series
with samples straddling the threshold, pinning the boundary:
`--gt 5` drops a sample equal to 5, `--ge 5` keeps it, etc. Cheap
insurance against an off-by-one flip on any one operator.

### Flag defaults

- `--delay-resolution-by` default is 0. One test on the flag default
  value, plus one pipeline scenario where two firings 5m apart stay
  separate under the default but merge under `10m`.

### `renderChain` display output

- `(A)`, `(A) AND (B)`, `(A) AND (B) OR (C)` render exactly as shown.
  The string ends up in user-facing logs; a silent format change
  would break grep-based workflows.

## Whole-pipeline tests with a fake fetcher

`query.Engine` calls a `grafana.Client` interface to pull chunks. A
test-only fake client that returns pre-canned `model.QueryResult`s
would let us cover the full `runGrafana` pipeline (fetch → filter →
combine → eval → merge → correlate) without a real backend. Worth
doing once we have a stable handful of "this synthetic input should
produce exactly these firings/correlated/incidents counts" fixtures.

Good scenarios for those:

1. **Single series, single firing.** Baseline.
2. **Single series, gap merging.** Two firings 5m apart with
   `--delay-resolution-by 10m` → one merged firing with later
   `LastFired`.
3. **Two series, one correlation key.** Both `cluster=A`; firings on
   each → 2 firings, 1 correlated, 1 incident.
4. **Form A vs form B equivalence.** Same input data, threshold
   applied server-side vs locally → identical firing counts. (The
   eval-time view; cache key equivalence is a separate concern.)
5. **Multi-clause AND on different metrics.** Two clauses sharing
   `cluster` label, overlapping but not identical timestamps → only
   overlap fires.
6. **Multi-clause OR.** Two clauses sharing `cluster` label,
   disjoint timestamps → union fires.
7. **Multi-clause precedence.** Three clauses `A and B or C` where
   A∧B is empty but C is not → fires from C only.
8. **Sweep produces N independent runs** with monotonically
   decreasing firing counts as the threshold rises.

## Integration tests against a real Prometheus ("AI tests")

There's a class of behavior we can only verify end-to-end: that the
HTTP chunking, cache layout, time-range splitting, and PromQL we
build are actually consistent with what a real Prometheus does. Mocks
can lie; the real server is the oracle.

If we had a local Prometheus-compatible instance loaded with synthetic
data, the things worth testing:

- **Form A vs form B identical results.** Push synthetic data,
  evaluate `metric > 0.5` and `metric` + `--gt 0.5` over the same
  window, assert identical firing counts and timestamps. Today this
  is hand-verified against the production cache; with synthetic data
  we'd own the inputs.
- **Multi-clause matches a single equivalent expression.**
  `-q 'a > 5' --and -q 'b > 3'` should produce the same firings as
  `-q '(a > 5) and (b > 3)'` for the same data. This is the load-bearing
  claim — if it doesn't hold, our combine semantics are wrong.
- **`on()` / `ignoring()` parity (future).** If we add label-matching
  modifiers to the flag chain, they should match what Prometheus does
  when the same modifiers are in the PromQL string.
- **Chunking is transparent.** A query run with `--chunk-size 1h`
  should produce identical results to the same query with
  `--chunk-size 24h` (one big chunk). Catches off-by-one at chunk
  boundaries and cache-key collisions.
- **Cache invalidation correctness.** Re-running with the same
  arguments after touching nothing should hit cache for every chunk.
  Re-running with a tweaked threshold (form B) should still hit cache
  for the raw fetch.
- **Preroll arithmetic.** `for: 30m` + `--delay-resolution-by 15m` over
  a window starting at T should pull data starting at T - 30m (the
  max of the two, aligned up to chunk size). Verify the actual
  Grafana request range matches.
- **Edge of window behavior.** A firing that starts 5m before the
  `--from` cutoff and continues 5m past it: `filterFiringsToWindow`
  should keep it (range overlaps the window). One that starts 5m
  before and resolves before `--from`: should be dropped.
- **Auto-resolve at the boundary.** Two firings, one ending at
  `--from - 1m` and the next starting at `--from + 4m`, with
  `--delay-resolution-by 10m`: the merge happens before the window
  filter, so the merged firing's `FirstFired` may be pre-window —
  document the chosen behavior, then test it.

The synthetic-data Prometheus doesn't need to be Prometheus
specifically; anything that speaks the Prometheus HTTP API and lets
us push known series will do. Mimir, Cortex, VictoriaMetrics, or a
local Prom with `remote_write` from a fixture script are all
candidates.

## Cache-cost measurements (not a unit test)

Local evaluation trades cache size for flexibility: a single push-down
clause currently measures ~17× fewer series and ~330× smaller on-disk
footprint than the equivalent local-eval clause (README has the
numbers). Multi-clause local eval has not been measured the same way.

Pick 2–3 real multi-clause-shaped alerts (ideally including one with
no aggregation, where raw-query cardinality is high) and record:

- Series count and on-disk cache size per clause, and combined.
- Ratio vs. a hypothetical single push-down equivalent.

The working hypothesis is "a few × a single push-down, not orders of
magnitude." If that breaks down on high-cardinality alerts, the
README's "use sparingly" guidance needs to get sharper, and we may
want a cardinality guard before fetching.