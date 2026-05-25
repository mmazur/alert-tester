# Tests

This doc covers what's tested today, what *should* be tested with
synthetic data, and what would need a real Prometheus to verify.

## Today

`internal/eval/combine_test.go` covers the multi-clause combiner:

- `TestCombineAnd` — `A and B` on a shared label set keeps only the
  overlapping timestamps.
- `TestCombineOr` — `A or B` on two different label sets emits one
  series per side.
- `TestCombineAndDifferentLabelSets` — `and` on disjoint label sets
  yields empty (full-label-set matching, like PromQL with no
  `on()`/`ignoring()`).
- `TestCombinePrecedenceAndBindsTighter` — fold `(A and B) or C` vs
  `A and (B or C)` manually and confirm they differ; documents the
  precedence that `combineChain` relies on.

Run them: `go test ./...`. That's the entire automated suite right now.

Everything else has been verified by hand against the real Grafana
cache (see the "Tested in this session" notes in the most recent
handoff for the exact runs).

## What should be unit-tested with synthetic data

The eval package is pure functions over `[]model.Series` — easy to
fixture. Things worth covering:

### `eval.EvaluateFor` (for.go)

- `for: 0` fires on the first non-zero sample.
- `for: N` does NOT fire when the run is `< N` long (off-by-one at the
  boundary — pending exactly N seconds with sample interval matching
  should fire; one tick short should not).
- A NaN sample mid-run resets the run (matches Alertmanager: missing
  sample = resolved).
- A zero sample mid-run resets the run.
- Multiple firings on the same series: gap of one resolution tick
  produces two firings, not one merged firing.
- Subsample alignment: samples at irregular timestamps round to the
  eval-interval grid correctly.

### `eval.MergeFirings` (incident.go)

- `mergeGap = 0`: only truly overlapping firings merge.
- `mergeGap = 10m`: firings with a 5m gap merge into one with the
  later `LastFired` and the higher `MaxValue`.
- Three firings A-B-C where A→B is below gap and B→C is above gap
  produces two firings: merged(A,B) and C.
- `MaxValue` propagates correctly across merges.

### `eval.CorrelatedFirings` / `eval.GroupIncidents` (incident.go)

- Two series sharing `cluster=foo` produce one correlated firing and
  one incident, not two.
- Two series with different `cluster` values produce two incidents.
- `--incident-group-by` not set → `GroupIncidents` not called; caller
  reports raw firing count only.
- A correlation key that fires, resolves for hours, then fires again:
  still one incident (no incident-resolution logic — see design doc).

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
  should keep it (FirstFired ≥ from). One that starts 5m before and
  resolves before `--from`: should be dropped.
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