package main

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"alert-tester/internal/model"
	"alert-tester/internal/query"
)

type fakeQueryResponse struct {
	series []model.Series
	stats  query.Stats
	err    error
}

type minutePoint struct {
	offset int
	value  float64
}

func fakeResponse(series ...model.Series) fakeQueryResponse {
	totalSamples := 0
	for _, s := range series {
		totalSamples += len(s.Samples)
	}
	return fakeQueryResponse{
		series: series,
		stats: query.Stats{
			Chunks:         1,
			CacheMisses:    1,
			FetchTime:      time.Second,
			MaxCardinality: len(series),
			SeriesReturned: len(series),
			SampleCount:    totalSamples,
		},
	}
}

func seriesAtMinutes(base time.Time, labels map[string]string, points ...minutePoint) model.Series {
	samples := make([]model.Sample, 0, len(points))
	for _, p := range points {
		samples = append(samples, model.Sample{
			Timestamp: base.Add(time.Duration(p.offset) * time.Minute),
			Value:     p.value,
		})
	}
	return model.Series{Labels: labels, Samples: samples}
}

func renderGrafanaFake(t *testing.T, f *grafanaFlags, responses map[string]fakeQueryResponse) (string, time.Time) {
	t.Helper()

	chain, err := parseClauses(f.clauseTokens)
	if err != nil {
		t.Fatalf("parseClauses returned error: %v", err)
	}
	if len(chain.clauses) > 1 {
		for i, c := range chain.clauses {
			if len(c.rhs) > 1 {
				t.Fatalf("threshold sweeps are not supported with multi-clause chains (clause %d has %d values)", i+1, len(c.rhs))
			}
		}
	}

	forDurations, err := parseForDurations(f.forRaw)
	if err != nil {
		t.Fatalf("parseForDurations returned error: %v", err)
	}
	from, err := parseTimestamp(f.fromRaw)
	if err != nil {
		t.Fatalf("parseTimestamp(from) returned error: %v", err)
	}
	to, err := parseTimestamp(f.toRaw)
	if err != nil {
		t.Fatalf("parseTimestamp(to) returned error: %v", err)
	}
	correlationLabels := parseCorrelationID(f.correlationID)
	preroll := computePreroll(forDurations, f.delayResolutionBy, f.chunkSize)
	queryFrom := deriveQueryFrom(from, preroll)

	sortForDurations(forDurations)

	runs, err := buildEvalRunsWithFetcher(chain, func(r run) ([]model.Series, reportFetch, error) {
		resp, ok := responses[r.fetchExpr]
		if !ok {
			t.Fatalf("unexpected query for %q", r.fetchExpr)
		}
		if resp.err != nil {
			return nil, reportFetch{}, resp.err
		}
		series := resp.series
		samplesPass := resp.stats.SampleCount
		if r.predicate != nil {
			series = applyPredicate(series, r.predicate)
			samplesPass = 0
			for _, s := range series {
				samplesPass += len(s.Samples)
			}
		}
		return series, makeReportFetch(r.fetchExpr, resp.stats, r.thresholdLabel, r.predicate != nil, samplesPass), nil
	})
	if err != nil {
		t.Fatalf("buildEvalRunsWithFetcher returned error: %v", err)
	}

	report := reportFromRuns(reportRequest{
		Source:            f.grafanaURL,
		Datasource:        f.datasource,
		QueryFrom:         queryFrom,
		From:              from,
		To:                to,
		Preroll:           preroll,
		Step:              f.step,
		EvalInterval:      f.evalInterval,
		ChunkSize:         f.chunkSize,
		DelayResolutionBy: f.delayResolutionBy,
		CorrelationLabels: correlationLabels,
		ForDurations:      forDurations,
	}, runs)

	var out bytes.Buffer
	renderGrafanaReport(&out, report, f.verbose)
	return out.String(), queryFrom
}

func sortForDurations(forDurations []time.Duration) {
	for i := 0; i < len(forDurations); i++ {
		for j := i + 1; j < len(forDurations); j++ {
			if forDurations[j] < forDurations[i] {
				forDurations[i], forDurations[j] = forDurations[j], forDurations[i]
			}
		}
	}
}

