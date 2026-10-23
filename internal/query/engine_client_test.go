package query

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"alert-tester/internal/cache"
	"alert-tester/internal/grafana"
)

// fakeGrafana serves /api/ds/query and returns one sample per step for a single series.
type fakeGrafana struct {
	mu           sync.Mutex
	requests     []string // "from-to" in ms
	executedStep string
	status       int
	seriesCount  int
}

func (f *fakeGrafana) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			From    string `json:"from"`
			To      string `json:"to"`
			Queries []struct {
				IntervalMs int64 `json:"intervalMs"`
			} `json:"queries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		f.mu.Lock()
		f.requests = append(f.requests, body.From+"-"+body.To)
		status, executed, n := f.status, f.executedStep, f.seriesCount
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("nope"))
			return
		}
		if n == 0 {
			n = 1
		}
		from, _ := strconv.ParseInt(body.From, 10, 64)
		to, _ := strconv.ParseInt(body.To, 10, 64)
		step := body.Queries[0].IntervalMs
		var ts, vals []string
		for t := from; t < to; t += step {
			ts = append(ts, strconv.FormatInt(t, 10))
			vals = append(vals, "1")
		}
		var frames []string
		for i := 0; i < n; i++ {
			meta := "{}"
			if i == 0 && executed != "" {
				meta = fmt.Sprintf(`{"executedQueryString":"Expr: x\nStep: %s"}`, executed)
			}
			frames = append(frames, fmt.Sprintf(`{"schema":{"fields":[{"name":"Time"},{"name":"Value","labels":{"i":"%d"}}],"meta":%s},"data":{"values":[[%s],[%s]]}}`,
				i, meta, strings.Join(ts, ","), strings.Join(vals, ",")))
		}
		fmt.Fprintf(w, `{"results":{"A":{"frames":[%s]}}}`, strings.Join(frames, ","))
	}
}

func newTestEngine(t *testing.T, fg *fakeGrafana) (*Engine, func()) {
	server := httptest.NewServer(fg.handler(t))
	return &Engine{
		Client:    grafana.NewClient(server.URL, ""),
		Cache:     cache.New(t.TempDir(), true),
		ChunkSize: time.Hour,
	}, server.Close
}

func TestEngineWithRealClientChunksAndMerges(t *testing.T) {
	fg := &fakeGrafana{}
	e, done := newTestEngine(t, fg)
	defer done()

	from := time.Date(2025, 5, 1, 1, 30, 0, 0, time.UTC)
	to := time.Date(2025, 5, 1, 4, 0, 0, 0, time.UTC)
	result, stats, err := e.Query("prom", "up", from, to, time.Minute)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	wantReqs := []string{
		fmt.Sprint(from.UnixMilli()) + "-" + fmt.Sprint(from.Add(30*time.Minute).UnixMilli()),
		fmt.Sprint(from.Add(30*time.Minute).UnixMilli()) + "-" + fmt.Sprint(from.Add(90*time.Minute).UnixMilli()),
		fmt.Sprint(from.Add(90*time.Minute).UnixMilli()) + "-" + fmt.Sprint(to.UnixMilli()),
	}
	if strings.Join(fg.requests, ",") != strings.Join(wantReqs, ",") {
		t.Fatalf("requests = %v, want %v", fg.requests, wantReqs)
	}
	if stats.Chunks != 3 || stats.CacheMisses != 3 || stats.CacheHits != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(result.Series) != 1 || len(result.Series[0].Samples) != 150 {
		t.Fatalf("got %d series / %d samples, want 1 / 150", len(result.Series), len(result.Series[0].Samples))
	}
	for i := 1; i < len(result.Series[0].Samples); i++ {
		if !result.Series[0].Samples[i].Timestamp.After(result.Series[0].Samples[i-1].Timestamp) {
			t.Fatalf("samples not strictly increasing at %d", i)
		}
	}

	// Second run must be served entirely from cache.
	_, stats, err = e.Query("prom", "up", from, to, time.Minute)
	if err != nil {
		t.Fatalf("second Query: %v", err)
	}
	if len(fg.requests) != 3 || stats.CacheHits != 3 || stats.CacheMisses != 0 {
		t.Fatalf("second run: requests=%d stats=%+v, want all cache hits", len(fg.requests), stats)
	}
}

func TestEngineWithRealClientPropagatesHTTPError(t *testing.T) {
	fg := &fakeGrafana{status: http.StatusInternalServerError}
	e, done := newTestEngine(t, fg)
	defer done()

	from := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	_, _, err := e.Query("prom", "up", from, from.Add(2*time.Hour), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "chunk 1 query failed: grafana returned 500: nope") {
		t.Fatalf("err = %v", err)
	}
	if len(fg.requests) != 1 {
		t.Fatalf("requests = %d, want 1 (stop at first failing chunk, no retry on 500)", len(fg.requests))
	}
	// Failed chunk must not be cached.
	fg.status = 0
	if _, stats, err := e.Query("prom", "up", from, from.Add(2*time.Hour), time.Minute); err != nil || stats.CacheHits != 0 {
		t.Fatalf("after recovery: err=%v stats=%+v", err, stats)
	}
}

func TestEngineWithRealClientRejectsWidenedStep(t *testing.T) {
	fg := &fakeGrafana{executedStep: "2m0s"}
	e, done := newTestEngine(t, fg)
	defer done()

	from := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	_, _, err := e.Query("prom", "up", from, from.Add(time.Hour), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "silently widened step from 1m0s to 2m0s") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := e.Cache.Get(e.Client.URL, "prom", cache.NormalizeExpr("up"), from, from.Add(time.Hour), time.Minute); ok {
		t.Fatalf("undersampled chunk was cached")
	}
}

func TestEngineWithRealClientCardinalityLimit(t *testing.T) {
	fg := &fakeGrafana{seriesCount: seriesLimitPerChunk + 1}
	e, done := newTestEngine(t, fg)
	defer done()

	from := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	_, _, err := e.Query("prom", "up", from, from.Add(10*time.Minute), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "above the 1000 safety limit") {
		t.Fatalf("err = %v", err)
	}

	e.AllowHighCardinality = true
	result, _, err := e.Query("prom", "up", from, from.Add(10*time.Minute), time.Minute)
	if err != nil || len(result.Series) != seriesLimitPerChunk+1 {
		t.Fatalf("with override: err=%v series=%d", err, len(result.Series))
	}
}
