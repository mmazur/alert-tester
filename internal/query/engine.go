package query

import (
	"fmt"
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
	// Progress is called once before fetching starts (with done=0) and then
	// once after each chunk completes (cache hit or fetched). total is the
	// total chunk count for this Query call.
	Progress func(done, total int)
}

type Stats struct {
	CacheHits     int
	CacheMisses   int
	Chunks        int
	CacheTime     time.Duration
	FetchTime     time.Duration
	MaxCardinality int
}

const seriesLimitPerChunk = 1000

func (e *Engine) Query(datasource, expr string, from, to time.Time, step time.Duration) (*model.QueryResult, Stats, error) {
	expr = cache.NormalizeExpr(expr)
	chunks := e.splitChunks(from, to)
	var stats Stats
	stats.Chunks = len(chunks)

	seriesMap := make(map[string]*model.Series)

	if e.Progress != nil {
		e.Progress(0, stats.Chunks)
	}

	for i, chunk := range chunks {
		t0 := time.Now()
		if result, ok := e.Cache.Get(e.Client.URL, datasource, expr, chunk.from, chunk.to, step); ok {
			stats.CacheHits++
			stats.CacheTime += time.Since(t0)
			if e.Verbose {
				fmt.Printf("  chunk %d/%d [%s - %s] CACHE HIT (%d series)\n", i+1, stats.Chunks, chunk.from.Format(time.RFC3339), chunk.to.Format(time.RFC3339), len(result.Series))
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
			fmt.Printf("  chunk %d/%d [%s - %s] querying...\n", i+1, stats.Chunks, chunk.from.Format(time.RFC3339), chunk.to.Format(time.RFC3339))
		}

		t1 := time.Now()
		result, executedStep, err := e.Client.RangeQuery(datasource, expr, chunk.from, chunk.to, step)
		if err != nil {
			return nil, stats, fmt.Errorf("chunk %d query failed: %w", i+1, err)
		}
		stats.FetchTime += time.Since(t1)
		if e.Verbose {
			fmt.Printf("  chunk %d/%d fetched (%d series)\n", i+1, stats.Chunks, len(result.Series))
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

		if err := e.Cache.Put(e.Client.URL, datasource, expr, chunk.from, chunk.to, step, result); err != nil {
			fmt.Printf("  warning: cache write failed: %v\n", err)
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
		combined.Series = append(combined.Series, *s)
	}

	return &combined, stats, nil
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
