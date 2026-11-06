package fetcher

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// PrometheusFetcher is a concrete implementation that queries Prometheus.
type PrometheusFetcher struct {
	client        v1.API
	queryTemplate string
	timeout       time.Duration
}

// NewPrometheusFetcher constructs a PrometheusFetcher.
// queryTemplate should contain a single %s placeholder for the incident ID, e.g. `{incident="%s"}`.
func NewPrometheusFetcher(prometheusURL string, queryTemplate string, timeout time.Duration) (*PrometheusFetcher, error) {
	client, err := api.NewClient(api.Config{Address: prometheusURL})
	if err != nil {
		return nil, fmt.Errorf("failed to create prometheus client: %w", err)
	}
	return &PrometheusFetcher{
		client:        v1.NewAPI(client),
		queryTemplate: queryTemplate,
		timeout:       timeout,
	}, nil
}

// Fetch retrieves metric samples for the incident.
func (p *PrometheusFetcher) Fetch(ctx context.Context, incidentID string) ([]Metric, error) {
	query := p.queryTemplate
	if query == "" {
		query = fmt.Sprintf("{incident=\"%s\"}", incidentID)
	} else {
		query = fmt.Sprintf(query, incidentID)
	}

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	result, warnings, _, err := p.client.Query(ctx, query, time.Now())
	if err != nil {
		return nil, fmt.Errorf("prometheus query failed: %w", err)
	}
	if len(warnings) > 0 {
		// Log warnings but don't fail
	}

	vector, ok := result.(model.Vector)
	if !ok {
		return []Metric{}, nil
	}

	metrics := make([]Metric, 0, len(vector))
	for _, sample := range vector {
		labels := make(map[string]string)
		for k, v := range sample.Metric {
			if k != "__name__" {
				labels[string(k)] = string(v)
			}
		}
		metrics = append(metrics, Metric{
			Name:   string(sample.Metric[model.MetricNameLabel]),
			Value:  float64(sample.Value),
			Labels: labels,
			At:     sample.Timestamp.Time(),
		})
	}
	return metrics, nil
}