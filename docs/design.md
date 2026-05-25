# Design notes

## Comparator flags and PromQL precedence

`atest grafana` accepts a threshold via either:

  -q 'metric > 5'         # form A: comparator inside the query
  -q 'metric' --gt 5      # form B: comparator as a flag

Internally form B is emitted as plain string concatenation: `<query> <op> <rhs>`
(no surrounding parens on the query). This means form B and form A produce
identical cache keys, which is the point — we want the same Grafana data to be
served from cache regardless of which form the user typed.

### Why no parens?

Wrapping the user's query in parens — `(<query>) <op> <rhs>` — would change the
cache key. Prometheus's promql parser preserves explicit parens in its
`.String()` output, so `(x) < 5` and `x < 5` normalize to different strings,
which means different cache files. We rely on cache hits across form A/B for
queries that already exist in the cache (today's cache was populated entirely
via form A, before `--gt`/`--lt`/etc existed).

### Why this is safe most of the time

PromQL operator precedence (highest → lowest):

  1. `^`
  2. `*`, `/`, `%`, `atan2`
  3. `+`, `-`
  4. `==`, `!=`, `<=`, `<`, `>=`, `>`         ← comparison
  5. `and`, `unless`
  6. `or`

Comparison is below arithmetic, so for queries built from identifiers,
functions, aggregations, or arithmetic, `query op rhs` parses with the user's
query as the LHS of the comparison — which is what they want.

Examples that work correctly:
- `-q 'sum(rate(x[5m]))' --gt 0.05`   → `sum(rate(x[5m])) > 0.05`
- `-q 'a/b' --gt 0.5`                 → `a/b > 0.5`
- `-q 'x' --lt 5`                     → `x < 5`

### Why we reject some queries

Two query shapes would silently re-associate when concatenated:

- **Top-level `and`/`or`/`unless`** — these have precedence *lower* than
  comparison, so `a and b < 5` parses as `a and (b < 5)`, not as
  `(a and b) < 5`.
- **Top-level comparison** — passing a query that already contains a
  comparator and then adding another (e.g. `-q 'x < 5' --gt 7`) produces
  `x < 5 > 7`, which parses left-to-right as `(x < 5) > 7`. Almost certainly
  a user mistake (they forgot to remove the inline comparator).

`validateQueryForComparator` parses the query and rejects both shapes when a
comparator flag is present. The user can fix it by wrapping the query
themselves: `-q '(a and b)' --lt 5` → `(a and b) < 5`, which parses correctly.

### Things we considered and rejected

1. **Build the AST directly and call `.String()` on it.** Prometheus's
   `BinaryExpr.String()` is *not* precedence-aware — given an AST for
   `(a and b) < 5`, it emits `a and b < 5` (which then re-parses as the
   wrong thing). So building the AST gains nothing.

2. **Always wrap in parens.** Solves the precedence issue but breaks cache
   compatibility with the form-A-populated cache, as described above.

3. **Auto-detect and wrap only when needed.** Same as (2) for any query that
   would actually need it — the cache key would still diverge. Wrapping only
   for `and`/`or`/`unless` would help, but those queries are rare in our
   threshold-style alerts, and rejecting is clearer than silently choosing
   between two different cache keys.

### What could go wrong

- A user passes a query that is "form-A-shaped but with surrounding noise"
  (e.g. `-q '  metric > 5  ' --gt 7`). The validator parses successfully (top
  level is `BinaryExpr` with comparison op) and rejects with the parens hint —
  which is unhelpful, since the right fix is "drop one of the comparators".
  We could improve the error message to detect this case.

- If we later introduce alternative cache backends that *do* normalize away
  redundant parens, the no-parens decision becomes unnecessary and the
  validator could be relaxed.

## Firings, correlation, and incidents

Three distinct counts come out of an evaluation, and they answer different
questions. The pipeline runs on whatever `[]model.Series` `EvaluateFor`
receives — for a single clause that's the fetched+filtered series; for a
multi-clause chain it's the combined output of `combineChain` (see
"Evaluation pipeline" below). From this stage onward there's no notion of
which clause contributed which sample; "series" just means "one unique
label set in the input to `EvaluateFor`".

1. **Per-series firings.** `eval.EvaluateFor` walks each unique label set
   (one `model.Series`) independently and emits `FiringRange`s based on the
   `for:` duration. By default, "a firing" is scoped to one label set.

2. **Per-series resolution-delay merge.** If `--delay-resolution-by` is non-zero
   (default 0), adjacent firings on the *same series* with a gap below
   that threshold collapse into one. Happens in `mergePerSeries` before
   any correlation.

