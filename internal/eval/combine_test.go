package eval

import (
	"testing"
	"time"

	"alert-tester/internal/model"
)

func TestCombineAnd(t *testing.T) {
	t0 := time.Unix(0, 0)
	a := []model.Series{{
		Labels:  map[string]string{"x": "1"},
		Samples: []model.Sample{{Timestamp: t0, Value: 1}, {Timestamp: t0.Add(time.Minute), Value: 1}},
	}}
	b := []model.Series{{
		Labels:  map[string]string{"x": "1"},
		Samples: []model.Sample{{Timestamp: t0, Value: 1}},
	}}
	out := Combine(a, b, OpAnd)
	if len(out) != 1 || len(out[0].Samples) != 1 {
		t.Fatalf("AND: expected 1 series with 1 sample, got %v", out)
	}
}

func TestCombineOr(t *testing.T) {
	t0 := time.Unix(0, 0)
	a := []model.Series{{
		Labels:  map[string]string{"x": "1"},
		Samples: []model.Sample{{Timestamp: t0, Value: 1}},
	}}
	b := []model.Series{{
		Labels:  map[string]string{"x": "2"},
		Samples: []model.Sample{{Timestamp: t0, Value: 1}},
	}}
	out := Combine(a, b, OpOr)
	if len(out) != 2 {
		t.Fatalf("OR: expected 2 series, got %d", len(out))
	}
}

func TestCombineAndDifferentLabelSets(t *testing.T) {
	t0 := time.Unix(0, 0)
	a := []model.Series{{
		Labels:  map[string]string{"x": "1"},
		Samples: []model.Sample{{Timestamp: t0, Value: 1}},
	}}
	b := []model.Series{{
		Labels:  map[string]string{"x": "2"},
		Samples: []model.Sample{{Timestamp: t0, Value: 1}},
	}}
	out := Combine(a, b, OpAnd)
	if len(out) != 0 {
		t.Fatalf("AND on disjoint labels: expected empty, got %v", out)
	}
}

func TestCombinePrecedenceAndBindsTighter(t *testing.T) {
	// Verify that with manual nesting (A and B) or C and (A and (B or C))
	// produce different results, and that our Combine helper matches the
	// (A and B) or C interpretation when callers fold left-to-right.
	t0 := time.Unix(0, 0)
	mk := func(label string, ts ...int) []model.Series {
		samples := make([]model.Sample, len(ts))
		for i, s := range ts {
			samples[i] = model.Sample{Timestamp: t0.Add(time.Duration(s) * time.Minute), Value: 1}
		}
		return []model.Series{{Labels: map[string]string{"x": label}, Samples: samples}}
	}
	// A at t=0, B at t=0, C at t=0,1 — all share label x=k
	a := mk("k", 0)
	b := mk("k", 0)
	c := mk("k", 0, 1)

	// (A and B) or C = at t=0 (A∧B present) plus t=1 (C only) = 2 samples
	ab := Combine(a, b, OpAnd)
	abOrC := Combine(ab, c, OpOr)
	if len(abOrC) != 1 || len(abOrC[0].Samples) != 2 {
		t.Fatalf("(A and B) or C: expected 1 series with 2 samples, got %v", abOrC)
	}

	// A and (B or C) = (B or C) is t=0,1; AND with A (only t=0) = 1 sample
	bOrC := Combine(b, c, OpOr)
	aAndBC := Combine(a, bOrC, OpAnd)
	if len(aAndBC) != 1 || len(aAndBC[0].Samples) != 1 {
		t.Fatalf("A and (B or C): expected 1 series with 1 sample, got %v", aAndBC)
	}
}
