package main

import (
	"fmt"
	"io"
	"time"
)

func renderGrafanaReport(w io.Writer, report *grafanaReport, verbose bool) {
	fmt.Fprintf(w, "type: grafana, source: %s, datasource: %s\n", report.Source, report.Datasource)
	fmt.Fprintf(w, "starttime: %s, endtime: %s (duration: %s)\n", report.StartTime.Format(time.RFC3339), report.EndTime.Format(time.RFC3339), trimDur(report.EndTime.Sub(report.StartTime)))
	if report.Preroll > 0 {
		fmt.Fprintf(w, "preroll: %s (querying from %s)\n", trimDur(report.Preroll), report.QueryFrom.Format(time.RFC3339))
	}
	fmt.Fprintf(w, "step: %s, eval-interval: %s, chunk-size: %s\n", trimDur(report.Step), trimDur(report.EvalInterval), trimDur(report.ChunkSize))

	queryCompletePrinted := false
	for i, run := range report.Runs {
		if i > 0 {
			fmt.Fprintln(w)
		}
		for _, fetch := range run.Fetches {
			fmt.Fprintf(w, "expr: %s\n\n", fetch.Expr)
		}
		for _, fetch := range run.Fetches {
			if !queryCompletePrinted {
				fmt.Fprintln(w, renderQueryComplete(fetch.Query))
				fmt.Fprintln(w)
				queryCompletePrinted = true
			}
			fmt.Fprintln(w, renderThreshold(fetch.Threshold))
		}
		for _, line := range run.ExtraLines {
			fmt.Fprintln(w, line)
		}

		if run.NoData {
			fmt.Fprintln(w, "  no data returned")
			fmt.Fprintln(w)
			continue
		}

		fmt.Fprintln(w, "analysis:")
		for _, analysis := range run.Analyses {
			if analysis.TotalFirings == 0 {
				fmt.Fprintf(w, "- for %s: never\n", formatDuration(analysis.ForDuration))
				continue
			}

			if len(analysis.Incidents) > 0 {
				fmt.Fprintf(w, "- for %s: %d firings, %d grouped firings (~incidents)\n",
					formatDuration(analysis.ForDuration), analysis.TotalFirings, analysis.GroupedFirings)
				printIncidents(w, analysis.Incidents, report.EvalInterval, verbose)
				continue
			}

			fmt.Fprintf(w, "- for %s: %d firings\n", formatDuration(analysis.ForDuration), analysis.TotalFirings)
			printFirings(w, analysis.Results, report.EvalInterval, verbose)
		}
	}
}

func renderQueryComplete(stats reportQueryStats) string {
	return fmt.Sprintf(
		"query complete: %d chunks (%d cached in %s, %d fetched in %s), %d series returned, %d samples total, max chunk cardinality %d",
		stats.Chunks,
		stats.CacheHits,
		trimDur(stats.CacheTime.Round(time.Millisecond)),
		stats.CacheMisses,
		trimDur(stats.FetchTime.Round(time.Millisecond)),
		stats.SeriesReturned,
		stats.SampleCount,
		stats.MaxCardinality,
	)
}

func renderThreshold(threshold reportThreshold) string {
	if threshold.Local {
		return fmt.Sprintf("local threshold %s: %d samples pass", threshold.Label, threshold.SamplesPass)
	}
	return fmt.Sprintf("%d samples pass (threshold is in the expression)", threshold.SamplesPass)
}