3. **Grouped firings (only with `--incident-group-by`).** `CorrelatedFirings`
   re-buckets all firings by the correlation key (subset of labels, e.g.
   `cluster/namespace`) and runs `MergeFirings` again across series sharing
   that key, using the same `--delay-resolution-by` gap. The returned count
   is what `atest` reports as "grouped firings" — so `--incident-group-by`
   affects firing counts, not just incident grouping.

4. **Incidents (only with `--incident-group-by`).** `GroupIncidents` is a pure
   bucketer: one incident per unique correlation key that fired anywhere in
   the window. **No resolution logic.** A key that fires, resolves for
   hours, then fires again is still one incident.

### Resolution delay does not affect incident count

`--delay-resolution-by` changes firing and grouped-firing counts (it
controls the gap-merge threshold in `MergeFirings`). It does **not** change
incident count: `GroupIncidents` ignores firing structure and only asks
"did this correlation key fire at all?".

If we wanted to emulate incident resolution (N minutes of no firing closes
the incident; the next firing opens a new one), we'd run `MergeFirings`
per correlation key and emit one incident per surviving range, rather
than one per key.

## Evaluation pipeline (with multi-clause)

A run goes through the following stages, in order. Stages 1–3 happen
once per clause; stages 4+ run on the combined result.

1. **Fetch.** For each clause, the engine pulls its raw PromQL
   (`clause.query`, with no comparator appended) from Grafana, chunked
   and cached. Cache key is the raw query — so form A (`expr > N`) and
   form B (`expr` + `--gt N`) share a cache file as long as the user
   types the same raw expression.

2. **Local filter (form B only).** If the clause used a comparator
   flag, `applyPredicate` drops samples where the predicate is false or
   NaN. Form A skips this — Grafana already filtered server-side.

3. **Normalize for combine (multi-clause only).** `NormalizeForCombine`
   drops zero and NaN samples so that "sample exists at T" uniformly
   means "this clause is firing at T". Skipped for single-clause runs
   because `findFirings` already treats 0/NaN as "not firing".

4. **Combine (multi-clause only).** `combineChain` walks the join chain
   with PromQL precedence — `and` binds tighter than `or`. First pass
   collapses each maximal run of `and`-joined clauses left-to-right via
   `Combine(_, _, OpAnd)`. Second pass `or`s the resulting groups
   together via `Combine(_, _, OpOr)`. Matching is on the **full label
   set** of each series (no `on()`/`ignoring()` yet) — matches upstream
   Prometheus and Azure Managed Prometheus semantics. `and` keeps
   timestamps present in both sides; `or` unions, left value wins on
   collision.

5. **`for:` evaluation.** `EvaluateFor` runs on the combined series,
   exactly the same code path as a single-clause run. The combined
   series carries no notion of which clause contributed which sample —
   from here on it's just a set of label sets with sample timestamps.

6. **Auto-resolve merge** (`mergePerSeries`), **preroll trim**
   (`filterFiringsToWindow`), **correlation** (`CorrelatedFirings` /
   `GroupIncidents`) — all unchanged.

### Why filtering happens before combine, not after

Combine needs each clause's samples to mean "firing now" so that
intersection/union semantics line up with what an alert author would
get from writing the same expression as a single PromQL string. A
clause whose comparator hasn't been applied yet would still carry
"below-threshold" samples, and `and` would over-match. So `applyPredicate`
runs per-clause, before normalize+combine.

### Sweep limitation

`--gt 0.3,0.5,1.0` produces multiple runs of a single clause. Mixing
sweeps with `--and`/`--or` is rejected up front: the output shape
("N runs, each with its own combined series") isn't a natural fit for
the per-run printing the rest of the code does, and we'd rather force
the user to script multiple invocations than guess at a Cartesian
product. If sweep+multi becomes a real need, the natural place to add
it is right before `buildEvalRuns`: expand the sweep clause into N
parallel chains, run each through the multi-clause path, and label the
output by threshold.

## Per-chunk safety aborts

Two checks run on every freshly fetched chunk, before it's written to
cache. Both abort the run on failure rather than warn — bad data that
lands in the cache silently poisons every future run against the same
expr+range.

1. **Step mismatch.** Grafana's Prometheus datasource silently widens
   the query step when `maxDataPoints` is too low for the range. The
   server reports the step it actually used in
   `frame.schema.meta.executedQueryString` (`"Expr: ...\nStep: <d>"`).
   If it differs from the requested step, abort: the samples are
   coarser than the eval logic expects and would silently under-fire.

2. **Series cap.** If a chunk returns more than 1000 series, abort with
   a message pointing at `--allow-high-cardinality` as the override.
   The intent is to catch accidentally under-aggregated queries (e.g.
   pulling raw per-pod samples when only namespace-level firings are
   wanted) before they fill the cache and dominate eval time. The
   override exists for the cases where the high cardinality is real.
