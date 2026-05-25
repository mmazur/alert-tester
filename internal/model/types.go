package model

import "time"

type Sample struct {
	Timestamp time.Time
	Value     float64
}

type Series struct {
	Labels  map[string]string
	Samples []Sample
}

type QueryResult struct {
	Series []Series
}

type FiringRange struct {
	FirstPending time.Time
	FirstFired   time.Time
	LastFired    time.Time
	MaxValue     float64
}

type AlertResult struct {
	Expr        string
	For         time.Duration
	LabelSet    map[string]string
	Fired       bool
	Firings     []FiringRange
	Error       string
	CacheHits   int
	CacheMisses int
}

type Incident struct {
	CorrelationKey string
	Labels         map[string]string
	Firings        []FiringRange
	FirstPending   time.Time
	LastFired      time.Time
}
