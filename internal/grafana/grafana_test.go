package grafana

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestMetricNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/datasources/uid/prom-east/resources/api/v1/label/__name__/values" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":["up","kube_pod_info"]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	names, err := client.MetricNames("prom-east")
	if err != nil {
		t.Fatalf("MetricNames returned error: %v", err)
	}
	if want := []string{"up", "kube_pod_info"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("MetricNames = %v, want %v", names, want)
	}
}

func TestInstantQuery(t *testing.T) {
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ds/query" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body dsQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Queries) != 1 || !body.Queries[0].Instant || body.Queries[0].Range {
			t.Fatalf("unexpected query request: %+v", body)
		}
		if body.Queries[0].Expr != `count({__name__="up"})` {
			t.Fatalf("unexpected expression: %s", body.Queries[0].Expr)
		}
		_, _ = w.Write([]byte(`{"results":{"A":{"frames":[{"schema":{"fields":[{"name":"Time","type":"time"},{"name":"Value","type":"number"}],"meta":{}},"data":{"values":[[1790683200000],[42]]}}]}}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	result, err := client.InstantQuery("prom-east", `count({__name__="up"})`, at)
	if err != nil {
		t.Fatalf("InstantQuery returned error: %v", err)
	}
	if len(result.Series) != 1 || len(result.Series[0].Samples) != 1 || result.Series[0].Samples[0].Value != 42 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestInstantQueryRetriesRateLimit(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	if _, err := client.InstantQuery("prom-east", "up", time.Now()); err != nil {
		t.Fatalf("InstantQuery returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}
