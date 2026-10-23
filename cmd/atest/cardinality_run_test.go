package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"alert-tester/internal/model"
)

// fakeCardinalityGrafana serves metric-name discovery and count() instant queries.
type fakeCardinalityGrafana struct {
	names    []string
	counts   map[string]int // missing => empty result
	fail     map[string]bool
	mu       sync.Mutex
	auth     []string
	inflight int32
	maxSeen  int32
}

func (f *fakeCardinalityGrafana) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/datasources/uid/prom/resources/api/v1/label/__name__/values":
			data, _ := json.Marshal(f.names)
			fmt.Fprintf(w, `{"status":"success","data":%s}`, data)
		case "/api/ds/query":
			n := atomic.AddInt32(&f.inflight, 1)
			defer atomic.AddInt32(&f.inflight, -1)
			for {
				m := atomic.LoadInt32(&f.maxSeen)
				if n <= m || atomic.CompareAndSwapInt32(&f.maxSeen, m, n) {
					break
				}
			}
			var body struct {
				Queries []struct {
					Expr    string `json:"expr"`
					Instant bool   `json:"instant"`
				} `json:"queries"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			q := body.Queries[0]
			if !q.Instant {
				t.Errorf("expected instant query, got %+v", q)
			}
			var metric string
			if _, err := fmt.Sscanf(q.Expr, "count({__name__=%q})", &metric); err != nil {
				t.Errorf("unexpected expr %q: %v", q.Expr, err)
			}
			if f.fail[metric] {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			c, ok := f.counts[metric]
			if !ok {
				_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
				return
			}
			fmt.Fprintf(w, `{"results":{"A":{"frames":[{"schema":{"fields":[{"name":"Time"},{"name":"Value"}],"meta":{}},"data":{"values":[[1790683200000],[%d]]}}]}}}`, c)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newFakeCardinality() *fakeCardinalityGrafana {
	return &fakeCardinalityGrafana{
		names:  []string{"up", "kube_pod_info", "kube_node_info", "gone", "zero", "weird\"name"},
		counts: map[string]int{"up": 3, "kube_pod_info": 12000, "kube_node_info": 40, "zero": 0, "weird\"name": 5},
		fail:   map[string]bool{},
	}
}

func runCardinalityForTest(t *testing.T, f *cardinalityFlags) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runCardinality(f, &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestRunCardinalityTableEndToEnd(t *testing.T) {
	fake := newFakeCardinality()
	server := fake.serve(t)

	out, errOut, err := runCardinalityForTest(t, &cardinalityFlags{
		grafanaURL: server.URL, datasource: "prom", bearerToken: "tok",
		format: "table", minSeries: 1, limit: 50, concurrency: 1,
	})
	if err != nil {
		t.Fatalf("runCardinality: %v", err)
	}
	want := "SERIES  METRIC\n" +
		"12000   kube_pod_info\n" +
		"40      kube_node_info\n" +
		"5       weird\"name\n" +
		"3       up\n"
	if out != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", out, want)
	}
	for _, s := range []string{"discovering metric names...", "querying 6 metrics with concurrency 1...", "progress: 6/6 metrics queried (0 failed)", "4 metrics matched --min-series, 4 rows written"} {
		if !strings.Contains(errOut, s) {
			t.Errorf("stderr missing %q:\n%s", s, errOut)
		}
	}
	for _, a := range fake.auth {
		if a != "Bearer tok" {
			t.Fatalf("Authorization = %q on some request", a)
		}
	}
	if fake.maxSeen != 1 {
		t.Fatalf("max concurrent queries = %d, want 1 with --concurrency 1", fake.maxSeen)
	}
}

func TestRunCardinalityCSVLimitMinSeriesRegex(t *testing.T) {
	server := newFakeCardinality().serve(t)

	out, errOut, err := runCardinalityForTest(t, &cardinalityFlags{
		grafanaURL: server.URL, datasource: "prom", metricRegex: "^kube_|^up$",
		format: "csv", minSeries: 4, limit: 1, concurrency: 3, noProgress: true,
	})
	if err != nil {
		t.Fatalf("runCardinality: %v", err)
	}
	if want := "series,metric\n12000,kube_pod_info\n"; out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
	if errOut != "" {
		t.Fatalf("stderr with --no-progress and no failures = %q, want empty", errOut)
	}
}

func TestRunCardinalityLimitZeroMeansAllAndMinSeriesZeroKeepsEmpty(t *testing.T) {
	server := newFakeCardinality().serve(t)

	out, _, err := runCardinalityForTest(t, &cardinalityFlags{
		grafanaURL: server.URL, datasource: "prom",
		format: "csv", minSeries: 0, limit: 0, concurrency: 2, noProgress: true,
	})
	if err != nil {
		t.Fatalf("runCardinality: %v", err)
	}
	want := "series,metric\n12000,kube_pod_info\n40,kube_node_info\n5,\"weird\"\"name\"\n3,up\n0,gone\n0,zero\n"
	if out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
}

func TestRunCardinalityPartialFailuresWarn(t *testing.T) {
	fake := newFakeCardinality()
	fake.fail["kube_node_info"] = true
	fake.fail["up"] = true
	server := fake.serve(t)

	out, errOut, err := runCardinalityForTest(t, &cardinalityFlags{
		grafanaURL: server.URL, datasource: "prom",
		format: "csv", minSeries: 1, limit: 50, concurrency: 1,
	})
	if err != nil {
		t.Fatalf("runCardinality: %v", err)
	}
	if want := "series,metric\n12000,kube_pod_info\n5,\"weird\"\"name\"\n"; out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
	for _, s := range []string{"query failed: kube_node_info: grafana returned 500", "query failed: up: grafana returned 500", "(2 failed)", "warning: 2 metric queries failed; showing successful results"} {
		if !strings.Contains(errOut, s) {
			t.Errorf("stderr missing %q:\n%s", s, errOut)
		}
	}
}

func TestRunCardinalityConcurrencyIsBoundedAndComplete(t *testing.T) {
	fake := &fakeCardinalityGrafana{counts: map[string]int{}, fail: map[string]bool{}}
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("m%03d", i)
		fake.names = append(fake.names, name)
		fake.counts[name] = i + 1
	}
	server := fake.serve(t)

	out, _, err := runCardinalityForTest(t, &cardinalityFlags{
		grafanaURL: server.URL, datasource: "prom",
		format: "csv", minSeries: 1, limit: 0, concurrency: 8, noProgress: true,
	})
	if err != nil {
		t.Fatalf("runCardinality: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 201 || lines[1] != "200,m199" || lines[200] != "1,m000" {
		t.Fatalf("got %d lines, first=%q last=%q", len(lines), lines[1], lines[len(lines)-1])
	}
	if fake.maxSeen > 8 {
		t.Fatalf("max concurrent queries = %d, want <= 8", fake.maxSeen)
	}
}

func TestRunCardinalityTokenFromEnv(t *testing.T) {
	t.Setenv("ATEST_GRAFANA_BEARER_TOKEN", "envtok")
	fake := newFakeCardinality()
	server := fake.serve(t)

	if _, _, err := runCardinalityForTest(t, &cardinalityFlags{
		grafanaURL: server.URL, datasource: "prom", format: "table", minSeries: 1, concurrency: 1, noProgress: true,
	}); err != nil {
		t.Fatalf("runCardinality: %v", err)
	}
	if len(fake.auth) == 0 || fake.auth[0] != "Bearer envtok" {
		t.Fatalf("auth = %q, want Bearer envtok", fake.auth)
	}
}

func TestRunCardinalityMetricNamesFailureIsFatal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer server.Close()

	out, _, err := runCardinalityForTest(t, &cardinalityFlags{
		grafanaURL: server.URL, datasource: "prom", format: "table", minSeries: 1, concurrency: 1, noProgress: true,
	})
	if err == nil || !strings.Contains(err.Error(), "listing metric names: grafana returned 401") {
		t.Fatalf("err = %v", err)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty on fatal error", out)
	}
}

func TestRunCardinalityValidatesFlags(t *testing.T) {
	base := cardinalityFlags{grafanaURL: "http://127.0.0.1:1", datasource: "prom", format: "table", minSeries: 1, limit: 50, concurrency: 1}
	cases := []struct {
		name    string
		mutate  func(*cardinalityFlags)
		wantErr string
	}{
		{"concurrency zero", func(f *cardinalityFlags) { f.concurrency = 0 }, "--concurrency must be at least 1"},
		{"negative limit", func(f *cardinalityFlags) { f.limit = -1 }, "--limit cannot be negative"},
		{"negative min-series", func(f *cardinalityFlags) { f.minSeries = -1 }, "--min-series cannot be negative"},
		{"bad format", func(f *cardinalityFlags) { f.format = "json" }, "--format must be table or csv"},
		{"bad regex", func(f *cardinalityFlags) { f.metricRegex = "(" }, "invalid --metric-regex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			tc.mutate(&f)
			if _, _, err := runCardinalityForTest(t, &f); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestCardinalityCommandRequiredFlagsAndDefaults(t *testing.T) {
	cmd := newCardinalityCmd()
	cmd.SetArgs([]string{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "required flag") {
		t.Fatalf("err = %v, want required flag error", err)
	}

	defaults := map[string]string{"format": "table", "min-series": "1", "limit": "50", "concurrency": "1", "no-progress": "false"}
	for name, want := range defaults {
		if got := cmd.Flags().Lookup(name).DefValue; got != want {
			t.Errorf("--%s default = %q, want %q", name, got, want)
		}
	}
}

func TestScalarValue(t *testing.T) {
	cases := []struct {
		name string
		in   *model.QueryResult
		want int64
	}{
		{"nil", nil, 0},
		{"no series", &model.QueryResult{}, 0},
		{"no samples", &model.QueryResult{Series: []model.Series{{}}}, 0},
		{"last sample wins", &model.QueryResult{Series: []model.Series{{Samples: []model.Sample{{Value: 1}, {Value: 9}}}}}, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scalarValue(tc.in); got != tc.want {
				t.Fatalf("scalarValue = %d, want %d", got, tc.want)
			}
		})
	}
}
