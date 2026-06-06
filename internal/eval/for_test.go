package eval

import (
	"math"
	"testing"
	"time"

	"alert-tester/internal/model"
)

func TestEvaluateForBoundary(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	series := []model.Series{{
		Labels: map[string]string{"x": "1"},
		Samples: []model.Sample{
			{Timestamp: t0, Value: 1},
			{Timestamp: t0.Add(time.Minute), Value: 1},
			{Timestamp: t0.Add(2 * time.Minute), Value: 1},
		},
	}}

	got := EvaluateFor(series, 2*time.Minute, time.Minute)
	if len(got) != 1 || len(got[0].Firings) != 1 {
		t.Fatalf("EvaluateFor returned %v, want 1 firing", got)
	}
	if got[0].Firings[0].FirstFired != t0.Add(2*time.Minute) {
		t.Fatalf("FirstFired = %s, want %s", got[0].Firings[0].FirstFired, t0.Add(2*time.Minute))
	}

	got = EvaluateFor(series[:], 3*time.Minute, time.Minute)
	if len(got) != 1 || len(got[0].Firings) != 0 {
		t.Fatalf("EvaluateFor with one tick short returned %v, want no firings", got)
	}
}

func TestEvaluateForResetsOnNaN(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	series := []model.Series{{
		Labels: map[string]string{"x": "1"},
		Samples: []model.Sample{
			{Timestamp: t0, Value: 1},
			{Timestamp: t0.Add(time.Minute), Value: math.NaN()},
			{Timestamp: t0.Add(2 * time.Minute), Value: 1},
			{Timestamp: t0.Add(3 * time.Minute), Value: 1},
		},
	}}

	got := EvaluateFor(series, time.Minute, time.Minute)
	if len(got) != 1 || len(got[0].Firings) != 1 {
		t.Fatalf("EvaluateFor returned %v, want exactly 1 firing after reset", got)
	}
	if got[0].Firings[0].FirstPending != t0.Add(2*time.Minute) {
		t.Fatalf("FirstPending = %s, want %s", got[0].Firings[0].FirstPending, t0.Add(2*time.Minute))
	}
}

func TestEvaluateForMultipleFirings(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	series := []model.Series{{
		Labels: map[string]string{"x": "1"},
		Samples: []model.Sample{
			{Timestamp: t0, Value: 1},
			{Timestamp: t0.Add(time.Minute), Value: 1},
			{Timestamp: t0.Add(2 * time.Minute), Value: math.NaN()},
			{Timestamp: t0.Add(3 * time.Minute), Value: 1},
			{Timestamp: t0.Add(4 * time.Minute), Value: 1},
		},
	}}

	got := EvaluateFor(series, time.Minute, time.Minute)
	if len(got) != 1 || len(got[0].Firings) != 2 {
		t.Fatalf("EvaluateFor returned %v, want 2 firings", got)
	}
}

func TestEvaluateForTreatsZeroValuedSamplesAsPresent(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	series := []model.Series{{
		Labels: map[string]string{"x": "1"},
		Samples: []model.Sample{
			{Timestamp: t0, Value: 0},
			{Timestamp: t0.Add(time.Minute), Value: 0},
			{Timestamp: t0.Add(2 * time.Minute), Value: 0},
		},
	}}

	got := EvaluateFor(series, 2*time.Minute, time.Minute)
	if len(got) != 1 || len(got[0].Firings) != 1 {
		t.Fatalf("EvaluateFor returned %v, want 1 firing for present zero-valued samples", got)
	}
	if got[0].Firings[0].FirstPending != t0 {
		t.Fatalf("FirstPending = %s, want %s", got[0].Firings[0].FirstPending, t0)
	}
	if got[0].Firings[0].FirstFired != t0.Add(2*time.Minute) {
		t.Fatalf("FirstFired = %s, want %s", got[0].Firings[0].FirstFired, t0.Add(2*time.Minute))
	}
}
