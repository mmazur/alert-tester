package cache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/prometheus/prometheus/promql/parser"

	"alert-tester/internal/model"
)

type Cache struct {
	Dir     string
	Enabled bool
}

func New(dir string, enabled bool) *Cache {
	return &Cache{Dir: dir, Enabled: enabled}
}

type entry struct {
	GrafanaURL   string         `json:"grafana_url"`
	Datasource   string         `json:"datasource"`
	Expr         string         `json:"expr"`
	From         time.Time      `json:"from"`
	To           time.Time      `json:"to"`
	StepSeconds  int            `json:"step_seconds"`
	FetchedAt    time.Time      `json:"fetched_at"`
	Series       []model.Series `json:"series"`
}

func (c *Cache) Get(grafanaURL, datasource, expr string, from, to time.Time, step time.Duration) (*model.QueryResult, bool) {
	if !c.Enabled {
		return nil, false
	}

	path := c.path(grafanaURL, datasource, expr, from, to, step)
	data, err := readMaybeGzip(path)
	if err != nil {
		return nil, false
	}

	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}

	return &model.QueryResult{Series: e.Series}, true
}

func readMaybeGzip(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		// fall back to legacy uncompressed path
		legacy := strings.TrimSuffix(path, ".gz")
		if legacy != path {
			return os.ReadFile(legacy)
		}
		return nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	return io.ReadAll(gr)
}

func (c *Cache) Put(grafanaURL, datasource, expr string, from, to time.Time, step time.Duration, result *model.QueryResult) error {
	path := c.path(grafanaURL, datasource, expr, from, to, step)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	e := entry{
		GrafanaURL:  grafanaURL,
		Datasource:  datasource,
		Expr:        expr,
		From:        from,
		To:          to,
		StepSeconds: int(step.Seconds()),
		FetchedAt:   time.Now(),
		Series:      result.Series,
	}

	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}

	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func (c *Cache) path(grafanaURL, datasource, expr string, from, to time.Time, step time.Duration) string {
	normalizedURL := strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(grafanaURL, "https://"), "http://"), "/")
	sourceDir := sanitize(normalizedURL) + "/" + sanitize(datasource)

	h := sha256.Sum256([]byte(NormalizeExpr(expr)))
	exprHash := fmt.Sprintf("%x", h[:8])

	filename := fmt.Sprintf("%d-%d-step%d.json.gz", from.Unix(), to.Unix(), int(step.Seconds()))

	return filepath.Join(c.Dir, sourceDir, exprHash, filename)
}

func NormalizeExpr(expr string) string {
	p := parser.NewParser(parser.Options{
		EnableExperimentalFunctions:  true,
		ExperimentalDurationExpr:     true,
		EnableExtendedRangeSelectors: true,
		EnableBinopFillModifiers:     true,
	})
	parsed, err := p.ParseExpr(expr)
	if err != nil {
		return expr
	}
	return parsed.String()
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
