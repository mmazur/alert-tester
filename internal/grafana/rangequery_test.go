package grafana

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alert-tester/internal/model"
)

const rangeResponseTwoSeries = `{"results":{"A":{"frames":[
 {"schema":{"fields":[{"name":"Time","type":"time"},{"name":"Value","type":"number","labels":{"cluster":"a"}}],
  "meta":{"executedQueryString":"Expr: up\nStep: 1m0s"}},
  "data":{"values":[[1746061200000,1746061260000,1746061320000],[1,null,"NaN"]]}},
 {"schema":{"fields":[{"name":"Time","type":"time"},{"name":"Value","type":"number","labels":{"cluster":"b"}}],"meta":{}},
  "data":{"values":[[1746061200000,1746061260000],[2.5,"Inf"]]}},
 {"schema":{"fields":[{"name":"Time","type":"time"},{"name":"Value","type":"number","labels":{"cluster":"empty"}}],"meta":{}},
  "data":{"values":[[1746061200000],[null]]}}
]}}}`

func TestRangeQueryRequestShape(t *testing.T) {
	from := time.Date(2025, 5, 1, 1, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/ds/query" {
			t.Errorf("path = %s, want /api/ds/query", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}

		raw, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		want := map[string]any{
			"from": "1746061200000",
			"to":   "1746064800000",
			"queries": []any{map[string]any{
				"refId":         "A",
				"expr":          `sum(rate(x[5m]))`,
				"datasource":    map[string]any{"uid": "prom", "type": "prometheus"},
				"intervalMs":    float64(60000),
				"maxDataPoints": float64(60),
			}},
		}
		// Exact match also guards against new keys (e.g. instant/range) leaking into range queries.
		if !reflect.DeepEqual(got, want) {
			t.Errorf("request body =\n%s\nwant keys/values %v", raw, want)
		}
		_, _ = w.Write([]byte(rangeResponseTwoSeries))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/", "secret")
	if _, _, err := client.RangeQuery("prom", `sum(rate(x[5m]))`, from, to, time.Minute); err != nil {
		t.Fatalf("RangeQuery: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRangeQueryNoTokenOmitsAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Errorf("Authorization header sent without token")
		}
		_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
	}))
	defer server.Close()

	from := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := NewClient(server.URL, "").RangeQuery("prom", "up", from, from.Add(time.Hour), time.Minute); err != nil {
		t.Fatalf("RangeQuery: %v", err)
	}
}

func TestRangeQueryStepAndMaxDataPoints(t *testing.T) {
	from := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name           string
		span           time.Duration
		step           time.Duration
		wantIntervalMs float64
		wantMaxPoints  float64
	}{
		{"sub-second step clamps to 1s", time.Minute, 100 * time.Millisecond, 1000, 60},
		{"non-divisible span rounds up", 61 * time.Minute, time.Minute, 60000, 61},
		{"partial step rounds up", 90 * time.Second, time.Minute, 60000, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Queries []map[string]any `json:"queries"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				q := body.Queries[0]
				if q["intervalMs"] != tc.wantIntervalMs || q["maxDataPoints"] != tc.wantMaxPoints {
					t.Errorf("intervalMs=%v maxDataPoints=%v, want %v/%v", q["intervalMs"], q["maxDataPoints"], tc.wantIntervalMs, tc.wantMaxPoints)
				}
				_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
			}))
			defer server.Close()
			if _, _, err := NewClient(server.URL, "").RangeQuery("prom", "up", from, from.Add(tc.span), tc.step); err != nil {
				t.Fatalf("RangeQuery: %v", err)
			}
		})
	}
}

func TestRangeQueryParsesFrames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rangeResponseTwoSeries))
	}))
	defer server.Close()

	from := time.UnixMilli(1746061200000).UTC()
	result, step, err := NewClient(server.URL, "").RangeQuery("prom", "up", from, from.Add(3*time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("RangeQuery: %v", err)
	}
	if step != time.Minute {
		t.Fatalf("executed step = %s, want 1m", step)
	}
	want := []model.Series{
		{Labels: map[string]string{"cluster": "a"}, Samples: []model.Sample{{Timestamp: time.UnixMilli(1746061200000), Value: 1}}},
		{Labels: map[string]string{"cluster": "b"}, Samples: []model.Sample{{Timestamp: time.UnixMilli(1746061200000), Value: 2.5}}},
	}
	if len(result.Series) != len(want) {
		t.Fatalf("series = %+v, want %+v", result.Series, want)
	}
	for i := range want {
		got := result.Series[i]
		if !reflect.DeepEqual(got.Labels, want[i].Labels) || len(got.Samples) != len(want[i].Samples) {
			t.Fatalf("series[%d] = %+v, want %+v", i, got, want[i])
		}
		for j := range want[i].Samples {
			if !got.Samples[j].Timestamp.Equal(want[i].Samples[j].Timestamp) || got.Samples[j].Value != want[i].Samples[j].Value {
				t.Fatalf("series[%d] sample[%d] = %+v, want %+v", i, j, got.Samples[j], want[i].Samples[j])
			}
		}
	}
}

func TestRangeQueryParsesSpecialFloatsAsSkipped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":{"A":{"frames":[{"schema":{"fields":[{"name":"Time"},{"name":"Value"}],"meta":{}},
			"data":{"values":[[1,2,3,4,5],[null,"NaN","Inf","-Inf",7]]}}]}}}`))
	}))
	defer server.Close()

	from := time.UnixMilli(0)
	result, _, err := NewClient(server.URL, "").RangeQuery("prom", "up", from, from.Add(time.Minute), time.Second)
	if err != nil {
		t.Fatalf("RangeQuery: %v", err)
	}
	if len(result.Series) != 1 || len(result.Series[0].Samples) != 1 || result.Series[0].Samples[0].Value != 7 || math.IsNaN(result.Series[0].Samples[0].Value) {
		t.Fatalf("result = %+v, want single sample value 7", result)
	}
}

func TestRangeQueryErrors(t *testing.T) {
	from := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"server error", http.StatusInternalServerError, "boom", "grafana returned 500: boom"},
		{"bad request", http.StatusBadRequest, `{"message":"bad"}`, `grafana returned 400: {"message":"bad"}`},
		{"long body truncated", http.StatusBadGateway, strings.Repeat("x", 600), "grafana returned 502: " + strings.Repeat("x", 500) + "..."},
		{"refId error", http.StatusOK, `{"results":{"A":{"error":"parse error at char 3"}}}`, "grafana query error: parse error at char 3"},
		{"missing refId", http.StatusOK, `{"results":{}}`, "no result for refId A in response"},
		{"invalid json", http.StatusOK, `not json`, "parsing response:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			_, _, err := NewClient(server.URL, "").RangeQuery("prom", "up", from, from.Add(time.Hour), time.Minute)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want exactly 1 (non-429 errors must not be retried)", calls)
			}
		})
	}
}

func TestRangeQueryConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	from := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	_, _, err := NewClient(url, "").RangeQuery("prom", "up", from, from.Add(time.Hour), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "executing request:") {
		t.Fatalf("err = %v, want executing request error", err)
	}
}
