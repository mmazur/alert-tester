package main

import (
	"path/filepath"
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
