package main

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"

	"alert-tester/internal/model"
)

type fakeCardinalityClient struct {
	names  []string
	counts map[string]float64
	fails  map[string]bool
	mu     sync.Mutex
	seen   []string
}

func (f *fakeCardinalityClient) MetricNames(string) ([]string, error) {
	return f.names, nil
}

func (f *fakeCardinalityClient) InstantQuery(_ string, expr string, at time.Time) (*model.QueryResult, error) {
	f.mu.Lock()
	f.seen = append(f.seen, expr)
	f.mu.Unlock()
	for metric, count := range f.counts {
		if expr == fmt.Sprintf("count({__name__=%q})", metric) {
			if f.fails[metric] {
				return nil, fmt.Errorf("query failed")
			}
			return &model.QueryResult{Series: []model.Series{{Samples: []model.Sample{{Timestamp: at, Value: count}}}}}, nil
		}
	}
	return &model.QueryResult{}, nil
}

func TestCollectCardinalitySortsFiltersAndCountsFailures(t *testing.T) {
	client := &fakeCardinalityClient{
		names:  []string{"up", "kube_pod_info", "kube_node_info", "old_metric"},
		counts: map[string]float64{"up": 3, "kube_pod_info": 12000, "kube_node_info": 40, "old_metric": 0},
		fails:  map[string]bool{"kube_node_info": true},
	}

	var progressCalls [][3]int
	var progressErrors []error
	results, failures, err := collectCardinality(client, "prom-east", time.Now(), regexp.MustCompile(`^(up|kube_)`), 1, 2, func(done, total, failures int, queryErr error) {
		progressCalls = append(progressCalls, [3]int{done, total, failures})
		if queryErr != nil {
			progressErrors = append(progressErrors, queryErr)
		}
	})
	if err != nil {
		t.Fatalf("collectCardinality returned error: %v", err)
	}
	want := []metricCardinality{{Metric: "kube_pod_info", Series: 12000}, {Metric: "up", Series: 3}}
	if !reflect.DeepEqual(results, want) {
		t.Fatalf("results = %+v, want %+v", results, want)
	}
	if failures != 1 {
		t.Fatalf("failures = %d, want 1", failures)
	}
	if got := progressCalls[len(progressCalls)-1]; got != [3]int{3, 3, 1} {
		t.Fatalf("final progress = %v, want [3 3 1]", got)
	}
	if got, want := progressErrors[0].Error(), "kube_node_info: query failed"; got != want {
		t.Fatalf("progress error = %q, want %q", got, want)
	}
}

func TestWriteCardinalityCSV(t *testing.T) {
	var out bytes.Buffer
	err := writeCardinalityCSV(&out, []metricCardinality{{Metric: "kube_pod_info", Series: 12000}})
	if err != nil {
		t.Fatalf("writeCardinalityCSV returned error: %v", err)
	}
	if got, want := out.String(), "series,metric\n12000,kube_pod_info\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
