package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"alert-tester/internal/buildinfo"
	"alert-tester/internal/cache"
	"alert-tester/internal/grafana"
	"alert-tester/internal/model"
	"alert-tester/internal/query"

	"github.com/spf13/cobra"
)

type replayCaptureFlags struct {
	cacheDir          string
	forRaw            []string
	evalInterval      time.Duration
	delayResolutionBy time.Duration
	incidentGroupBy   string
	chunkSize         time.Duration
}

type replayManifest struct {
	SchemaVersion int              `json:"schema_version"`
	CreatedAt     string           `json:"created_at"`
	Revision      string           `json:"revision"`
	TreeState     string           `json:"tree_state"`
	BuildTime     string           `json:"build_time,omitempty"`
	CacheDir      string           `json:"cache_dir"`
	Config        replayConfigJSON `json:"config"`
	Counts        replayCounts     `json:"counts"`
}

type replayCounts struct {
	Completed int `json:"completed"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}

type replayConfigJSON struct {
	For               []string `json:"for"`
	EvalInterval      string   `json:"eval_interval"`
	DelayResolutionBy string   `json:"delay_resolution_by"`
	IncidentGroupBy   string   `json:"incident_group_by"`
	ChunkSize         string   `json:"chunk_size"`
}

type replayCaseSummary struct {
	ID           string                  `json:"id"`
	Status       string                  `json:"status"`
	Reason       string                  `json:"reason,omitempty"`
	Source       string                  `json:"source"`
	Datasource   string                  `json:"datasource"`
	Expr         string                  `json:"expr"`
	Step         string                  `json:"step"`
	QueryFrom    string                  `json:"query_from"`
	QueryTo      string                  `json:"query_to"`
	AnalysisFrom string                  `json:"analysis_from"`
	AnalysisTo   string                  `json:"analysis_to"`
	DetailPath   string                  `json:"detail_path,omitempty"`
	DetailSHA256 string                  `json:"detail_sha256,omitempty"`
	Query        replayQuerySummary      `json:"query"`
	Analyses     []replayAnalysisSummary `json:"analyses"`
}

type replayQuerySummary struct {
	Chunks         int `json:"chunks"`
	CacheHits      int `json:"cache_hits"`
	CacheMisses    int `json:"cache_misses"`
	SeriesReturned int `json:"series_returned"`
	SampleCount    int `json:"sample_count"`
	MaxCardinality int `json:"max_chunk_cardinality"`
}

type replayAnalysisSummary struct {
	For            string `json:"for"`
	TotalFirings   int    `json:"total_firings"`
	GroupedFirings int    `json:"grouped_firings,omitempty"`
	IncidentCount  int    `json:"incident_count,omitempty"`
}

type replayCaseDetail struct {
	SchemaVersion int                    `json:"schema_version"`
	ID            string                 `json:"id"`
	Source        string                 `json:"source"`
	Datasource    string                 `json:"datasource"`
	Expr          string                 `json:"expr"`
	Step          string                 `json:"step"`
	QueryFrom     string                 `json:"query_from"`
	QueryTo       string                 `json:"query_to"`
	AnalysisFrom  string                 `json:"analysis_from"`
	AnalysisTo    string                 `json:"analysis_to"`
	Config        replayConfigJSON       `json:"config"`
	Fetches       []replayFetchDetail    `json:"fetches"`
	Analyses      []replayAnalysisDetail `json:"analyses"`
}

type replayFetchDetail struct {
	Expr           string `json:"expr"`
	Local          bool   `json:"local_threshold"`
	Threshold      string `json:"threshold,omitempty"`
	SamplesPass    int    `json:"samples_pass"`
	Chunks         int    `json:"chunks"`
	CacheHits      int    `json:"cache_hits"`
	CacheMisses    int    `json:"cache_misses"`
	SeriesReturn   int    `json:"series_returned"`
	SampleCount    int    `json:"sample_count"`
	MaxCardinality int    `json:"max_chunk_cardinality"`
}

type replayAnalysisDetail struct {
	For            string                 `json:"for"`
	TotalFirings   int                    `json:"total_firings"`
	GroupedFirings int                    `json:"grouped_firings,omitempty"`
	IncidentCount  int                    `json:"incident_count,omitempty"`
	Results        []replayAlertResult    `json:"results"`
	Incidents      []replayIncidentDetail `json:"incidents,omitempty"`
}

type replayAlertResult struct {
	LabelSet map[string]string `json:"label_set"`
	Fired    bool              `json:"fired"`
	Firings  []replayFiring    `json:"firings"`
}

type replayIncidentDetail struct {
	CorrelationKey string            `json:"correlation_key"`
	Labels         map[string]string `json:"labels"`
	FirstPending   string            `json:"first_pending"`
	LastFired      string            `json:"last_fired"`
	Firings        []replayFiring    `json:"firings"`
}

type replayFiring struct {
	FirstPending string  `json:"first_pending"`
	FirstFired   string  `json:"first_fired"`
	LastFired    string  `json:"last_fired"`
	MaxValue     float64 `json:"max_value"`
}

type discoveredReplayCase struct {
	ID           string
	Source       string
	Datasource   string
	Expr         string
	Step         time.Duration
	QueryFrom    time.Time
	QueryTo      time.Time
	AnalysisFrom time.Time
	AnalysisTo   time.Time
	Chunks       []query.TimeRange
}

func newReplayCaptureCmd() *cobra.Command {
	f := replayCaptureFlags{
		cacheDir:          defaultCacheDir(),
		evalInterval:      replayDefaults.EvalInterval,
		delayResolutionBy: replayDefaults.DelayResolutionBy,
		incidentGroupBy:   replayDefaults.IncidentGroupBy,
		chunkSize:         replayDefaults.ChunkSize,
	}
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Capture a replay baseline from local cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReplayCapture(&f)
		},
	}
	cmd.Flags().StringVar(&f.cacheDir, "cache-dir", f.cacheDir, "Cache directory to scan")
	cmd.Flags().StringSliceVar(&f.forRaw, "for", durationsJSON(replayDefaults.ForDurations), "Replay for: durations, comma-separated")
	cmd.Flags().DurationVar(&f.evalInterval, "eval-interval", f.evalInterval, "Replay evaluation interval")
	cmd.Flags().DurationVar(&f.delayResolutionBy, "delay-resolution-by", f.delayResolutionBy, "Replay resolution delay")
	cmd.Flags().StringVar(&f.incidentGroupBy, "incident-group-by", f.incidentGroupBy, "Replay incident grouping labels")
	cmd.Flags().DurationVar(&f.chunkSize, "chunk-size", f.chunkSize, "Replay chunk size used for grafana-style preroll inference")
	return cmd
}

func runReplayCapture(f *replayCaptureFlags) error {
	forDurations, err := parseForDurations(f.forRaw)
	if err != nil {
		return err
	}
	sort.Slice(forDurations, func(i, j int) bool {
		return forDurations[i] < forDurations[j]
	})

	cfg := replayConfig{
		ForDurations:      forDurations,
		EvalInterval:      f.evalInterval,
		DelayResolutionBy: f.delayResolutionBy,
		IncidentGroupBy:   f.incidentGroupBy,
		ChunkSize:         f.chunkSize,
	}

	entries, err := cache.ScanEntries(f.cacheDir)
	if err != nil {
		return err
	}

	cases, skipped := discoverReplayCases(entries, cfg)

	root, err := projectRoot()
	if err != nil {
		return err
	}
	runDir := filepath.Join(root, ".local", "replays", replayRunDirName(time.Now()))
	detailsDir := filepath.Join(runDir, "details")
	if err := os.MkdirAll(detailsDir, 0o755); err != nil {
		return err
	}

	casesFile, err := os.Create(filepath.Join(runDir, "cases.jsonl.gz"))
	if err != nil {
		return err
	}
	defer casesFile.Close()
	casesGzip := gzip.NewWriter(casesFile)
	defer casesGzip.Close()

	manifest := replayManifest{
		SchemaVersion: replaySchemaVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Revision:      buildinfo.Current().Revision,
		TreeState:     buildinfo.Current().TreeState(),
		BuildTime:     buildinfo.Current().BuildTime,
		CacheDir:      f.cacheDir,
		Config:        cfg.json(),
		Counts: replayCounts{
			Skipped: len(skipped),
		},
	}

	for _, summary := range skipped {
		if err := writeJSONLine(casesGzip, summary); err != nil {
			return err
		}
	}

	for _, rc := range cases {
		summary, detailBytes, err := captureReplayCase(cfg, f.cacheDir, rc)
		if err != nil {
			manifest.Counts.Failed++
			summary := replayCaseSummary{
				ID:           rc.ID,
				Status:       "error",
				Reason:       err.Error(),
				Source:       rc.Source,
				Datasource:   rc.Datasource,
				Expr:         rc.Expr,
				Step:         trimDur(rc.Step),
				QueryFrom:    rc.QueryFrom.Format(time.RFC3339),
				QueryTo:      rc.QueryTo.Format(time.RFC3339),
				AnalysisFrom: rc.AnalysisFrom.Format(time.RFC3339),
				AnalysisTo:   rc.AnalysisTo.Format(time.RFC3339),
			}
			if err := writeJSONLine(casesGzip, summary); err != nil {
				return err
			}
			continue
		}

		detailRelPath := filepath.Join("details", rc.ID+".json.gz")
		if err := os.WriteFile(filepath.Join(runDir, detailRelPath), detailBytes, 0o644); err != nil {
			return err
		}
		digest := sha256.Sum256(detailBytes)
		summary.DetailPath = detailRelPath
		summary.DetailSHA256 = hex.EncodeToString(digest[:])
		if err := writeJSONLine(casesGzip, summary); err != nil {
			return err
		}
		manifest.Counts.Completed++
	}

	if err := casesGzip.Close(); err != nil {
		return err
	}
	if err := casesFile.Close(); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(runDir, "manifest.json"), manifest); err != nil {
		return err
	}

	fmt.Printf("replay capture written to %s\n", runDir)
	fmt.Printf("completed: %d, skipped: %d, failed: %d\n", manifest.Counts.Completed, manifest.Counts.Skipped, manifest.Counts.Failed)
	return nil
}

func discoverReplayCases(entries []cache.Metadata, cfg replayConfig) ([]discoveredReplayCase, []replayCaseSummary) {
	type groupKey struct {
		source     string
		datasource string
		expr       string
		step       time.Duration
	}
	grouped := make(map[groupKey][]cache.Metadata)
	for _, entry := range entries {
		key := groupKey{
			source:     entry.GrafanaURL,
			datasource: entry.Datasource,
			expr:       entry.Expr,
			step:       entry.Step,
		}
		grouped[key] = append(grouped[key], entry)
	}

	var cases []discoveredReplayCase
	var skipped []replayCaseSummary
	for key, group := range grouped {
		sort.Slice(group, func(i, j int) bool {
			if group[i].From.Equal(group[j].From) {
				return group[i].To.Before(group[j].To)
			}
			return group[i].From.Before(group[j].From)
		})

		current := []query.TimeRange{{From: group[0].From, To: group[0].To}}
		spanStart, spanEnd := group[0].From, group[0].To
		for _, entry := range group[1:] {
			if entry.From.After(spanEnd) {
				appendReplayCase(&cases, &skipped, key.source, key.datasource, key.expr, key.step, spanStart, spanEnd, current, cfg)
				current = []query.TimeRange{{From: entry.From, To: entry.To}}
				spanStart, spanEnd = entry.From, entry.To
				continue
			}
			current = append(current, query.TimeRange{From: entry.From, To: entry.To})
			if entry.To.After(spanEnd) {
				spanEnd = entry.To
			}
		}
		appendReplayCase(&cases, &skipped, key.source, key.datasource, key.expr, key.step, spanStart, spanEnd, current, cfg)
	}

	sort.Slice(cases, func(i, j int) bool {
		if cases[i].Source != cases[j].Source {
			return cases[i].Source < cases[j].Source
		}
		if cases[i].Expr != cases[j].Expr {
			return cases[i].Expr < cases[j].Expr
		}
		return cases[i].QueryFrom.Before(cases[j].QueryFrom)
	})
	sort.Slice(skipped, func(i, j int) bool {
		if skipped[i].Source != skipped[j].Source {
			return skipped[i].Source < skipped[j].Source
		}
		if skipped[i].Expr != skipped[j].Expr {
			return skipped[i].Expr < skipped[j].Expr
		}
		return skipped[i].QueryFrom < skipped[j].QueryFrom
	})
	return cases, skipped
}

func appendReplayCase(cases *[]discoveredReplayCase, skipped *[]replayCaseSummary, source, datasource, expr string, step time.Duration, queryFrom, queryTo time.Time, chunks []query.TimeRange, cfg replayConfig) {
	preroll := replayPreroll(cfg)
	analysisFrom := queryFrom.Add(preroll)
	analysisTo := queryTo
	id := replayCaseID(source, datasource, expr, step, queryFrom, queryTo, analysisFrom, analysisTo)

	if !analysisFrom.Before(analysisTo) || analysisTo.Sub(analysisFrom) < cfg.EvalInterval {
		*skipped = append(*skipped, replayCaseSummary{
			ID:           id,
			Status:       "skipped",
			Reason:       fmt.Sprintf("window shorter than replay eval interval after preroll (%s)", trimDur(cfg.EvalInterval)),
			Source:       source,
			Datasource:   datasource,
			Expr:         expr,
			Step:         trimDur(step),
			QueryFrom:    queryFrom.Format(time.RFC3339),
			QueryTo:      queryTo.Format(time.RFC3339),
			AnalysisFrom: analysisFrom.Format(time.RFC3339),
			AnalysisTo:   analysisTo.Format(time.RFC3339),
		})
		return
	}

	deduped := dedupeRanges(chunks)
	*cases = append(*cases, discoveredReplayCase{
		ID:           id,
		Source:       source,
		Datasource:   datasource,
		Expr:         expr,
		Step:         step,
		QueryFrom:    queryFrom,
		QueryTo:      queryTo,
		AnalysisFrom: analysisFrom,
		AnalysisTo:   analysisTo,
		Chunks:       deduped,
	})
}

func dedupeRanges(chunks []query.TimeRange) []query.TimeRange {
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].From.Equal(chunks[j].From) {
			return chunks[i].To.Before(chunks[j].To)
		}
		return chunks[i].From.Before(chunks[j].From)
	})
	out := make([]query.TimeRange, 0, len(chunks))
	seen := make(map[string]struct{}, len(chunks))
	for _, chunk := range chunks {
		key := chunk.From.Format(time.RFC3339Nano) + "|" + chunk.To.Format(time.RFC3339Nano)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, chunk)
	}
	return out
}

func replayPreroll(cfg replayConfig) time.Duration {
	return computePreroll(cfg.ForDurations, cfg.DelayResolutionBy, cfg.ChunkSize)
}

func replayCaseID(source, datasource, expr string, step time.Duration, queryFrom, queryTo, analysisFrom, analysisTo time.Time) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		source,
		datasource,
		expr,
		trimDur(step),
		queryFrom.UTC().Format(time.RFC3339),
		queryTo.UTC().Format(time.RFC3339),
		analysisFrom.UTC().Format(time.RFC3339),
		analysisTo.UTC().Format(time.RFC3339),
	}, "\x00")))
	return hex.EncodeToString(h[:8])
}

func captureReplayCase(cfg replayConfig, cacheDir string, rc discoveredReplayCase) (replayCaseSummary, []byte, error) {
	client := grafana.NewClient(rc.Source, "")
	eng := &query.Engine{
		Client:    client,
		Cache:     cache.New(cacheDir, true),
		ChunkSize: cfg.ChunkSize,
		CacheOnly: true,
		Output:    io.Discard,
	}
	chain := &clauseChain{clauses: []clause{{query: rc.Expr}}}
	runs, err := buildEvalRunsWithFetcher(chain, func(r run) ([]model.Series, reportFetch, error) {
		return fetchAndFilterChunks(eng, rc.Datasource, r, rc.Chunks, rc.Step)
	})
	if err != nil {
		return replayCaseSummary{}, nil, err
	}
	report := reportFromRuns(reportRequest{
		Source:            rc.Source,
		Datasource:        rc.Datasource,
		QueryFrom:         rc.QueryFrom,
		From:              rc.AnalysisFrom,
		To:                rc.AnalysisTo,
		Preroll:           rc.AnalysisFrom.Sub(rc.QueryFrom),
		Step:              rc.Step,
		EvalInterval:      cfg.EvalInterval,
		ChunkSize:         cfg.ChunkSize,
		DelayResolutionBy: cfg.DelayResolutionBy,
		CorrelationLabels: cfg.correlationLabels(),
		ForDurations:      cfg.ForDurations,
	}, runs)

	summary, detail := makeReplayArtifacts(cfg, rc, report)
	detailBytes, err := marshalGzippedJSON(detail)
	if err != nil {
		return replayCaseSummary{}, nil, err
	}
	return summary, detailBytes, nil
}

func fetchAndFilterChunks(eng *query.Engine, datasource string, r run, chunks []query.TimeRange, step time.Duration) ([]model.Series, reportFetch, error) {
	result, stats, err := eng.QueryChunks(datasource, r.fetchExpr, chunks, step)
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

func makeReplayArtifacts(cfg replayConfig, rc discoveredReplayCase, report *grafanaReport) (replayCaseSummary, replayCaseDetail) {
	detail := replayCaseDetail{
		SchemaVersion: replaySchemaVersion,
		ID:            rc.ID,
		Source:        rc.Source,
		Datasource:    rc.Datasource,
		Expr:          rc.Expr,
		Step:          trimDur(rc.Step),
		QueryFrom:     rc.QueryFrom.Format(time.RFC3339),
		QueryTo:       rc.QueryTo.Format(time.RFC3339),
		AnalysisFrom:  rc.AnalysisFrom.Format(time.RFC3339),
		AnalysisTo:    rc.AnalysisTo.Format(time.RFC3339),
		Config:        cfg.json(),
	}
	summary := replayCaseSummary{
		ID:           rc.ID,
		Status:       "ok",
		Source:       rc.Source,
		Datasource:   rc.Datasource,
		Expr:         rc.Expr,
		Step:         trimDur(rc.Step),
		QueryFrom:    rc.QueryFrom.Format(time.RFC3339),
		QueryTo:      rc.QueryTo.Format(time.RFC3339),
		AnalysisFrom: rc.AnalysisFrom.Format(time.RFC3339),
		AnalysisTo:   rc.AnalysisTo.Format(time.RFC3339),
	}
	if len(report.Runs) == 0 {
		return summary, detail
	}

	run := report.Runs[0]
	detail.Fetches = make([]replayFetchDetail, len(run.Fetches))
	for i, fetch := range run.Fetches {
		detail.Fetches[i] = replayFetchDetail{
			Expr:           fetch.Expr,
			Local:          fetch.Threshold.Local,
			Threshold:      fetch.Threshold.Label,
			SamplesPass:    fetch.Threshold.SamplesPass,
			Chunks:         fetch.Query.Chunks,
			CacheHits:      fetch.Query.CacheHits,
			CacheMisses:    fetch.Query.CacheMisses,
			SeriesReturn:   fetch.Query.SeriesReturned,
			SampleCount:    fetch.Query.SampleCount,
			MaxCardinality: fetch.Query.MaxCardinality,
		}
		if i == 0 {
			summary.Query = replayQuerySummary{
				Chunks:         fetch.Query.Chunks,
				CacheHits:      fetch.Query.CacheHits,
				CacheMisses:    fetch.Query.CacheMisses,
				SeriesReturned: fetch.Query.SeriesReturned,
				SampleCount:    fetch.Query.SampleCount,
				MaxCardinality: fetch.Query.MaxCardinality,
			}
		}
	}

	detail.Analyses = make([]replayAnalysisDetail, 0, len(run.Analyses))
	summary.Analyses = make([]replayAnalysisSummary, 0, len(run.Analyses))
	for _, analysis := range run.Analyses {
		detailAnal := replayAnalysisDetail{
			For:            formatDuration(analysis.ForDuration),
			TotalFirings:   analysis.TotalFirings,
			GroupedFirings: analysis.GroupedFirings,
			IncidentCount:  len(analysis.Incidents),
			Results:        make([]replayAlertResult, 0, len(analysis.Results)),
			Incidents:      make([]replayIncidentDetail, 0, len(analysis.Incidents)),
		}
		for _, result := range analysis.Results {
			detailAnal.Results = append(detailAnal.Results, replayAlertResult{
				LabelSet: result.LabelSet,
				Fired:    result.Fired,
				Firings:  marshalFirings(result.Firings),
			})
		}
		for _, incident := range analysis.Incidents {
			detailAnal.Incidents = append(detailAnal.Incidents, replayIncidentDetail{
				CorrelationKey: incident.CorrelationKey,
				Labels:         incident.Labels,
				FirstPending:   incident.FirstPending.Format(time.RFC3339),
				LastFired:      incident.LastFired.Format(time.RFC3339),
				Firings:        marshalFirings(incident.Firings),
			})
		}
		detail.Analyses = append(detail.Analyses, detailAnal)
		summary.Analyses = append(summary.Analyses, replayAnalysisSummary{
			For:            detailAnal.For,
			TotalFirings:   detailAnal.TotalFirings,
			GroupedFirings: detailAnal.GroupedFirings,
			IncidentCount:  detailAnal.IncidentCount,
		})
	}

	return summary, detail
}

func marshalFirings(firings []model.FiringRange) []replayFiring {
	out := make([]replayFiring, len(firings))
	for i, firing := range firings {
		out[i] = replayFiring{
			FirstPending: firing.FirstPending.Format(time.RFC3339),
			FirstFired:   firing.FirstFired.Format(time.RFC3339),
			LastFired:    firing.LastFired.Format(time.RFC3339),
			MaxValue:     firing.MaxValue,
		}
	}
	return out
}

func marshalGzippedJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return gz.Bytes(), nil
}

func writeJSONLine(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	return enc.Encode(v)
}

func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
