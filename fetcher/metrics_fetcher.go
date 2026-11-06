package fetcher

import (
    "context"
    "time"
)

// Metric represents a single Prometheus metric sample.
type Metric struct {
    Name   string            // metric name
    Value  float64           // sample value
    Labels map[string]string // metric labels
    At     time.Time         // timestamp of sample
}

// MetricsFetcher defines an interface to fetch Prometheus metrics for an incident.
type MetricsFetcher interface {
    // Fetch returns a slice of metrics that belong to the specified incident.
    Fetch(ctx context.Context, incidentID string) ([]Metric, error)
}

// NoOpMetricsFetcher is a placeholder that always returns an empty slice.
// It is useful for tests that do not require real Prometheus calls.
type NoOpMetricsFetcher struct{}

func (NoOpMetricsFetcher) Fetch(ctx context.Context, incidentID string) ([]Metric, error) {
    _ = ctx
    _ = incidentID
    return []Metric{}, nil
}
