package query

import (
	"testing"
	"time"

	"alert-tester/internal/model"
)

func TestSplitChunks(t *testing.T) {
	e := &Engine{ChunkSize: time.Hour}
	from := time.Date(2025, 5, 1, 1, 15, 0, 0, time.UTC)
	to := time.Date(2025, 5, 1, 3, 10, 0, 0, time.UTC)

	got := e.splitChunks(from, to)
	if len(got) != 3 {
		t.Fatalf("splitChunks returned %v, want 3 chunks", got)
	}
	if got[0].from != from || got[0].to != time.Date(2025, 5, 1, 2, 0, 0, 0, time.UTC) {
		t.Fatalf("first chunk = %+v, want [from, 02:00)", got[0])
	}
	if got[2].from != time.Date(2025, 5, 1, 3, 0, 0, 0, time.UTC) || got[2].to != to {
		t.Fatalf("last chunk = %+v, want [03:00, to)", got[2])
	}
}

func TestMergeSeries(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	m := map[string]*model.Series{}

	mergeSeries(m, []model.Series{{
		Labels:  map[string]string{"x": "1"},
		Samples: []model.Sample{{Timestamp: t0, Value: 1}},
	}})
	mergeSeries(m, []model.Series{{
		Labels:  map[string]string{"x": "1"},
		Samples: []model.Sample{{Timestamp: t0.Add(time.Minute), Value: 2}},
	}})

	if len(m) != 1 {
		t.Fatalf("mergeSeries map size = %d, want 1", len(m))
	}
	series := m[seriesKey(map[string]string{"x": "1"})]
	if series == nil || len(series.Samples) != 2 {
		t.Fatalf("merged series = %+v, want 2 samples", series)
	}
}
