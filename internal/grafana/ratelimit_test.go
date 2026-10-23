package grafana

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func rateLimitedServer(t *testing.T, failures int32, retryAfter string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= failures {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(rangeResponseTwoSeries))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestRangeQueryRetriesRateLimitThenSucceeds(t *testing.T) {
	server, calls := rateLimitedServer(t, 3, "0")
	from := time.UnixMilli(1746061200000).UTC()

	result, step, err := NewClient(server.URL, "").RangeQuery("prom", "up", from, from.Add(3*time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("RangeQuery: %v", err)
	}
	if *calls != 4 {
		t.Fatalf("calls = %d, want 4", *calls)
	}
	if step != time.Minute || len(result.Series) != 2 {
		t.Fatalf("result after retry: step=%s series=%d", step, len(result.Series))
	}
}

func TestRangeQueryGivesUpAfterFourRateLimitedAttempts(t *testing.T) {
	server, calls := rateLimitedServer(t, 100, "0")
	from := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)

	_, _, err := NewClient(server.URL, "").RangeQuery("prom", "up", from, from.Add(time.Hour), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "grafana returned 429: slow down") {
		t.Fatalf("err = %v, want 429 error", err)
	}
	if *calls != 4 {
		t.Fatalf("calls = %d, want 4", *calls)
	}
}

func TestRangeQueryRetrySendsIdenticalRequest(t *testing.T) {
	var bodies []string
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, string(b))
		auths = append(auths, r.Header.Get("Authorization"))
		if len(bodies) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
	}))
	defer server.Close()

	from := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := NewClient(server.URL, "tok").RangeQuery("prom", "up", from, from.Add(time.Hour), time.Minute); err != nil {
		t.Fatalf("RangeQuery: %v", err)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("retry body differs or is empty: %q", bodies)
	}
	if auths[0] != "Bearer tok" || auths[1] != "Bearer tok" {
		t.Fatalf("auth headers = %q", auths)
	}
}

func TestRateLimitDelay(t *testing.T) {
	cases := []struct {
		name       string
		retryAfter string
		attempt    int
		want       time.Duration
	}{
		{"seconds", "7", 0, 7 * time.Second},
		{"seconds with spaces", " 3 ", 2, 3 * time.Second},
		{"zero", "0", 1, 0},
		{"past http date", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), 0, 0},
		{"empty attempt 0", "", 0, time.Second},
		{"empty attempt 1", "", 1, 2 * time.Second},
		{"empty attempt 2", "", 2, 4 * time.Second},
		{"garbage", "soon", 0, time.Second},
		{"negative falls back", "-5", 1, 2 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateLimitDelay(tc.retryAfter, tc.attempt); got != tc.want {
				t.Fatalf("rateLimitDelay(%q, %d) = %s, want %s", tc.retryAfter, tc.attempt, got, tc.want)
			}
		})
	}

	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	if got := rateLimitDelay(future, 0); got <= 25*time.Second || got > 31*time.Second {
		t.Fatalf("future http date delay = %s, want ~30s", got)
	}
}

func TestInstantQueryRequestShape(t *testing.T) {
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		q := got["queries"].([]any)[0].(map[string]any)
		ms := "1790683200000"
		if got["from"] != ms || got["to"] != ms {
			t.Errorf("from/to = %v/%v, want %s", got["from"], got["to"], ms)
		}
		if q["instant"] != true || q["maxDataPoints"] != float64(1) {
			t.Errorf("query = %v, want instant=true maxDataPoints=1", q)
		}
		if _, ok := q["range"]; ok {
			t.Errorf("range key should be omitted for instant query: %v", q)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
	}))
	defer server.Close()

	result, err := NewClient(server.URL, "tok").InstantQuery("prom", "up", at)
	if err != nil || result == nil || len(result.Series) != 0 {
		t.Fatalf("InstantQuery = %+v, %v", result, err)
	}
}

func TestInstantQueryErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":{"A":{"error":"bad expr"}}}`))
	}))
	defer server.Close()

	if _, err := NewClient(server.URL, "").InstantQuery("prom", "up(", time.Now()); err == nil || !strings.Contains(err.Error(), "grafana query error: bad expr") {
		t.Fatalf("err = %v", err)
	}
}

func TestMetricNamesEscapesDatasourceUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/api/datasources/uid/a%2Fb%20c/resources/api/v1/label/__name__/values"; got != want {
			t.Errorf("path = %s, want %s", got, want)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if _, ok := r.Header["Authorization"]; ok {
			t.Errorf("Authorization sent without token")
		}
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	}))
	defer server.Close()

	names, err := NewClient(server.URL, "").MetricNames("a/b c")
	if err != nil || len(names) != 0 {
		t.Fatalf("MetricNames = %v, %v", names, err)
	}
}

func TestMetricNamesErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"http error", http.StatusForbidden, "denied", "grafana returned 403: denied"},
		{"prometheus error", http.StatusOK, `{"status":"error","errorType":"bad_data","error":"invalid"}`, "grafana metric names query failed (bad_data): invalid"},
		{"invalid json", http.StatusOK, `<html>`, "parsing metric names:"},
		// MetricNames does not go through the 429 retry loop.
		{"rate limited", http.StatusTooManyRequests, "slow", "grafana returned 429: slow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			_, err := NewClient(server.URL, "").MetricNames("prom")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want 1", calls)
			}
		})
	}
}
