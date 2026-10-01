package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"alert-tester/internal/grafana"
	"alert-tester/internal/model"
)

type cardinalityFlags struct {
	grafanaURL  string
	datasource  string
	bearerToken string
	metricRegex string
	format      string
	minSeries   int64
	limit       int
	concurrency int
	noProgress  bool
}

type cardinalityClient interface {
	MetricNames(datasourceUID string) ([]string, error)
	InstantQuery(datasourceUID, expr string, at time.Time) (*model.QueryResult, error)
}

type metricCardinality struct {
	Metric string
	Series int64
}

type cardinalityOutcome struct {
	result metricCardinality
	err    error
}

type cardinalityProgress func(done, total, failures int, queryErr error)

func newCardinalityCmd() *cobra.Command {
	f := &cardinalityFlags{}
	cmd := &cobra.Command{
		Use:   "cardinality",
		Short: "Rank Prometheus metrics by current series count",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCardinality(f, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&f.grafanaURL, "grafana-url", "", "Grafana base URL (required)")
	cmd.Flags().StringVar(&f.datasource, "datasource", "", "Prometheus datasource UID (required)")
	cmd.Flags().StringVar(&f.bearerToken, "bearer-token", "", "Bearer token (or ATEST_GRAFANA_BEARER_TOKEN env var)")
	cmd.Flags().StringVar(&f.metricRegex, "metric-regex", "", "Only include metric names matching this regular expression")
	cmd.Flags().StringVar(&f.format, "format", "table", "Output format: table or csv")
	cmd.Flags().Int64Var(&f.minSeries, "min-series", 1, "Only include metrics with at least this many current series")
	cmd.Flags().IntVar(&f.limit, "limit", 50, "Maximum rows to return after sorting (0 for all)")
	cmd.Flags().IntVar(&f.concurrency, "concurrency", 1, "Maximum concurrent Grafana queries")
	cmd.Flags().BoolVar(&f.noProgress, "no-progress", false, "Suppress progress messages")
	for _, name := range []string{"grafana-url", "datasource"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func runCardinality(f *cardinalityFlags, out, errOut io.Writer) error {
	if f.concurrency < 1 {
		return fmt.Errorf("--concurrency must be at least 1")
	}
	if f.limit < 0 {
		return fmt.Errorf("--limit cannot be negative")
	}
	if f.minSeries < 0 {
		return fmt.Errorf("--min-series cannot be negative")
	}
	if f.format != "table" && f.format != "csv" {
		return fmt.Errorf("--format must be table or csv")
	}

	var filter *regexp.Regexp
	if f.metricRegex != "" {
		var err error
		filter, err = regexp.Compile(f.metricRegex)
		if err != nil {
			return fmt.Errorf("invalid --metric-regex: %w", err)
		}
	}

	token := f.bearerToken
	if token == "" {
		token = os.Getenv("ATEST_GRAFANA_BEARER_TOKEN")
	}
	client := grafana.NewClient(f.grafanaURL, token)
	started := time.Now()
	if !f.noProgress {
		fmt.Fprintln(errOut, "discovering metric names...")
	}
	progress := cardinalityProgress(nil)
	if !f.noProgress {
		progress = func(done, total, failures int, queryErr error) {
			if done == 0 {
				fmt.Fprintf(errOut, "querying %d metrics with concurrency %d...\n", total, f.concurrency)
				return
			}
			if queryErr != nil {
				fmt.Fprintf(errOut, "query failed: %v\n", queryErr)
			}
			if done%25 == 0 || done == total {
				fmt.Fprintf(errOut, "progress: %d/%d metrics queried (%d failed)\n", done, total, failures)
			}
		}
	}
	results, failures, err := collectCardinality(client, f.datasource, time.Now(), filter, f.minSeries, f.concurrency, progress)
	if err != nil {
		return err
	}
	matched := len(results)
	if f.limit > 0 && len(results) > f.limit {
		results = results[:f.limit]
	}
	if !f.noProgress {
		fmt.Fprintf(errOut, "completed in %s: %d metrics matched --min-series, %d rows written\n", time.Since(started).Round(time.Second), matched, len(results))
	}
	if failures > 0 {
		fmt.Fprintf(errOut, "warning: %d metric queries failed; showing successful results\n", failures)
	}

	if f.format == "csv" {
		return writeCardinalityCSV(out, results)
	}
	return writeCardinalityTable(out, results)
}

func collectCardinality(client cardinalityClient, datasource string, at time.Time, filter *regexp.Regexp, minSeries int64, concurrency int, progress cardinalityProgress) ([]metricCardinality, int, error) {
	names, err := client.MetricNames(datasource)
	if err != nil {
		return nil, 0, fmt.Errorf("listing metric names: %w", err)
	}
	filteredNames := make([]string, 0, len(names))
	for _, metric := range names {
		if filter == nil || filter.MatchString(metric) {
			filteredNames = append(filteredNames, metric)
		}
	}
	if progress != nil {
		progress(0, len(filteredNames), 0, nil)
	}

	jobs := make(chan string)
	outcomes := make(chan cardinalityOutcome)
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for metric := range jobs {
				expr := fmt.Sprintf("count({__name__=%s})", strconv.Quote(metric))
				queryResult, err := client.InstantQuery(datasource, expr, at)
				if err != nil {
					outcomes <- cardinalityOutcome{err: fmt.Errorf("%s: %w", metric, err)}
					continue
				}
				outcomes <- cardinalityOutcome{result: metricCardinality{Metric: metric, Series: scalarValue(queryResult)}}
			}
		}()
	}

	go func() {
		for _, metric := range filteredNames {
			jobs <- metric
		}
		close(jobs)
		workers.Wait()
		close(outcomes)
	}()

	var results []metricCardinality
	failures := 0
	done := 0
	for outcome := range outcomes {
		done++
		if outcome.err != nil {
			failures++
		} else if outcome.result.Series >= minSeries {
			results = append(results, outcome.result)
		}
		if progress != nil {
			progress(done, len(filteredNames), failures, outcome.err)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Series == results[j].Series {
			return results[i].Metric < results[j].Metric
		}
		return results[i].Series > results[j].Series
	})
	return results, failures, nil
}

func scalarValue(result *model.QueryResult) int64 {
	if result == nil || len(result.Series) == 0 || len(result.Series[0].Samples) == 0 {
		return 0
	}
	samples := result.Series[0].Samples
	return int64(samples[len(samples)-1].Value)
}

func writeCardinalityTable(out io.Writer, results []metricCardinality) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "SERIES\tMETRIC"); err != nil {
		return err
	}
	for _, result := range results {
		if _, err := fmt.Fprintf(w, "%d\t%s\n", result.Series, result.Metric); err != nil {
			return err
		}
	}
	return w.Flush()
}

func writeCardinalityCSV(out io.Writer, results []metricCardinality) error {
	w := csv.NewWriter(out)
	if err := w.Write([]string{"series", "metric"}); err != nil {
		return err
	}
	for _, result := range results {
		if err := w.Write([]string{strconv.FormatInt(result.Series, 10), result.Metric}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}
