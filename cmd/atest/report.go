package main

import (
	"time"

	"alert-tester/internal/cache"
	"alert-tester/internal/eval"
	"alert-tester/internal/model"
	"alert-tester/internal/query"
)

type analysisConfig struct {
	ForDurations      []time.Duration
	EvalInterval      time.Duration
	DelayResolutionBy time.Duration
	CorrelationLabels []string
	From              time.Time
	To                time.Time
}

type grafanaReport struct {
	Source       string
	Datasource   string
	StartTime    time.Time
	EndTime      time.Time
	QueryFrom    time.Time
	Preroll      time.Duration
	Step         time.Duration
	EvalInterval time.Duration
	ChunkSize    time.Duration
	Runs         []reportRun
}

type reportRun struct {
	DisplayExpr string
	Fetches     []reportFetch
	ExtraLines  []string
	NoData      bool
	Analyses    []reportAnalysis
}

type reportFetch struct {
	Expr      string
	Query     reportQueryStats
	Threshold reportThreshold
}

type reportQueryStats struct {
	Chunks         int
	CacheHits      int
	CacheMisses    int
	CacheTime      time.Duration
	FetchTime      time.Duration
	SeriesReturned int
	SampleCount    int
	MaxCardinality int
}

type reportThreshold struct {
	Local       bool
	Label       string
	SamplesPass int
}

type reportAnalysis struct {
	ForDuration    time.Duration
	Results        []model.AlertResult
	TotalFirings   int
	GroupedFirings int
	Incidents      []model.Incident
}

func buildGrafanaReport(f *grafanaFlags, eng *query.Engine, chain *clauseChain, queryFrom, from, to, sourceStart time.Time, preroll time.Duration, correlationLabels []string, forDurations []time.Duration) (*grafanaReport, error) {
	runs, err := buildEvalRuns(eng, f.datasource, chain, queryFrom, to, f.step)
	if err != nil {
		return nil, err
	}

	report := &grafanaReport{
		Source:       f.grafanaURL,
		Datasource:   f.datasource,
		StartTime:    from,
		EndTime:      to,
		QueryFrom:    sourceStart,
		Preroll:      preroll,
		Step:         f.step,
		EvalInterval: f.evalInterval,
		ChunkSize:    f.chunkSize,
		Runs:         make([]reportRun, 0, len(runs)),
	}

	cfg := analysisConfig{
		ForDurations:      forDurations,
		EvalInterval:      f.evalInterval,
		DelayResolutionBy: f.delayResolutionBy,
		CorrelationLabels: correlationLabels,
		From:              from,
		To:                to,
	}

	for _, run := range runs {
		report.Runs = append(report.Runs, analyzeRun(run, cfg))
	}

	return report, nil
}

func analyzeRun(run evalRun, cfg analysisConfig) reportRun {
	out := reportRun{
		DisplayExpr: run.displayExpr,
		Fetches:     make([]reportFetch, len(run.fetches)),
		ExtraLines:  append([]string(nil), run.extraLines...),
		NoData:      len(run.series) == 0,
	}
	for i, fetch := range run.fetches {
		out.Fetches[i] = fetch
	}
	if out.NoData {
		return out
	}

	out.Analyses = make([]reportAnalysis, 0, len(cfg.ForDurations))
	for _, forDur := range cfg.ForDurations {
		results := eval.EvaluateFor(run.series, forDur, cfg.EvalInterval)
		if cfg.DelayResolutionBy > 0 {
			results = mergePerSeries(results, cfg.DelayResolutionBy)
		}
		results = filterFiringsToWindow(results, cfg.From, cfg.To)

		analysis := reportAnalysis{
			ForDuration: forDur,
			Results:     results,
		}
		for _, r := range results {
			analysis.TotalFirings += len(r.Firings)
		}
		if analysis.TotalFirings > 0 && len(cfg.CorrelationLabels) > 0 {
			analysis.GroupedFirings = eval.CorrelatedFirings(results, cfg.CorrelationLabels, cfg.DelayResolutionBy)
			analysis.Incidents = eval.GroupIncidents(results, cfg.CorrelationLabels)
		}
		out.Analyses = append(out.Analyses, analysis)
	}

	return out
}

func makeReportFetch(fetchExpr string, stats query.Stats, thresholdLabel string, predicateApplied bool, samplesPass int) reportFetch {
	return reportFetch{
		Expr: cache.NormalizeExpr(fetchExpr),
		Query: reportQueryStats{
			Chunks:         stats.Chunks,
			CacheHits:      stats.CacheHits,
			CacheMisses:    stats.CacheMisses,
			CacheTime:      stats.CacheTime,
			FetchTime:      stats.FetchTime,
			SeriesReturned: stats.SeriesReturned,
			SampleCount:    stats.SampleCount,
			MaxCardinality: stats.MaxCardinality,
		},
		Threshold: reportThreshold{
			Local:       predicateApplied,
			Label:       thresholdLabel,
			SamplesPass: samplesPass,
		},
	}
}
