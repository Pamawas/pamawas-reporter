package fetcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// LogEntry represents a single Loki log line.
type LogEntry struct {
	Timestamp time.Time
	Stream    string
	Line      string
}

// LogsFetcher defines an interface to fetch Loki logs for an incident.
type LogsFetcher interface {
	Fetch(ctx context.Context, incidentID string) ([]LogEntry, error)
}

// LokiFetcher is a concrete implementation that queries the Loki HTTP API.
type LokiFetcher struct {
	baseURL       string
	httpClient    *http.Client
	queryTemplate string
	timeout       time.Duration
	maxLines      int
}

// NewLokiFetcher constructs a LokiFetcher.
func NewLokiFetcher(url string, queryTemplate string, timeout time.Duration, maxLines int) *LokiFetcher {
	return &LokiFetcher{baseURL: url, httpClient: &http.Client{Timeout: timeout}, queryTemplate: queryTemplate, timeout: timeout, maxLines: maxLines}
}

// Loki API response structures
type lokiResponse struct {
	Status string     `json:"status"`
	Data   lokiData   `json:"data"`
}

type lokiData struct {
	ResultType string             `json:"resultType"`
	Result     []lokiStreamResult `json:"result"`
}

type lokiStreamResult struct {
	Stream logLabels        `json:"stream"`
	Values [][]interface{} `json:"values"`
}

type logLabels map[string]string

// Fetch retrieves recent logs for the incident.
func (l *LokiFetcher) Fetch(ctx context.Context, incidentID string) ([]LogEntry, error) {
	query := l.queryTemplate
	if query == "" {
		query = fmt.Sprintf("{incident=\"%s\"}", incidentID)
	} else {
		query = fmt.Sprintf(query, incidentID)
	}

	// 5 minutes window by default
	end := time.Now().UTC()
	start := end.Add(-5 * time.Minute)
	body := map[string]string{
		"query": query,
		"start": fmt.Sprintf("%f", float64(start.UnixNano())/1e9),
		"end":   fmt.Sprintf("%f", float64(end.UnixNano())/1e9),
		"limit": fmt.Sprintf("%d", l.maxLines),
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL+"/v1/query_range", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki returned status %d", resp.StatusCode)
	}
	var r lokiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	var logs []LogEntry
	for _, res := range r.Data.Result {
		for _, val := range res.Values {
			if t, ok := val[0].(float64); ok {
				logs = append(logs, LogEntry{Timestamp: time.Unix(0, int64(t*1e9)), Stream: res.Stream["__name__"], Line: val[1].(string)})
			}
		}
	}
	return logs, nil
}

// NoOpLogsFetcher is a placeholder that always returns empty logs.
type NoOpLogsFetcher struct{}

func (NoOpLogsFetcher) Fetch(ctx context.Context, incidentID string) ([]LogEntry, error) { return []LogEntry{}, nil }