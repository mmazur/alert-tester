package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var errReplayDiff = errors.New("replay comparison found differences")

func newReplayCompareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compare <old> <new>",
		Short: "Compare two replay captures",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReplayCompare(args[0], args[1])
		},
	}
}

func runReplayCompare(oldDir, newDir string) error {
	oldDir, err := resolveReplayDir(oldDir)
	if err != nil {
		return err
	}
	newDir, err = resolveReplayDir(newDir)
	if err != nil {
		return err
	}

	oldManifest, err := readReplayManifest(oldDir)
	if err != nil {
		return err
	}
	newManifest, err := readReplayManifest(newDir)
	if err != nil {
		return err
	}
	if oldManifest.SchemaVersion != newManifest.SchemaVersion {
		return fmt.Errorf("replay schema mismatch: %d vs %d", oldManifest.SchemaVersion, newManifest.SchemaVersion)
	}
	if !reflect.DeepEqual(oldManifest.Config, newManifest.Config) {
		return fmt.Errorf("replay config mismatch:\n  old: %s\n  new: %s", formatReplayConfig(oldManifest.Config), formatReplayConfig(newManifest.Config))
	}

	oldCases, err := readReplayCases(oldDir)
	if err != nil {
		return err
	}
	newCases, err := readReplayCases(newDir)
	if err != nil {
		return err
	}

	var added, removed, changed []string
	var common []string
	for id := range oldCases {
		if _, ok := newCases[id]; !ok {
			removed = append(removed, id)
			continue
		}
		common = append(common, id)
	}
	for id := range newCases {
		if _, ok := oldCases[id]; !ok {
			added = append(added, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(common)

	for _, id := range common {
		if !replaySummaryEqual(oldCases[id], newCases[id]) {
			changed = append(changed, id)
		}
	}

	if len(added) == 0 && len(removed) == 0 && len(changed) == 0 {
		fmt.Printf("replays match: %d cases compared\n", len(common))
		return nil
	}

	fmt.Printf("replays differ: %d changed, %d added, %d removed\n", len(changed), len(added), len(removed))
	for _, id := range added {
		printReplayCaseHeader("added", newCases[id])
	}
	for _, id := range removed {
		printReplayCaseHeader("removed", oldCases[id])
	}
	for _, id := range changed {
		printReplayCaseDiff(oldDir, newDir, oldCases[id], newCases[id])
	}

	return errReplayDiff
}

func resolveReplayDir(dir string) (string, error) {
	if filepath.IsAbs(dir) || strings.Contains(dir, string(os.PathSeparator)) {
		return dir, nil
	}
	root, err := replayProjectRoot()
	if err != nil {
		return "", fmt.Errorf("resolve replay %q: %w", dir, err)
	}
	return filepath.Join(root, ".local", "replays", dir), nil
}

func readReplayManifest(dir string) (replayManifest, error) {
	var manifest replayManifest
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func readReplayCases(dir string) (map[string]replayCaseSummary, error) {
	f, err := os.Open(filepath.Join(dir, "cases.jsonl.gz"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	cases := make(map[string]replayCaseSummary)
	scanner := bufio.NewScanner(gr)
	for scanner.Scan() {
		var summary replayCaseSummary
		if err := json.Unmarshal(scanner.Bytes(), &summary); err != nil {
			return nil, err
		}
		cases[summary.ID] = summary
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return cases, nil
}

func readReplayDetail(dir, relPath string) (replayCaseDetail, error) {
	var detail replayCaseDetail
	f, err := os.Open(filepath.Join(dir, relPath))
	if err != nil {
		return detail, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return detail, err
	}
	defer gr.Close()
	if err := json.NewDecoder(gr).Decode(&detail); err != nil {
		return detail, err
	}
	return detail, nil
}

func formatReplayConfig(cfg replayConfigJSON) string {
	return fmt.Sprintf("for=%s eval-interval=%s delay-resolution-by=%s incident-group-by=%q chunk-size=%s",
		strings.Join(cfg.For, ","), cfg.EvalInterval, cfg.DelayResolutionBy, cfg.IncidentGroupBy, cfg.ChunkSize)
}

func replaySummaryEqual(oldSummary, newSummary replayCaseSummary) bool {
	oldSummary.DetailPath = ""
	newSummary.DetailPath = ""
	oldSummary.DetailSHA256 = ""
	newSummary.DetailSHA256 = ""
	return reflect.DeepEqual(oldSummary, newSummary)
}

func printReplayCaseHeader(kind string, summary replayCaseSummary) {
	fmt.Printf("\n[%s] %s\n", kind, summary.ID)
	fmt.Printf("  source: %s\n", summary.Source)
	fmt.Printf("  datasource: %s\n", summary.Datasource)
	fmt.Printf("  expr: %s\n", summary.Expr)
	fmt.Printf("  step: %s\n", summary.Step)
	fmt.Printf("  query window: %s -> %s\n", summary.QueryFrom, summary.QueryTo)
	fmt.Printf("  analysis window: %s -> %s\n", summary.AnalysisFrom, summary.AnalysisTo)
	if summary.Status != "" {
		fmt.Printf("  status: %s\n", summary.Status)
	}
	if summary.Reason != "" {
		fmt.Printf("  reason: %s\n", summary.Reason)
	}
}

func printReplayCaseDiff(oldDir, newDir string, oldSummary, newSummary replayCaseSummary) {
	fmt.Printf("\n[changed] %s\n", oldSummary.ID)
	fmt.Printf("  source: %s\n", newSummary.Source)
	fmt.Printf("  datasource: %s\n", newSummary.Datasource)
	fmt.Printf("  expr: %s\n", newSummary.Expr)
	fmt.Printf("  step: %s -> %s\n", oldSummary.Step, newSummary.Step)
	fmt.Printf("  query window: %s -> %s | %s -> %s\n", oldSummary.QueryFrom, oldSummary.QueryTo, newSummary.QueryFrom, newSummary.QueryTo)
	fmt.Printf("  analysis window: %s -> %s | %s -> %s\n", oldSummary.AnalysisFrom, oldSummary.AnalysisTo, newSummary.AnalysisFrom, newSummary.AnalysisTo)
	if oldSummary.Status != newSummary.Status {
		fmt.Printf("  status: %s -> %s\n", oldSummary.Status, newSummary.Status)
	}
	if oldSummary.Reason != newSummary.Reason {
		fmt.Printf("  reason: %q -> %q\n", oldSummary.Reason, newSummary.Reason)
	}
	printReplayQueryDiff(oldSummary.Query, newSummary.Query)
	printReplayAnalysisSummaryDiff(oldSummary.Analyses, newSummary.Analyses)

	if oldSummary.DetailPath == "" || newSummary.DetailPath == "" {
		return
	}
	oldDetail, err := readReplayDetail(oldDir, oldSummary.DetailPath)
	if err != nil {
		fmt.Printf("  detail read error (old): %v\n", err)
		return
	}
	newDetail, err := readReplayDetail(newDir, newSummary.DetailPath)
	if err != nil {
		fmt.Printf("  detail read error (new): %v\n", err)
		return
	}
	printReplayDetailDiff(oldDetail, newDetail)
	fmt.Printf("  detail files:\n")
	fmt.Printf("    old: %s\n", filepath.Join(oldDir, oldSummary.DetailPath))
	fmt.Printf("    new: %s\n", filepath.Join(newDir, newSummary.DetailPath))
}

func printReplayQueryDiff(oldQuery, newQuery replayQuerySummary) {
	if reflect.DeepEqual(oldQuery, newQuery) {
		return
	}
	fmt.Printf("  query stats:\n")
	fmt.Printf("    chunks: %d -> %d\n", oldQuery.Chunks, newQuery.Chunks)
	fmt.Printf("    cache hits: %d -> %d\n", oldQuery.CacheHits, newQuery.CacheHits)
	fmt.Printf("    cache misses: %d -> %d\n", oldQuery.CacheMisses, newQuery.CacheMisses)
	fmt.Printf("    series returned: %d -> %d\n", oldQuery.SeriesReturned, newQuery.SeriesReturned)
	fmt.Printf("    sample count: %d -> %d\n", oldQuery.SampleCount, newQuery.SampleCount)
	fmt.Printf("    max chunk cardinality: %d -> %d\n", oldQuery.MaxCardinality, newQuery.MaxCardinality)
}

func printReplayAnalysisSummaryDiff(oldAnalyses, newAnalyses []replayAnalysisSummary) {
	oldByFor := make(map[string]replayAnalysisSummary, len(oldAnalyses))
	newByFor := make(map[string]replayAnalysisSummary, len(newAnalyses))
	keys := make(map[string]struct{}, len(oldAnalyses)+len(newAnalyses))
	for _, analysis := range oldAnalyses {
		oldByFor[analysis.For] = analysis
		keys[analysis.For] = struct{}{}
	}
	for _, analysis := range newAnalyses {
		newByFor[analysis.For] = analysis
		keys[analysis.For] = struct{}{}
	}

	var ordered []string
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		oldAnalysis, oldOK := oldByFor[key]
		newAnalysis, newOK := newByFor[key]
		if !oldOK {
			fmt.Printf("  analysis %s added: firings=%d grouped=%d incidents=%d\n", key, newAnalysis.TotalFirings, newAnalysis.GroupedFirings, newAnalysis.IncidentCount)
			continue
		}
		if !newOK {
			fmt.Printf("  analysis %s removed: firings=%d grouped=%d incidents=%d\n", key, oldAnalysis.TotalFirings, oldAnalysis.GroupedFirings, oldAnalysis.IncidentCount)
			continue
		}
		if reflect.DeepEqual(oldAnalysis, newAnalysis) {
			continue
		}
		fmt.Printf("  analysis %s: firings %d -> %d, grouped %d -> %d, incidents %d -> %d\n",
			key,
			oldAnalysis.TotalFirings, newAnalysis.TotalFirings,
			oldAnalysis.GroupedFirings, newAnalysis.GroupedFirings,
			oldAnalysis.IncidentCount, newAnalysis.IncidentCount,
		)
	}
}

func printReplayDetailDiff(oldDetail, newDetail replayCaseDetail) {
	oldByFor := make(map[string]replayAnalysisDetail, len(oldDetail.Analyses))
	newByFor := make(map[string]replayAnalysisDetail, len(newDetail.Analyses))
	keys := make(map[string]struct{}, len(oldDetail.Analyses)+len(newDetail.Analyses))
	for _, analysis := range oldDetail.Analyses {
		oldByFor[analysis.For] = analysis
		keys[analysis.For] = struct{}{}
	}
	for _, analysis := range newDetail.Analyses {
		newByFor[analysis.For] = analysis
		keys[analysis.For] = struct{}{}
	}

	var ordered []string
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		oldAnalysis, oldOK := oldByFor[key]
		newAnalysis, newOK := newByFor[key]
		if !oldOK || !newOK {
			continue
		}
		printReplayResultDiff(key, oldAnalysis.Results, newAnalysis.Results)
		printReplayIncidentDiff(key, oldAnalysis.Incidents, newAnalysis.Incidents)
	}
}

func printReplayResultDiff(forValue string, oldResults, newResults []replayAlertResult) {
	oldByLabel := make(map[string]replayAlertResult, len(oldResults))
	newByLabel := make(map[string]replayAlertResult, len(newResults))
	keys := make(map[string]struct{}, len(oldResults)+len(newResults))
	for _, result := range oldResults {
		key := formatLabels(result.LabelSet)
		oldByLabel[key] = result
		keys[key] = struct{}{}
	}
	for _, result := range newResults {
		key := formatLabels(result.LabelSet)
		newByLabel[key] = result
		keys[key] = struct{}{}
	}

	var ordered []string
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	printed := 0
	for _, key := range ordered {
		oldResult, oldOK := oldByLabel[key]
		newResult, newOK := newByLabel[key]
		if oldOK && newOK && reflect.DeepEqual(oldResult, newResult) {
			continue
		}
		if printed == 0 {
			fmt.Printf("    label-set changes for %s:\n", forValue)
		}
		fmt.Printf("      %s: firings %d -> %d\n", key, len(oldResult.Firings), len(newResult.Firings))
		printed++
		if printed == 5 {
			remaining := len(ordered) - printed
			if remaining > 0 {
				fmt.Printf("      ... %d more label-set changes\n", remaining)
			}
			return
		}
	}
}

func printReplayIncidentDiff(forValue string, oldIncidents, newIncidents []replayIncidentDetail) {
	oldByKey := make(map[string]replayIncidentDetail, len(oldIncidents))
	newByKey := make(map[string]replayIncidentDetail, len(newIncidents))
	keys := make(map[string]struct{}, len(oldIncidents)+len(newIncidents))
	for _, incident := range oldIncidents {
		oldByKey[incident.CorrelationKey] = incident
		keys[incident.CorrelationKey] = struct{}{}
	}
	for _, incident := range newIncidents {
		newByKey[incident.CorrelationKey] = incident
		keys[incident.CorrelationKey] = struct{}{}
	}

	var ordered []string
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	printed := 0
	for _, key := range ordered {
		oldIncident, oldOK := oldByKey[key]
		newIncident, newOK := newByKey[key]
		if oldOK && newOK && reflect.DeepEqual(oldIncident, newIncident) {
			continue
		}
		if printed == 0 {
			fmt.Printf("    incident changes for %s:\n", forValue)
		}
		fmt.Printf("      %s: firings %d -> %d\n", key, len(oldIncident.Firings), len(newIncident.Firings))
		printed++
		if printed == 5 {
			remaining := len(ordered) - printed
			if remaining > 0 {
				fmt.Printf("      ... %d more incident changes\n", remaining)
			}
			return
		}
	}
}
