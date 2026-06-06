package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveReplayDir(t *testing.T) {
	orig := replayProjectRoot
	replayProjectRoot = func() (string, error) {
		return "/repo", nil
	}
	t.Cleanup(func() {
		replayProjectRoot = orig
	})

	t.Run("bare replay id resolves under local replays", func(t *testing.T) {
		got, err := resolveReplayDir("20260605220842-31d437adeb6f-clean")
		if err != nil {
			t.Fatalf("resolveReplayDir returned error: %v", err)
		}
		want := filepath.Join("/repo", ".local", "replays", "20260605220842-31d437adeb6f-clean")
		if got != want {
			t.Fatalf("resolveReplayDir = %q, want %q", got, want)
		}
	})

	t.Run("explicit relative path is preserved", func(t *testing.T) {
		got, err := resolveReplayDir(".local/replays/run-a")
		if err != nil {
			t.Fatalf("resolveReplayDir returned error: %v", err)
		}
		if got != ".local/replays/run-a" {
			t.Fatalf("resolveReplayDir = %q, want %q", got, ".local/replays/run-a")
		}
	})

	t.Run("absolute path is preserved", func(t *testing.T) {
		got, err := resolveReplayDir("/tmp/run-a")
		if err != nil {
			t.Fatalf("resolveReplayDir returned error: %v", err)
		}
		if got != "/tmp/run-a" {
			t.Fatalf("resolveReplayDir = %q, want %q", got, "/tmp/run-a")
		}
	})
}

func TestPrintReplayDiffSummary(t *testing.T) {
	oldCases := map[string]replayCaseSummary{
		"removed-a": {Datasource: "example-alpha"},
	}
	newCases := map[string]replayCaseSummary{
		"changed-a": {Datasource: "example-alpha"},
		"changed-b": {Datasource: "example-beta"},
		"added-a":   {Datasource: "example-beta"},
	}

	var got bytes.Buffer
	printReplayDiffSummary(&got, oldCases, newCases, []string{"added-a"}, []string{"removed-a"}, []string{"changed-b", "changed-a"})

	out := got.String()
	for _, want := range []string{
		"replays differ: 2 result-changed, 1 added, 1 removed",
		"result-changed by datasource:\n  example-alpha: 1\n  example-beta: 1",
		"added by datasource:\n  example-beta: 1",
		"removed by datasource:\n  example-alpha: 1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintReplayCaseDiffToShowsIdentityOnly(t *testing.T) {
	var got bytes.Buffer
	oldSummary := replayCaseSummary{
		ID:           "old-case",
		Source:       "https://grafana.example.com",
		Datasource:   "example-datasource",
		Expr:         "metric == 0",
		Step:         "30s",
		QueryFrom:    "2026-05-14T23:00:00Z",
		QueryTo:      "2026-05-16T00:00:00Z",
		AnalysisFrom: "2026-05-15T01:00:00Z",
		AnalysisTo:   "2026-05-16T00:00:00Z",
		Status:       "ok",
		Query: replayQuerySummary{
			SampleCount: 1,
		},
		Analyses: []replayAnalysisSummary{{
			For:          "0m",
			TotalFirings: 1,
		}},
	}
	newSummary := oldSummary
	newSummary.ID = "new-case"
	newSummary.Query.SampleCount = 99
	newSummary.Analyses[0].TotalFirings = 42

	printReplayCaseDiffTo(&got, oldSummary, newSummary)

	out := got.String()
	for _, want := range []string{
		"[result-changed] old-case",
		"datasource: example-datasource",
		"expr: metric == 0",
		"case id: old-case -> new-case",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("changed output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"query stats:",
		"analysis 0m:",
		"label-set changes",
		"detail files:",
	} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("changed output unexpectedly contains %q:\n%s", unwanted, out)
		}
	}
}
