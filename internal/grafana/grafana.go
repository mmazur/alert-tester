package grafana

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"alert-tester/internal/model"
)

type Client struct {
	URL        string
	Token      string
	HTTPClient *http.Client
}

func NewClient(url, token string) *Client {
	return &Client{
		URL:   strings.TrimRight(url, "/"),
		Token: token,
		HTTPClient: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
	}
}

type dsQueryRequest struct {
	Queries []dsQuery `json:"queries"`
	From    string    `json:"from"`
	To      string    `json:"to"`
}

type dsQuery struct {
	RefID         string            `json:"refId"`
	Expr          string            `json:"expr"`
	Datasource    dsQueryDatasource `json:"datasource"`
	IntervalMs    int64             `json:"intervalMs"`
	MaxDataPoints int               `json:"maxDataPoints"`
	Instant       bool              `json:"instant,omitempty"`
	Range         bool              `json:"range,omitempty"`
}

type dsQueryDatasource struct {
	UID  string `json:"uid"`
	Type string `json:"type"`
}

func (c *Client) RangeQuery(datasourceUID, expr string, from, to time.Time, step time.Duration) (*model.QueryResult, time.Duration, error) {
	stepMs := step.Milliseconds()
	if stepMs < 1000 {
		stepMs = 1000
	}

	reqBody := dsQueryRequest{
		Queries: []dsQuery{{
			RefID:         "A",
			Expr:          expr,
			Datasource:    dsQueryDatasource{UID: datasourceUID, Type: "prometheus"},
			IntervalMs:    stepMs,
			MaxDataPoints: int(math.Ceil(float64(to.Sub(from).Milliseconds()) / float64(stepMs))),
		}},
		From: fmt.Sprintf("%d", from.UnixMilli()),
		To:   fmt.Sprintf("%d", to.UnixMilli()),
	}

	return c.query(reqBody)
}

func (c *Client) InstantQuery(datasourceUID, expr string, at time.Time) (*model.QueryResult, error) {
	reqBody := dsQueryRequest{
		Queries: []dsQuery{{
			RefID:         "A",
			Expr:          expr,
			Datasource:    dsQueryDatasource{UID: datasourceUID, Type: "prometheus"},
			IntervalMs:    1000,
			MaxDataPoints: 1,
			Instant:       true,
		}},
		From: fmt.Sprintf("%d", at.UnixMilli()),
		To:   fmt.Sprintf("%d", at.UnixMilli()),
	}

	result, _, err := c.query(reqBody)
	return result, err
}

func (c *Client) MetricNames(datasourceUID string) ([]string, error) {
	endpoint := c.URL + "/api/datasources/uid/" + url.PathEscape(datasourceUID) + "/resources/api/v1/label/__name__/values"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("grafana returned %d: %s", resp.StatusCode, truncate(string(body), 500))
	}

	var result struct {
		Status    string   `json:"status"`
		Data      []string `json:"data"`
		ErrorType string   `json:"errorType"`
		Error     string   `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing metric names: %w", err)
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("grafana metric names query failed (%s): %s", result.ErrorType, result.Error)
	}
	return result.Data, nil
}

func (c *Client) query(reqBody dsQueryRequest) (*model.QueryResult, time.Duration, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling request: %w", err)
	}

	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequest(http.MethodPost, c.URL+"/api/ds/query", bytes.NewReader(body))
		if err != nil {
			return nil, 0, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("executing request: %w", err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, 0, fmt.Errorf("reading response: %w", readErr)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			time.Sleep(rateLimitDelay(resp.Header.Get("Retry-After"), attempt))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, 0, fmt.Errorf("grafana returned %d: %s", resp.StatusCode, truncate(string(respBody), 500))
		}
		return parseResponse(respBody)
	}

	panic("unreachable")
}

func rateLimitDelay(retryAfter string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(retryAfter); err == nil {
		if delay := time.Until(retryAt); delay > 0 {
			return delay
		}
		return 0
	}
	return time.Second << attempt
}

func parseExecutedStep(s string) (time.Duration, bool) {
	// executedQueryString format: "Expr: <q>\nStep: <duration>"
	idx := strings.Index(s, "Step: ")
	if idx < 0 {
		return 0, false
	}
	rest := s[idx+len("Step: "):]
	if end := strings.IndexAny(rest, "\n\r"); end >= 0 {
		rest = rest[:end]
	}
	d, err := time.ParseDuration(strings.TrimSpace(rest))
	if err != nil {
		return 0, false
	}
	return d, true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type dsQueryResponse struct {
	Results map[string]dsQueryResultEnvelope `json:"results"`
}

type dsQueryResultEnvelope struct {
	Frames []dsQueryFrame `json:"frames"`
	Error  string         `json:"error,omitempty"`
}

type dsQueryFrame struct {
	Schema dsQueryFrameSchema `json:"schema"`
	Data   dsQueryFrameData   `json:"data"`
}

type dsQueryFrameSchema struct {
	Fields []dsQueryFieldSchema `json:"fields"`
	Meta   dsQueryFrameMeta     `json:"meta"`
}

type dsQueryFrameMeta struct {
	ExecutedQueryString string `json:"executedQueryString"`
}

type dsQueryFieldSchema struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels,omitempty"`
}

type dsQueryFrameData struct {
	Values []json.RawMessage `json:"values"`
}

func parseResponse(body []byte) (*model.QueryResult, time.Duration, error) {
	var resp dsQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, 0, fmt.Errorf("parsing response: %w", err)
	}

	refA, ok := resp.Results["A"]
	if !ok {
		return nil, 0, fmt.Errorf("no result for refId A in response")
	}
	if refA.Error != "" {
		return nil, 0, fmt.Errorf("grafana query error: %s", refA.Error)
	}

	var result model.QueryResult
	var executedStep time.Duration
	for _, frame := range refA.Frames {
		if executedStep == 0 {
			if d, ok := parseExecutedStep(frame.Schema.Meta.ExecutedQueryString); ok {
				executedStep = d
			}
		}
		if len(frame.Schema.Fields) < 2 || len(frame.Data.Values) < 2 {
			continue
		}

		var timestamps []float64
		if err := json.Unmarshal(frame.Data.Values[0], &timestamps); err != nil {
			return nil, 0, fmt.Errorf("parsing timestamps: %w", err)
		}

		var values []jsonFloat
		if err := json.Unmarshal(frame.Data.Values[1], &values); err != nil {
			return nil, 0, fmt.Errorf("parsing values: %w", err)
		}

		labels := frame.Schema.Fields[1].Labels

		samples := make([]model.Sample, 0, len(timestamps))
		for i := range timestamps {
			if i >= len(values) {
				break
			}
			if values[i].IsNull {
				continue
			}
			samples = append(samples, model.Sample{
				Timestamp: time.UnixMilli(int64(timestamps[i])),
				Value:     values[i].Value,
			})
		}

		if len(samples) > 0 {
			result.Series = append(result.Series, model.Series{
				Labels:  labels,
				Samples: samples,
			})
		}
	}

	return &result, executedStep, nil
}

type jsonFloat struct {
	Value  float64
	IsNull bool
}

func (f *jsonFloat) UnmarshalJSON(data []byte) error {
	s := string(data)
	if s == "null" || s == "\"NaN\"" || s == "\"Inf\"" || s == "\"-Inf\"" {
		f.IsNull = true
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}
