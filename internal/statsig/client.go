// Package statsig provides a client for the Statsig Console API.
package statsig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/httputil"
)

const defaultAPIBase = "https://statsigapi.net/console/v1"

// Metric represents a Statsig metric definition as returned by the Console API.
type Metric struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Type           string        `json:"type"`
	Description    string        `json:"description"`
	Directionality string        `json:"directionality"`
	UnitTypes      []string      `json:"unitTypes"`
	MetricEvents   []MetricEvent `json:"metricEvents"`
	Tags           []string      `json:"tags"`

	RollupTimeWindow  string   `json:"rollupTimeWindow"`
	CustomRollUpStart *float64 `json:"customRollUpStart"`
	CustomRollUpEnd   *float64 `json:"customRollUpEnd"`

	// Warehouse Native / advanced fields
	WarehouseNative *WarehouseNative `json:"warehouseNative"`

	// Derived metric fields
	MetricComponentMetrics []ComponentMetric `json:"metricComponentMetrics"`
	FunnelEventList        []FunnelEvent     `json:"funnelEventList"`
	FunnelCountDistinct    string            `json:"funnelCountDistinct"`

	// Source information (for WH Native data source resolution)
	MetricSourceName string `json:"metricSourceName"`
}

// MetricEvent represents an event definition within a Statsig metric.
type MetricEvent struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	MetadataKey string      `json:"metadataKey"`
	Criteria    []Criterion `json:"criteria"`
}

// Criterion represents a filter condition on a metric event.
type Criterion struct {
	Type      string   `json:"type"`
	Column    string   `json:"column"`
	Condition string   `json:"condition"`
	Values    []string `json:"values"`
}

// WarehouseNative contains Statsig Warehouse Native-specific metric configuration.
type WarehouseNative struct {
	WinsorizationHigh *float64 `json:"winsorizationHigh"`
	WinsorizationLow  *float64 `json:"winsorizationLow"`
	Cap               *float64 `json:"cap"`
	Percentile        *float64 `json:"percentile"`
	UseLogTransform   *bool    `json:"useLogTransform"`
}

// ComponentMetric is a reference to another metric used in composite metrics.
type ComponentMetric struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// FunnelEvent is a step in a funnel metric.
type FunnelEvent struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type listResponse struct {
	Message    string   `json:"message"`
	Data       []Metric `json:"data"`
	HasMore    bool     `json:"has_more"`
	NextCursor string   `json:"next_page"`
}

// Client is a Statsig Console API client.
type Client struct {
	apiKey     string
	apiBase    string
	httpClient *http.Client
}

// NewClient creates a new Statsig Console API client.
// If baseURL is empty, the default Statsig API URL is used.
func NewClient(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultAPIBase
	}
	return &Client{
		apiKey:     apiKey,
		apiBase:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ListAllMetrics fetches all metrics from the Statsig project, handling pagination.
func (c *Client) ListAllMetrics(ctx context.Context) ([]Metric, error) {
	var all []Metric
	cursor := ""

	for {
		reqURL := fmt.Sprintf("%s/metrics/list?limit=100", c.apiBase)
		if cursor != "" {
			reqURL += "&cursor=" + url.QueryEscape(cursor)
		}

		batch, nextCursor, err := c.fetchPage(ctx, reqURL)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	return all, nil
}

// GetMetricByName fetches all metrics and returns the one matching the given name.
// Note: the Statsig Console API does not support server-side name filtering,
// so this fetches the full metric list and scans locally.
func (c *Client) GetMetricByName(ctx context.Context, name string) (*Metric, error) {
	metrics, err := c.ListAllMetrics(ctx)
	if err != nil {
		return nil, err
	}

	for i := range metrics {
		if metrics[i].Name == name {
			return &metrics[i], nil
		}
	}

	return nil, fmt.Errorf("metric %q not found among %d Statsig metrics", name, len(metrics))
}

func (c *Client) fetchPage(ctx context.Context, reqURL string) ([]Metric, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("STATSIG-API-KEY", c.apiKey)

	body, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, nil)
	if err != nil {
		return nil, "", err
	}

	if statusCode != 200 {
		return nil, "", fmt.Errorf("Statsig API returned HTTP %d: %s", statusCode, httputil.Truncate(string(body), 300))
	}

	var listResp listResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, "", fmt.Errorf("parsing response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
	}

	nextCursor := ""
	if listResp.HasMore {
		nextCursor = listResp.NextCursor
	}

	return listResp.Data, nextCursor, nil
}
