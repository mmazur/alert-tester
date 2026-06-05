package query

import (
	"fmt"
	"io"
	"sort"
	"time"

	"alert-tester/internal/cache"
	"alert-tester/internal/grafana"
	"alert-tester/internal/model"
)

type Engine struct {
	Client               *grafana.Client
	Cache                *cache.Cache
	ChunkSize            time.Duration
	Verbose              bool
	AllowHighCardinality bool
	CacheOnly            bool
	Output               io.Writer
	// Progress is called once before fetching starts (with done=0) and then
	// once after each chunk completes (cache hit or fetched). total is the
	// total chunk count for this Query call.
	Progress func(done, total int)
}

type Stats struct {
	CacheHits      int
	CacheMisses    int
	Chunks         int
	CacheTime      time.Duration
	FetchTime      time.Duration
	SeriesReturned int
	SampleCount    int
	MaxCardinality int
}

const seriesLimitPerChunk = 1000

type TimeRange struct {
	From time.Time
	To   time.Time
}

func (e *Engine) Query(datasource, expr string, from, to time.Time, step time.Duration) (*model.QueryResult, Stats, error) {
	expr = cache.NormalizeExpr(expr)
	ranges := e.splitChunks(from, to)
	chunks := make([]TimeRange, len(ranges))
	for i, chunk := range ranges {
		chunks[i] = TimeRange{From: chunk.from, To: chunk.to}
	}
	return e.queryChunks(datasource, expr, chunks, step)
}

func (e *Engine) QueryChunks(datasource, expr string, chunks []TimeRange, step time.Duration) (*model.QueryResult, Stats, error) {
	expr = cache.NormalizeExpr(expr)
	return e.queryChunks(datasource, expr, chunks, step)
}

func (e *Engine) queryChunks(datasource, expr string, chunks []TimeRange, step time.Duration) (*model.QueryResult, Stats, error) {
	var stats Stats
	stats.Chunks = len(chunks)

	seriesMap := make(map[string]*model.Series)

	if e.Progress != nil {
		e.Progress(0, stats.Chunks)
	}

	for i, chunk := range chunks {
		t0 := time.Now()
		if result, ok := e.Cache.Get(e.Client.URL, datasource, expr, chunk.From, chunk.To, step); ok {
			stats.CacheHits++
			stats.CacheTime += time.Since(t0)
			if e.Verbose {
				e.printf("  chunk %d/%d [%s - %s] CACHE HIT (%d series)\n", i+1, stats.Chunks, chunk.From.Format(time.RFC3339), chunk.To.Format(time.RFC3339), len(result.Series))
			}
			if len(result.Series) > stats.MaxCardinality {
				stats.MaxCardinality = len(result.Series)
			}
			mergeSeries(seriesMap, result.Series)
			if e.Progress != nil {
				e.Progress(i+1, stats.Chunks)
			}
			continue
		}

		stats.CacheMisses++
		if e.Verbose {
			e.printf("  chunk %d/%d [%s - %s] querying...\n", i+1, stats.Chunks, chunk.From.Format(time.RFC3339), chunk.To.Format(time.RFC3339))
		}

		if e.CacheOnly || e.Client == nil {
			return nil, stats, fmt.Errorf("chunk %d missing from cache for %s [%s - %s]", i+1, expr, chunk.From.Format(time.RFC3339), chunk.To.Format(time.RFC3339))
		}

		t1 := time.Now()
		result, executedStep, err := e.Client.RangeQuery(datasource, expr, chunk.From, chunk.To, step)
		if err != nil {
			return nil, stats, fmt.Errorf("chunk %d query failed: %w", i+1, err)
		}
		stats.FetchTime += time.Since(t1)
		if e.Verbose {
			e.printf("  chunk %d/%d fetched (%d series)\n", i+1, stats.Chunks, len(result.Series))
		}
		if len(result.Series) > stats.MaxCardinality {
			stats.MaxCardinality = len(result.Series)
		}

		if executedStep > 0 && executedStep != step {
			return nil, stats, fmt.Errorf("chunk %d: grafana silently widened step from %s to %s (likely maxDataPoints/server limit); aborting to avoid caching undersampled data — reduce --chunk-size or increase --step", i+1, step, executedStep)
		}

		if !e.AllowHighCardinality && len(result.Series) > seriesLimitPerChunk {
			return nil, stats, fmt.Errorf("chunk %d returned %d series, above the %d safety limit; pass --allow-high-cardinality to override", i+1, len(result.Series), seriesLimitPerChunk)
		}

		if err := e.Cache.Put(e.Client.URL, datasource, expr, chunk.From, chunk.To, step, result); err != nil {
			e.printf("  warning: cache write failed: %v\n", err)
		}

		mergeSeries(seriesMap, result.Series)
		if e.Progress != nil {
			e.Progress(i+1, stats.Chunks)
		}
	}

	var combined model.QueryResult
	for _, s := range seriesMap {
		sort.Slice(s.Samples, func(i, j int) bool {
			return s.Samples[i].Timestamp.Before(s.Samples[j].Timestamp)
		})
		stats.SampleCount += len(s.Samples)
		combined.Series = append(combined.Series, *s)
	}
	stats.SeriesReturned = len(combined.Series)

	return &combined, stats, nil
}

func (e *Engine) printf(format string, args ...any) {
	if e.Output == nil {
		return
	}
	fmt.Fprintf(e.Output, format, args...)
}

type chunk struct {
	from, to time.Time
}

func (e *Engine) splitChunks(from, to time.Time) []chunk {
	chunkSec := int64(e.ChunkSize.Seconds())
	if chunkSec <= 0 {
		chunkSec = 3600
	}

	// Align to chunk boundaries for cache reuse
	alignedStart := from.Unix() - (from.Unix() % chunkSec)
	if alignedStart < from.Unix() {
		alignedStart = from.Unix() - (from.Unix() % chunkSec)
	}

	var chunks []chunk
	cursor := time.Unix(alignedStart, 0).UTC()
	if cursor.Before(from) {
		cursor = from
	}

	for cursor.Before(to) {
		nextBoundary := time.Unix(cursor.Unix()-cursor.Unix()%chunkSec+chunkSec, 0).UTC()
		end := nextBoundary
		if end.After(to) {
			end = to
		}
		chunks = append(chunks, chunk{from: cursor, to: end})
		cursor = end
	}

	return chunks
}

func seriesKey(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := "{"
	for i, k := range keys {
		if i > 0 {
			s += ","
		}
		s += k + "=" + labels[k]
	}
	s += "}"
	return s
}

func mergeSeries(m map[string]*model.Series, series []model.Series) {
	for _, s := range series {
		key := seriesKey(s.Labels)
		existing, ok := m[key]
		if !ok {
			copied := model.Series{
				Labels:  s.Labels,
				Samples: make([]model.Sample, len(s.Samples)),
			}
			copy(copied.Samples, s.Samples)
			m[key] = &copied
		} else {
			existing.Samples = append(existing.Samples, s.Samples...)
		}
	}
}
