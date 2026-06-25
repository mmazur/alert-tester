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

type reportRequest struct {
	Source            string
	Datasource        string
	QueryFrom         time.Time
	From              time.Time
	To                time.Time
	Preroll           time.Duration
	Step              time.Duration
	EvalInterval      time.Duration
	ChunkSize         time.Duration
	DelayResolutionBy time.Duration
	CorrelationLabels []string
	ForDurations      []time.Duration
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
	ForDuration              time.Duration
	Results                  []model.AlertResult
	TotalFirings             int
	GroupedFirings           int
	SustainedFirings         int
	SustainedGroupedFirings  int
	SustainedWindowThreshold float64
	Incidents                []model.Incident
}

func reportFromRuns(req reportRequest, runs []evalRun) *grafanaReport {
	report := &grafanaReport{
		Source:       req.Source,
		Datasource:   req.Datasource,
		StartTime:    req.From,
		EndTime:      req.To,
		QueryFrom:    req.QueryFrom,
		Preroll:      req.Preroll,
		Step:         req.Step,
		EvalInterval: req.EvalInterval,
		ChunkSize:    req.ChunkSize,
		Runs:         make([]reportRun, 0, len(runs)),
	}

	cfg := analysisConfig{
		ForDurations:      req.ForDurations,
		EvalInterval:      req.EvalInterval,
		DelayResolutionBy: req.DelayResolutionBy,
		CorrelationLabels: req.CorrelationLabels,
		From:              req.From,
		To:                req.To,
	}

	for _, run := range runs {
		report.Runs = append(report.Runs, analyzeRun(run, cfg))
	}

	return report
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
			ForDuration:              forDur,
			Results:                  results,
			SustainedWindowThreshold: sustainedWindowThreshold,
		}
		for _, r := range results {
			analysis.TotalFirings += len(r.Firings)
		}
		analysis.SustainedFirings = countSustainedFirings(results, cfg.From, cfg.To, cfg.EvalInterval, sustainedWindowThreshold)
		if analysis.TotalFirings > 0 && len(cfg.CorrelationLabels) > 0 {
			analysis.GroupedFirings = eval.CorrelatedFirings(results, cfg.CorrelationLabels, cfg.DelayResolutionBy)
			analysis.Incidents = eval.GroupIncidents(results, cfg.CorrelationLabels)
			analysis.SustainedGroupedFirings = countSustainedGroupedFirings(results, cfg.CorrelationLabels, cfg.From, cfg.To, cfg.EvalInterval, cfg.DelayResolutionBy, sustainedWindowThreshold)
		}
		out.Analyses = append(out.Analyses, analysis)
	}

	return out
}

const sustainedWindowThreshold = 0.9

func countSustainedFirings(results []model.AlertResult, from, to time.Time, evalInterval time.Duration, threshold float64) int {
	count := 0
	for _, r := range results {
		for _, f := range r.Firings {
			if firingWindowRatio(f, from, to, evalInterval) >= threshold {
				count++
			}
		}
	}
	return count
}

func countSustainedGroupedFirings(results []model.AlertResult, correlationLabels []string, from, to time.Time, evalInterval, mergeGap time.Duration, threshold float64) int {
	incidents := eval.GroupIncidents(results, correlationLabels)
	count := 0
	for _, inc := range incidents {
		for _, f := range eval.MergeFirings(inc.Firings, mergeGap) {
			if firingWindowRatio(f, from, to, evalInterval) >= threshold {
				count++
			}
		}
	}
	return count
}

func firingWindowRatio(f model.FiringRange, from, to time.Time, evalInterval time.Duration) float64 {
	window := to.Sub(from)
	if window <= 0 {
		return 0
	}
	start := f.FirstFired
	if start.Before(from) {
		start = from
	}
	end := f.LastFired.Add(evalInterval)
	if end.After(to) {
		end = to
	}
	if !end.After(start) {
		return 0
	}
	return float64(end.Sub(start)) / float64(window)
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
