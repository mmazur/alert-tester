package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/prometheus/prometheus/promql/parser"

	"alert-tester/internal/cache"
	"alert-tester/internal/eval"
	"alert-tester/internal/grafana"
	"alert-tester/internal/model"
	"alert-tester/internal/query"
)

func main() {
	root := &cobra.Command{
		Use:           "atest",
		Short:         "alert-tester: check your alerts' performance before pushing them to prod",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newGrafanaCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type tokenKind int

const (
	tokQuery tokenKind = iota
	tokCmp
	tokAnd
	tokOr
)

type clauseToken struct {
	kind  tokenKind
	value string // for tokQuery: promql; for tokCmp: encoded as "<op>|<rhs>"
}

// orderedTokens captures --query in argv order.
type orderedTokens struct {
	tokens *[]clauseToken
	kind   tokenKind
}

func (o orderedTokens) String() string { return "" }
func (o orderedTokens) Type() string {
	switch o.kind {
	case tokQuery:
		return "promql"
	default:
		return ""
	}
}
func (o orderedTokens) Set(v string) error {
	*o.tokens = append(*o.tokens, clauseToken{kind: o.kind, value: v})
	return nil
}

// cmpTokens captures comparator flags (--gt/--lt/...) in argv order, with op encoded.
type cmpTokens struct {
	tokens *[]clauseToken
	op     string
}

func (c cmpTokens) String() string { return "" }
func (c cmpTokens) Type() string   { return "value[,value...]" }
func (c cmpTokens) Set(v string) error {
	*c.tokens = append(*c.tokens, clauseToken{kind: tokCmp, value: c.op + "|" + v})
	return nil
}

// flagJoin is a no-value flag that appends a join token.
type flagJoin struct {
	tokens *[]clauseToken
	kind   tokenKind
}

func (j flagJoin) String() string   { return "false" }
func (j flagJoin) Type() string     { return "" }
func (j flagJoin) IsBoolFlag() bool { return true }
func (j flagJoin) Set(v string) error {
	if v == "true" {
		*j.tokens = append(*j.tokens, clauseToken{kind: j.kind})
	}
	return nil
}

// cmpOps maps long flag name -> PromQL operator. Long-only because pflag would
// parse e.g. `-gt` as bundled short flags.
var cmpOps = []struct {
	flag, op string
}{
	{"eq", "=="},
	{"ne", "!="},
	{"lt", "<"},
	{"le", "<="},
	{"gt", ">"},
	{"ge", ">="},
}

type clause struct {
	query string
	op    string   // "" means "no threshold; non-empty result is truthy"
	rhs   []string // one or more RHS values; multiple = sweep
}

// join lives between clauses; clauses[i+1] is glued to clauses[i] by joins[i].
type clauseChain struct {
	clauses []clause
	joins   []tokenKind // each is tokAnd or tokOr
}

func parseClauses(tokens []clauseToken) (*clauseChain, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("--query is required")
	}
	if tokens[0].kind != tokQuery {
		return nil, fmt.Errorf("first clause flag must be --query")
	}
	ch := &clauseChain{}
	var cur *clause
	expectClause := true
	for _, t := range tokens {
		switch t.kind {
		case tokQuery:
			if !expectClause {
				return nil, fmt.Errorf("--query without preceding --and/--or")
			}
			ch.clauses = append(ch.clauses, clause{query: t.value})
			cur = &ch.clauses[len(ch.clauses)-1]
			expectClause = false
		case tokCmp:
			if cur == nil {
				return nil, fmt.Errorf("comparator flag before --query")
			}
			if cur.op != "" {
				return nil, fmt.Errorf("multiple comparator flags for same --query")
			}
			op, rhs, ok := strings.Cut(t.value, "|")
			if !ok {
				return nil, fmt.Errorf("internal: malformed cmp token")
			}
			var parts []string
			for _, p := range strings.Split(rhs, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				parts = append(parts, p)
			}
			if len(parts) == 0 {
				return nil, fmt.Errorf("comparator value is empty")
			}
			cur.op = op
			cur.rhs = parts
		case tokAnd, tokOr:
			if expectClause {
				return nil, fmt.Errorf("consecutive joins (--and/--or)")
			}
			ch.joins = append(ch.joins, t.kind)
			cur = nil
			expectClause = true
		}
	}
	if expectClause {
		return nil, fmt.Errorf("trailing --and/--or without --query")
	}
	return ch, nil
}

func newGrafanaCmd() *cobra.Command {
	f := &grafanaFlags{}
	cmd := &cobra.Command{
		Use:   "grafana",
		Short: "Test alerts against a Grafana datasource",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGrafana(f)
		},
	}
	cmd.Flags().StringVar(&f.grafanaURL, "grafana-url", "", "Grafana base URL (required)")
	cmd.Flags().StringVar(&f.datasource, "datasource", "", "Datasource UID (required)")
	cmd.Flags().StringVar(&f.bearerToken, "bearer-token", "", "Bearer token (or ATEST_GRAFANA_BEARER_TOKEN env var)")
	cmd.Flags().VarP(orderedTokens{tokens: &f.clauseTokens, kind: tokQuery}, "query", "q", "PromQL expression (repeatable; required at least once)")
	for _, c := range cmpOps {
		cmd.Flags().Var(cmpTokens{tokens: &f.clauseTokens, op: c.op}, c.flag, fmt.Sprintf("Threshold check %s RHS for preceding --query (comma-separated to sweep)", c.op))
	}
	cmd.Flags().VarP(flagJoin{tokens: &f.clauseTokens, kind: tokAnd}, "and", "a", "Join preceding clause with the next via AND")
	cmd.Flag("and").NoOptDefVal = "true"
	cmd.Flags().VarP(flagJoin{tokens: &f.clauseTokens, kind: tokOr}, "or", "o", "Join preceding clause with the next via OR")
	cmd.Flag("or").NoOptDefVal = "true"
	cmd.Flags().StringSliceVar(&f.forRaw, "for", nil, "for: durations, comma-separated (default 0m)")
	cmd.Flags().StringVar(&f.fromRaw, "from", "", "Start timestamp (required)")
	cmd.Flags().StringVar(&f.toRaw, "to", "", "End timestamp (required)")
	cmd.Flags().DurationVar(&f.chunkSize, "chunk-size", time.Hour, "Chunk size")
	cmd.Flags().DurationVar(&f.step, "step", 30*time.Second, "Query step")
	cmd.Flags().DurationVar(&f.evalInterval, "eval-interval", 60*time.Second, "Evaluation interval")
	cmd.Flags().BoolVar(&f.noCache, "no-cache", false, "Disable cache")
	cmd.Flags().StringVar(&f.cacheDir, "cache-dir", "", "Cache directory (default: ~/.cache/atest)")
	cmd.Flags().StringVar(&f.correlationID, "incident-group-by", "", "Comma- or slash-separated labels for incident grouping (e.g. cluster,namespace)")
	cmd.Flags().DurationVar(&f.delayResolutionBy, "delay-resolution-by", 0, "Wait some more after alert stops firing before declaring it resolved (on Azure Prom this defaults to 5m)")
	cmd.Flags().BoolVar(&f.allowHighCardinality, "allow-high-cardinality", false, "Disable the per-chunk 1000-series safety abort")
	cmd.Flags().BoolVar(&f.noProgress, "no-progress", false, "Suppress the progress meter (still printed if --verbose is set)")
	cmd.Flags().BoolVarP(&f.verbose, "verbose", "v", false, "Print per-chunk cache/fetch messages and per-firing timestamps")

	for _, name := range []string{"grafana-url", "datasource", "from", "to"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

type grafanaFlags struct {
	grafanaURL           string
	datasource           string
	bearerToken          string
	clauseTokens         []clauseToken
	forRaw               []string
	fromRaw              string
	toRaw                string
	chunkSize            time.Duration
	step                 time.Duration
	evalInterval         time.Duration
	noCache              bool
	cacheDir             string
	correlationID        string
	delayResolutionBy    time.Duration
	allowHighCardinality bool
	noProgress           bool
	verbose              bool
}

func runGrafana(f *grafanaFlags) error {
	chain, err := parseClauses(f.clauseTokens)
	if err != nil {
		return err
	}
	if len(chain.clauses) > 1 {
		for i, c := range chain.clauses {
			if len(c.rhs) > 1 {
				return fmt.Errorf("threshold sweeps are not supported with multi-clause chains (clause %d has %d values)", i+1, len(c.rhs))
			}
		}
	}

	forDurations, err := parseForDurations(f.forRaw)
	if err != nil {
		return err
	}
	from, err := parseTimestamp(f.fromRaw)
	if err != nil {
		return fmt.Errorf("invalid --from: %w", err)
	}
	to, err := parseTimestamp(f.toRaw)
	if err != nil {
		return fmt.Errorf("invalid --to: %w", err)
	}
	correlationLabels := parseCorrelationID(f.correlationID)

	token := f.bearerToken
	if token == "" {
		token = os.Getenv("ATEST_GRAFANA_BEARER_TOKEN")
	}

	client := grafana.NewClient(f.grafanaURL, token)

	cacheDir := f.cacheDir
	if cacheDir == "" {
		homeDir, _ := os.UserHomeDir()
		cacheDir = filepath.Join(homeDir, ".cache", "atest")
	}

	c := cache.New(cacheDir, !f.noCache)

	eng := &query.Engine{
		Client:               client,
		Cache:                c,
		ChunkSize:            f.chunkSize,
		Verbose:              f.verbose,
		AllowHighCardinality: f.allowHighCardinality,
		Output:               os.Stdout,
	}
	if !f.noProgress {
		eng.Progress = newProgressPrinter(os.Stderr, f.verbose)
	}

	maxFor := time.Duration(0)
	for _, d := range forDurations {
		if d > maxFor {
			maxFor = d
		}
	}
	prerollBase := maxFor
	if f.delayResolutionBy > prerollBase {
		prerollBase = f.delayResolutionBy
	}
	prerollBase += 10 * time.Minute
	preroll := alignUpToChunk(prerollBase, f.chunkSize)
	queryFrom := from.Add(-preroll)

	sort.Slice(forDurations, func(i, j int) bool {
		return forDurations[i] < forDurations[j]
	})

	report, err := buildGrafanaReport(f, eng, chain, queryFrom, from, to, queryFrom, preroll, correlationLabels, forDurations)
	if err != nil {
		return err
	}
	renderGrafanaReport(os.Stdout, report, f.verbose)

	return nil
}

type evalRun struct {
	displayExpr string
	fetches     []reportFetch
	extraLines  []string // e.g. "combined: ..." for multi-clause
	series      []model.Series
}

// buildEvalRuns fetches each clause, applies its local predicate if any, and
// combines the per-clause filtered series via --and/--or with PromQL
// precedence (and binds tighter than or). For a single clause, this is just
// the underlying buildRuns loop; for multi-clause, it produces one evalRun
// per combined output.
func buildEvalRuns(eng *query.Engine, datasource string, chain *clauseChain, queryFrom, to time.Time, step time.Duration) ([]evalRun, error) {
	if len(chain.clauses) == 1 {
		runs, err := buildRuns(chain.clauses[0])
		if err != nil {
			return nil, err
		}
		out := make([]evalRun, 0, len(runs))
		for _, r := range runs {
			series, info, err := fetchAndFilter(eng, datasource, r, queryFrom, to, step)
			if err != nil {
				return nil, err
			}
			out = append(out, evalRun{displayExpr: r.displayExpr, fetches: []reportFetch{info}, series: series})
		}
		return out, nil
	}

	clauseSeries := make([][]model.Series, len(chain.clauses))
	displayParts := make([]string, len(chain.clauses))
	var allFetches []reportFetch
	for i, cl := range chain.clauses {
		runs, err := buildRuns(cl)
		if err != nil {
			return nil, fmt.Errorf("clause %d: %w", i+1, err)
		}
		// sweeps already rejected upstream; should always be exactly one
		r := runs[0]
		series, info, err := fetchAndFilter(eng, datasource, r, queryFrom, to, step)
		if err != nil {
			return nil, err
		}
		allFetches = append(allFetches, info)
		clauseSeries[i] = eval.NormalizeForCombine(series)
		displayParts[i] = r.displayExpr
	}

	combined := combineChain(clauseSeries, chain.joins)
	return []evalRun{{
		displayExpr: renderChain(displayParts, chain.joins),
		fetches:     allFetches,
		extraLines:  []string{fmt.Sprintf("combined: %d series after %s", len(combined), joinSummary(chain.joins))},
		series:      combined,
	}}, nil
}

func fetchAndFilter(eng *query.Engine, datasource string, r run, queryFrom, to time.Time, step time.Duration) ([]model.Series, reportFetch, error) {
	result, stats, err := eng.Query(datasource, r.fetchExpr, queryFrom, to, step)
	if err != nil {
		return nil, reportFetch{}, err
	}
	series := result.Series
	samplesPass := stats.SampleCount
	if r.predicate != nil {
		series = applyPredicate(series, r.predicate)
		samplesPass = 0
		for _, s := range series {
			samplesPass += len(s.Samples)
		}
	}
	return series, makeReportFetch(r.fetchExpr, stats, r.thresholdLabel, r.predicate != nil, samplesPass), nil
}

// newProgressPrinter returns an Engine.Progress callback. In all modes it
// prints a "goal: N chunks" line once when fetching starts. In verbose mode
// it does nothing further (verbose already prints per-chunk lines). Otherwise
// it updates a "fetching: K" line in place on a TTY (\r-rewrite) or prints
// one dot per chunk on a non-TTY.
func newProgressPrinter(w *os.File, verbose bool) func(done, total int) {
	isTTY := false
	if fi, err := w.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		isTTY = true
	}
	var out io.Writer = w
	return func(done, total int) {
		if done == 0 {
			fmt.Fprintf(out, "goal: %d chunks\n", total)
			if verbose {
				return
			}
			if isTTY {
				fmt.Fprint(out, "fetching: 0")
			} else {
				fmt.Fprint(out, "fetching: ")
			}
			return
		}
		if verbose {
			return
		}
		if isTTY {
			fmt.Fprintf(out, "\rfetching: %d", done)
		} else {
			fmt.Fprint(out, ".")
		}
		if done == total {
			fmt.Fprintln(out)
		}
	}
}

// tighter than `or`. We do this in two passes: first collapse each
// maximal run of `and`-joined clauses left-to-right, then `or` the
// resulting groups together.
func combineChain(parts [][]model.Series, joins []tokenKind) []model.Series {
	if len(parts) == 0 {
		return nil
	}
	if len(joins) != len(parts)-1 {
		return parts[0]
	}
	groups := [][]model.Series{parts[0]}
	for i, j := range joins {
		if j == tokAnd {
			last := len(groups) - 1
			groups[last] = eval.Combine(groups[last], parts[i+1], eval.OpAnd)
		} else {
			groups = append(groups, parts[i+1])
		}
	}
	out := groups[0]
	for i := 1; i < len(groups); i++ {
		out = eval.Combine(out, groups[i], eval.OpOr)
	}
	return out
}

func renderChain(displays []string, joins []tokenKind) string {
	var b strings.Builder
	for i, d := range displays {
		if i > 0 {
			switch joins[i-1] {
			case tokAnd:
				b.WriteString(" AND ")
			case tokOr:
				b.WriteString(" OR ")
			}
		}
		b.WriteByte('(')
		b.WriteString(d)
		b.WriteByte(')')
	}
	return b.String()
}

func joinSummary(joins []tokenKind) string {
	ands, ors := 0, 0
	for _, j := range joins {
		switch j {
		case tokAnd:
			ands++
		case tokOr:
			ors++
		}
	}
	return fmt.Sprintf("%d AND / %d OR joins", ands, ors)
}

type run struct {
	fetchExpr      string
	displayExpr    string
	thresholdLabel string
	predicate      func(float64) bool
}

func buildRuns(cl clause) ([]run, error) {
	if cl.op == "" {
		return []run{{fetchExpr: cl.query, displayExpr: cl.query}}, nil
	}
	if err := validateQueryForComparator(cl.query); err != nil {
		return nil, err
	}
	out := make([]run, len(cl.rhs))
	for i, v := range cl.rhs {
		rhs, err := parseFloat(v)
		if err != nil {
			return nil, fmt.Errorf("invalid threshold %q: %w", v, err)
		}
		pred, err := predicateFor(cl.op, rhs)
		if err != nil {
			return nil, err
		}
		out[i] = run{
			fetchExpr:      cl.query,
			displayExpr:    fmt.Sprintf("%s %s %s (local)", cl.query, cl.op, v),
			thresholdLabel: fmt.Sprintf("%s %s", cl.op, v),
			predicate:      pred,
		}
	}
	return out, nil
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	if err != nil {
		return 0, err
	}
	return f, nil
}

func predicateFor(op string, rhs float64) (func(float64) bool, error) {
	switch op {
	case ">":
		return func(v float64) bool { return v > rhs }, nil
	case ">=":
		return func(v float64) bool { return v >= rhs }, nil
	case "<":
		return func(v float64) bool { return v < rhs }, nil
	case "<=":
		return func(v float64) bool { return v <= rhs }, nil
	case "==":
		return func(v float64) bool { return v == rhs }, nil
	case "!=":
		return func(v float64) bool { return v != rhs }, nil
	}
	return nil, fmt.Errorf("unknown comparator %q", op)
}

// applyPredicate drops samples where the predicate is false (or value is NaN).
// Matches Alertmanager semantics: a comparison filter `x > T` produces no
// sample where the condition is false, and absence resolves the alert.
func applyPredicate(series []model.Series, pred func(float64) bool) []model.Series {
	out := make([]model.Series, 0, len(series))
	for _, s := range series {
		kept := make([]model.Sample, 0, len(s.Samples))
		for _, sm := range s.Samples {
			if math.IsNaN(sm.Value) {
				continue
			}
			if pred(sm.Value) {
				kept = append(kept, sm)
			}
		}
		if len(kept) == 0 {
			continue
		}
		out = append(out, model.Series{Labels: s.Labels, Samples: kept})
	}
	return out
}

// Reject queries whose top-level op has precedence <= comparison. Concatenating
// "query op rhs" would re-associate wrong (see docs/design.md).
func validateQueryForComparator(q string) error {
	p := parser.NewParser(parser.Options{
		EnableExperimentalFunctions:  true,
		ExperimentalDurationExpr:     true,
		EnableExtendedRangeSelectors: true,
		EnableBinopFillModifiers:     true,
	})
	expr, err := p.ParseExpr(q)
	if err != nil {
		return fmt.Errorf("--query is not valid PromQL: %w", err)
	}
	be, ok := expr.(*parser.BinaryExpr)
	if !ok {
		return nil
	}
	switch be.Op {
	case parser.EQLC, parser.NEQ, parser.LSS, parser.LTE, parser.GTR, parser.GTE,
		parser.LAND, parser.LOR, parser.LUNLESS:
		return fmt.Errorf("--query top-level operator %q has precedence <= comparison; wrap in parens: -q '(<query>)'", be.Op)
	}
	return nil
}

func parseForDurations(raw []string) ([]time.Duration, error) {
	var out []time.Duration
	for _, item := range raw {
		for _, part := range strings.Split(item, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			d, err := parseDuration(part)
			if err != nil {
				return nil, fmt.Errorf("invalid --for value %q: %w", part, err)
			}
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return []time.Duration{0}, nil
	}
	return out, nil
}

func parseCorrelationID(s string) []string {
	if s == "" {
		return nil
	}
	sep := "/"
	if strings.Contains(s, ",") {
		sep = ","
	}
	var out []string
	for _, part := range strings.Split(s, sep) {
		if l := strings.TrimSpace(part); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func printFirings(w io.Writer, results []model.AlertResult, evalInterval time.Duration, verbose bool) {
	type entry struct {
		labels  map[string]string
		firings []model.FiringRange
	}
	var entries []entry
	for _, r := range results {
		if len(r.Firings) == 0 {
			continue
		}
		entries = append(entries, entry{labels: r.LabelSet, firings: r.Firings})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].firings[0].FirstPending.Before(entries[j].firings[0].FirstPending)
	})
	for _, e := range entries {
		if !verbose {
			continue
		}
		fmt.Fprintf(w, "    %s: %d firings\n", formatLabels(e.labels), len(e.firings))
		for _, f := range e.firings {
			end := f.LastFired.Add(evalInterval)
			fmt.Fprintf(w, "      %s -> %s (%s, max=%.2f)\n",
				f.FirstFired.Format(time.RFC3339),
				end.Format(time.RFC3339),
				formatDuration(end.Sub(f.FirstFired)),
				f.MaxValue)
		}
	}
}

func printIncidents(w io.Writer, incidents []model.Incident, evalInterval time.Duration, verbose bool) {
	for _, inc := range incidents {
		if !verbose {
			continue
		}
		fmt.Fprintf(w, "    %s: %d firings\n", formatLabels(inc.Labels), len(inc.Firings))
		for _, f := range inc.Firings {
			end := f.LastFired.Add(evalInterval)
			fmt.Fprintf(w, "      %s -> %s (%s, max=%.2f)\n",
				f.FirstFired.Format(time.RFC3339),
				end.Format(time.RFC3339),
				formatDuration(end.Sub(f.FirstFired)),
				f.MaxValue)
		}
	}
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func alignUpToChunk(d, chunk time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if chunk <= 0 {
		return d
	}
	n := (d + chunk - 1) / chunk
	return n * chunk
}

func mergePerSeries(results []model.AlertResult, gap time.Duration) []model.AlertResult {
	out := make([]model.AlertResult, len(results))
	for i, r := range results {
		r.Firings = eval.MergeFirings(r.Firings, gap)
		r.Fired = len(r.Firings) > 0
		out[i] = r
	}
	return out
}

func filterFiringsToWindow(results []model.AlertResult, from, to time.Time) []model.AlertResult {
	out := make([]model.AlertResult, len(results))
	for i, r := range results {
		var kept []model.FiringRange
		for _, f := range r.Firings {
			if !f.FirstFired.Before(from) && f.FirstFired.Before(to) {
				kept = append(kept, f)
			}
		}
		r.Firings = kept
		r.Fired = len(kept) > 0
		out[i] = r
	}
	return out
}

func formatDuration(d time.Duration) string {
	if d >= time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	if d >= time.Minute || d == 0 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// trimDur formats a Duration via String() but strips trailing 0m0s / 0s
// so "1h0m0s" → "1h", "5m0s" → "5m", "1h30m0s" → "1h30m". Sub-second
// units (ms, µs, ns) are left intact.
func trimDur(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func parseDuration(s string) (time.Duration, error) {
	if len(s) == 0 {
		return 0, fmt.Errorf("empty duration")
	}

	multiplier := time.Second
	numStr := s

	switch s[len(s)-1] {
	case 's':
		numStr = s[:len(s)-1]
		multiplier = time.Second
	case 'm':
		numStr = s[:len(s)-1]
		multiplier = time.Minute
	case 'h':
		numStr = s[:len(s)-1]
		multiplier = time.Hour
	case 'd':
		numStr = s[:len(s)-1]
		multiplier = 24 * time.Hour
	default:
		return time.ParseDuration(s)
	}

	var n int
	_, err := fmt.Sscanf(numStr, "%d", &n)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}

	return time.Duration(n) * multiplier, nil
}

func parseTimestamp(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q (try RFC3339 format)", s)
}
