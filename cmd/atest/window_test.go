package main

import (
	"testing"
	"time"
)

func TestComputePrerollAlignsToChunk(t *testing.T) {
	got := computePreroll([]time.Duration{0}, 0, time.Hour)
	if got != time.Hour {
		t.Fatalf("computePreroll = %s, want %s", got, time.Hour)
	}
}

func TestComputePrerollUsesMaxForAndDelay(t *testing.T) {
	got := computePreroll([]time.Duration{5 * time.Minute, 2 * time.Hour}, 90*time.Minute, time.Hour)
	if got != 3*time.Hour {
		t.Fatalf("computePreroll = %s, want %s", got, 3*time.Hour)
	}
}

func TestDeriveQueryFrom(t *testing.T) {
	from := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	got := deriveQueryFrom(from, time.Hour)
	want := time.Date(2026, 6, 4, 23, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("deriveQueryFrom = %s, want %s", got, want)
	}
}