func TestParseClauses(t *testing.T) {
	t.Run("single query", func(t *testing.T) {
		chain, err := parseClauses([]clauseToken{{kind: tokQuery, value: "up"}})
		if err != nil {
			t.Fatalf("parseClauses returned error: %v", err)
		}
		if len(chain.clauses) != 1 || len(chain.joins) != 0 {
			t.Fatalf("unexpected clause chain: %+v", chain)
		}
		if chain.clauses[0].query != "up" || chain.clauses[0].op != "" {
			t.Fatalf("unexpected first clause: %+v", chain.clauses[0])
		}
	})

	t.Run("chain with predicates", func(t *testing.T) {
		chain, err := parseClauses([]clauseToken{
			{kind: tokQuery, value: "a"},
			{kind: tokCmp, value: ">|5"},
			{kind: tokAnd},
			{kind: tokQuery, value: "b"},
			{kind: tokCmp, value: "<|3"},
		})
		if err != nil {
			t.Fatalf("parseClauses returned error: %v", err)
		}
		if len(chain.clauses) != 2 || len(chain.joins) != 1 || chain.joins[0] != tokAnd {
			t.Fatalf("unexpected clause chain: %+v", chain)
		}
		if chain.clauses[0].op != ">" || len(chain.clauses[0].rhs) != 1 || chain.clauses[0].rhs[0] != "5" {
			t.Fatalf("unexpected first clause: %+v", chain.clauses[0])
		}
		if chain.clauses[1].op != "<" || len(chain.clauses[1].rhs) != 1 || chain.clauses[1].rhs[0] != "3" {
			t.Fatalf("unexpected second clause: %+v", chain.clauses[1])
		}
	})

	tests := []struct {
		name string
		in   []clauseToken
		want string
	}{
		{name: "comparator before query", in: []clauseToken{{kind: tokCmp, value: ">|5"}}, want: "first clause flag must be --query"},
		{
			name: "duplicate comparator",
			in: []clauseToken{
				{kind: tokQuery, value: "a"},
				{kind: tokCmp, value: ">|5"},
				{kind: tokCmp, value: "<|3"},
			},
			want: "multiple comparator flags",
		},
		{
			name: "trailing join",
			in: []clauseToken{
				{kind: tokQuery, value: "a"},
				{kind: tokAnd},
			},
			want: "trailing --and/--or",
		},
		{
			name: "consecutive joins",
			in: []clauseToken{
				{kind: tokQuery, value: "a"},
				{kind: tokAnd},
				{kind: tokOr},
			},
			want: "consecutive joins",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseClauses(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseClauses error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestValidateQueryForComparator(t *testing.T) {
	tests := []struct {
		query   string
		wantErr string
	}{
		{query: "a and b", wantErr: "top-level operator"},
		{query: "a or b", wantErr: "top-level operator"},
		{query: "x > 5", wantErr: "top-level operator"},
		{query: "a / b"},
		{query: "sum(rate(x[5m]))"},
		{query: "(a and b)"},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			err := validateQueryForComparator(tc.query)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateQueryForComparator returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateQueryForComparator error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestApplyPredicateBoundaries(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	base := []model.Series{{
		Labels: map[string]string{"x": "1"},
		Samples: []model.Sample{
			{Timestamp: t0, Value: 4},
			{Timestamp: t0.Add(time.Minute), Value: 5},
			{Timestamp: t0.Add(2 * time.Minute), Value: 6},
			{Timestamp: t0.Add(3 * time.Minute), Value: math.NaN()},
		},
	}}

	tests := []struct {
		name string
		op   string
		rhs  float64
		want []float64
	}{
		{name: "gt", op: ">", rhs: 5, want: []float64{6}},
		{name: "ge", op: ">=", rhs: 5, want: []float64{5, 6}},
		{name: "lt", op: "<", rhs: 5, want: []float64{4}},
		{name: "le", op: "<=", rhs: 5, want: []float64{4, 5}},
		{name: "eq", op: "==", rhs: 5, want: []float64{5}},
		{name: "ne", op: "!=", rhs: 5, want: []float64{4, 6}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pred, err := predicateFor(tc.op, tc.rhs)
			if err != nil {
				t.Fatalf("predicateFor returned error: %v", err)
			}
			got := applyPredicate(base, pred)
			if len(got) != 1 || len(got[0].Samples) != len(tc.want) {
				t.Fatalf("applyPredicate returned %v, want %d samples", got, len(tc.want))
			}
			for i, v := range tc.want {
				if got[0].Samples[i].Value != v {
					t.Fatalf("sample %d = %v, want %v", i, got[0].Samples[i].Value, v)
				}
			}
		})
	}
}

func TestFilterFiringsToWindowKeepsOverlaps(t *testing.T) {
	t0 := time.Date(2025, 5, 1, 10, 0, 0, 0, time.UTC)
	results := []model.AlertResult{{
		LabelSet: map[string]string{"cluster": "a"},
		Fired:    true,
		Firings: []model.FiringRange{
			{FirstFired: t0.Add(-10 * time.Minute), LastFired: t0.Add(-5 * time.Minute)},
			{FirstFired: t0.Add(-2 * time.Minute), LastFired: t0.Add(2 * time.Minute)},
			{FirstFired: t0.Add(5 * time.Minute), LastFired: t0.Add(8 * time.Minute)},
			{FirstFired: t0.Add(15 * time.Minute), LastFired: t0.Add(16 * time.Minute)},
		},
	}}

	got := filterFiringsToWindow(results, t0, t0.Add(10*time.Minute))
	if len(got) != 1 || len(got[0].Firings) != 2 {
		t.Fatalf("filterFiringsToWindow returned %v, want 2 firings", got)
	}
	if got[0].Firings[0].FirstFired != t0.Add(-2*time.Minute) {
		t.Fatalf("first kept firing = %v, want overlap from preroll", got[0].Firings[0])
	}
	if got[0].Firings[1].FirstFired != t0.Add(5*time.Minute) {
		t.Fatalf("second kept firing = %v, want in-window firing", got[0].Firings[1])
	}
}

func TestRenderChain(t *testing.T) {
	tests := []struct {
		name     string
		displays []string
		joins    []tokenKind
		want     string
	}{
		{name: "single", displays: []string{"A"}, want: "(A)"},
		{name: "and", displays: []string{"A", "B"}, joins: []tokenKind{tokAnd}, want: "(A) AND (B)"},
		{name: "mixed", displays: []string{"A", "B", "C"}, joins: []tokenKind{tokAnd, tokOr}, want: "(A) AND (B) OR (C)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderChain(tc.displays, tc.joins); got != tc.want {
				t.Fatalf("renderChain = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGrafanaPipelineLocalThreshold(t *testing.T) {
	from := time.Date(2025, 5, 1, 1, 0, 0, 0, time.UTC)
	f := &grafanaFlags{
		grafanaURL:   "https://grafana.example.com",
		datasource:   "prom",
		clauseTokens: []clauseToken{{kind: tokQuery, value: "metric"}, {kind: tokCmp, value: ">|5"}},
		forRaw:       []string{"0m"},
		fromRaw:      from.Format(time.RFC3339),
		toRaw:        from.Add(3 * time.Minute).Format(time.RFC3339),
		chunkSize:    time.Hour,
		step:         time.Minute,
		evalInterval: time.Minute,
		noProgress:   true,
	}

	out, queryFrom := renderGrafanaFake(t, f, map[string]fakeQueryResponse{
		"metric": fakeResponse(seriesAtMinutes(from, map[string]string{"arena": "amber", "bot": "r1"},
			minutePoint{offset: 0, value: 4},
			minutePoint{offset: 1, value: 6},
			minutePoint{offset: 2, value: 7},
		)),
	})

	if !strings.Contains(out, "local threshold > 5: 2 samples pass") {
		t.Fatalf("output missing local-threshold summary:\n%s", out)
	}
	if !strings.Contains(out, "- for 0m: 1 firings") {
		t.Fatalf("output missing firing count:\n%s", out)
	}
	if queryFrom != from.Add(-2*time.Hour) {
		t.Fatalf("queryFrom = %s, want %s", queryFrom, from.Add(-2*time.Hour))
	}
}

func TestGrafanaPipelineMultiClausePrecedence(t *testing.T) {
	from := time.Date(2025, 5, 1, 1, 0, 0, 0, time.UTC)
	f := &grafanaFlags{
		grafanaURL: "https://grafana.example.com",
		datasource: "prom",
		clauseTokens: []clauseToken{
			{kind: tokQuery, value: "a"}, {kind: tokCmp, value: ">|0"}, {kind: tokAnd},
			{kind: tokQuery, value: "b"}, {kind: tokCmp, value: ">|0"}, {kind: tokOr},
			{kind: tokQuery, value: "c"}, {kind: tokCmp, value: ">|0"},
		},
		forRaw:       []string{"0m"},
		fromRaw:      from.Format(time.RFC3339),
		toRaw:        from.Add(4 * time.Minute).Format(time.RFC3339),
		chunkSize:    time.Hour,
		step:         time.Minute,
		evalInterval: time.Minute,
		noProgress:   true,
	}

	out, _ := renderGrafanaFake(t, f, map[string]fakeQueryResponse{
		"a": fakeResponse(seriesAtMinutes(from, map[string]string{"arena": "amber", "bot": "r1"}, minutePoint{offset: 1, value: 1})),
		"b": fakeResponse(seriesAtMinutes(from, map[string]string{"arena": "amber", "bot": "r1"}, minutePoint{offset: 2, value: 1})),
		"c": fakeResponse(seriesAtMinutes(from, map[string]string{"arena": "amber", "bot": "r1"}, minutePoint{offset: 3, value: 1})),
	})

	if !strings.Contains(out, "combined: 1 series after 1 AND / 1 OR joins") {
		t.Fatalf("output missing combine summary:\n%s", out)
	}
	if !strings.Contains(out, "- for 0m: 1 firings") {
		t.Fatalf("output missing precedence-sensitive firing count:\n%s", out)
	}
}

func TestRunGrafanaRejectsSweepInMultiClause(t *testing.T) {
	f := &grafanaFlags{
		clauseTokens: []clauseToken{
			{kind: tokQuery, value: "a"}, {kind: tokCmp, value: ">|1,2"}, {kind: tokAnd},
			{kind: tokQuery, value: "b"},
		},
		fromRaw: "2025-05-01T00:00:00Z",
		toRaw:   "2025-05-01T01:00:00Z",
	}

	err := runGrafana(f)
	if err == nil || !strings.Contains(err.Error(), "threshold sweeps are not supported") {
		t.Fatalf("runGrafana error = %v, want sweep rejection", err)
	}
}

func TestGrafanaPipelineForDurations(t *testing.T) {
	from := time.Date(2025, 5, 1, 1, 0, 0, 0, time.UTC)
	f := &grafanaFlags{
		grafanaURL:   "https://grafana.example.com",
		datasource:   "prom",
		clauseTokens: []clauseToken{{kind: tokQuery, value: "orbit_metric > 5"}},
		forRaw:       []string{"4m,2m"},
		fromRaw:      from.Format(time.RFC3339),
		toRaw:        from.Add(7 * time.Minute).Format(time.RFC3339),
		chunkSize:    time.Hour,
		step:         time.Minute,
		evalInterval: time.Minute,
		noProgress:   true,
		verbose:      true,
	}

	out, _ := renderGrafanaFake(t, f, map[string]fakeQueryResponse{
		"orbit_metric > 5": fakeResponse(seriesAtMinutes(from, map[string]string{"arena": "amber", "bot": "r1"},
			minutePoint{offset: 0, value: 6},
			minutePoint{offset: 1, value: 7},
			minutePoint{offset: 2, value: 8},
			minutePoint{offset: 3, value: 9},
			minutePoint{offset: 4, value: 8},
			minutePoint{offset: 5, value: 7},
			minutePoint{offset: 6, value: 6},
		)),
	})

	if !strings.Contains(out, "- for 2m: 1 firings") {
		t.Fatalf("output missing for=2m summary:\n%s", out)
	}
	if !strings.Contains(out, "- for 4m: 1 firings") {
		t.Fatalf("output missing for=4m summary:\n%s", out)
	}
	if !strings.Contains(out, "2025-05-01T01:02:00Z -> 2025-05-01T01:07:00Z (5m, max=9.00)") {
		t.Fatalf("output missing exact 2m firing window:\n%s", out)
	}
	if !strings.Contains(out, "2025-05-01T01:04:00Z -> 2025-05-01T01:07:00Z (3m, max=9.00)") {
		t.Fatalf("output missing exact 4m firing window:\n%s", out)
	}
}

func TestGrafanaPipelineDelayResolutionMergesFirings(t *testing.T) {
	from := time.Date(2025, 5, 1, 1, 0, 0, 0, time.UTC)
	f := &grafanaFlags{
		grafanaURL:        "https://grafana.example.com",
		datasource:        "prom",
		clauseTokens:      []clauseToken{{kind: tokQuery, value: "pulse_metric > 0"}},
		forRaw:            []string{"1m"},
		fromRaw:           from.Format(time.RFC3339),
		toRaw:             from.Add(10 * time.Minute).Format(time.RFC3339),
		chunkSize:         time.Hour,
		step:              time.Minute,
		evalInterval:      time.Minute,
		delayResolutionBy: 5 * time.Minute,
		noProgress:        true,
		verbose:           true,
	}

	out, _ := renderGrafanaFake(t, f, map[string]fakeQueryResponse{
		"pulse_metric > 0": fakeResponse(seriesAtMinutes(from, map[string]string{"arena": "amber", "bot": "r1"},
			minutePoint{offset: 0, value: 4},
			minutePoint{offset: 1, value: 5},
			minutePoint{offset: 2, value: 6},
			minutePoint{offset: 6, value: 5},
			minutePoint{offset: 7, value: 6},
			minutePoint{offset: 8, value: 7},
		)),
	})

	if !strings.Contains(out, "- for 1m: 1 firings") {
		t.Fatalf("output missing merged firing count:\n%s", out)
	}
	if !strings.Contains(out, "2025-05-01T01:01:00Z -> 2025-05-01T01:09:00Z (8m, max=7.00)") {
		t.Fatalf("output missing merged firing range:\n%s", out)
	}
}

func TestGrafanaPipelineGroupedFiringsAndIncidents(t *testing.T) {
	from := time.Date(2025, 5, 1, 1, 0, 0, 0, time.UTC)
	f := &grafanaFlags{
		grafanaURL:        "https://grafana.example.com",
		datasource:        "prom",
		clauseTokens:      []clauseToken{{kind: tokQuery, value: "fleet_metric > 0"}},
		forRaw:            []string{"1m"},
		fromRaw:           from.Format(time.RFC3339),
		toRaw:             from.Add(45 * time.Minute).Format(time.RFC3339),
		chunkSize:         time.Hour,
		step:              time.Minute,
		evalInterval:      time.Minute,
		delayResolutionBy: 3 * time.Minute,
		correlationID:     "arena",
		noProgress:        true,
		verbose:           true,
	}

	out, _ := renderGrafanaFake(t, f, map[string]fakeQueryResponse{
		"fleet_metric > 0": fakeResponse(
			seriesAtMinutes(from, map[string]string{"arena": "amber", "bot": "r1"},
				minutePoint{offset: 0, value: 5},
				minutePoint{offset: 1, value: 6},
				minutePoint{offset: 2, value: 7},
				minutePoint{offset: 20, value: 5},
				minutePoint{offset: 21, value: 6},
				minutePoint{offset: 22, value: 7},
			),
			seriesAtMinutes(from, map[string]string{"arena": "amber", "bot": "r2"},
				minutePoint{offset: 3, value: 4},
				minutePoint{offset: 4, value: 5},
				minutePoint{offset: 5, value: 6},
				minutePoint{offset: 23, value: 4},
				minutePoint{offset: 24, value: 5},
				minutePoint{offset: 25, value: 6},
			),
			seriesAtMinutes(from, map[string]string{"arena": "violet", "bot": "r3"},
				minutePoint{offset: 40, value: 8},
				minutePoint{offset: 41, value: 9},
				minutePoint{offset: 42, value: 10},
			),
		),
	})

	if !strings.Contains(out, "- for 1m: 5 firings, 3 grouped firings, 2 incidents") {
		t.Fatalf("output missing grouped summary:\n%s", out)
	}
	if !strings.Contains(out, "    {arena=amber}: 4 firings") {
		t.Fatalf("output missing first incident header:\n%s", out)
	}
	if !strings.Contains(out, "    {arena=violet}: 1 firings") {
		t.Fatalf("output missing second incident header:\n%s", out)
	}
	if !strings.Contains(out, "2025-05-01T01:24:00Z -> 2025-05-01T01:26:00Z (2m, max=6.00)") {
		t.Fatalf("output missing later amber firing detail:\n%s", out)
	}
}
