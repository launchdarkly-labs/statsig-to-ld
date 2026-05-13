package statsig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/httputil"
	j "github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/jsonutil"
)

// getRaw performs a GET request and returns the parsed JSON response.
func (c *Client) getRaw(ctx context.Context, path string, params map[string]string) (map[string]any, error) {
	reqURL := c.apiBase + path
	if len(params) > 0 {
		vals := url.Values{}
		for k, v := range params {
			vals.Set(k, v)
		}
		reqURL += "?" + vals.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("STATSIG-API-KEY", c.apiKey)
	req.Header.Set("STATSIG-API-VERSION", "20240601")
	req.Header.Set("Content-Type", "application/json")

	body, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, nil)
	if err != nil {
		return nil, err
	}
	if statusCode != 200 {
		return nil, fmt.Errorf("Statsig API returned HTTP %d on GET %s: %s", statusCode, path, httputil.Truncate(string(body), 300))
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	return result, nil
}

// getRawOptional performs a GET request, returning nil on any error.
func (c *Client) getRawOptional(ctx context.Context, path string) map[string]any {
	result, err := c.getRaw(ctx, path, nil)
	if err != nil {
		return nil
	}
	return result
}

// GetWarehouseConnection fetches the Statsig warehouse connection configuration.
// Returns nil if not available (non-fatal).
func (c *Client) GetWarehouseConnection(ctx context.Context) map[string]any {
	data := c.getRawOptional(ctx, "/wh_connections")
	if data == nil {
		return nil
	}
	if d := j.GetMap(data, "data"); d != nil {
		return d
	}
	return data
}

// ListMetricSources fetches all metric sources from Statsig.
func (c *Client) ListMetricSources(ctx context.Context) ([]map[string]any, error) {
	data, err := c.getRaw(ctx, "/metrics/metric_source/list", nil)
	if err != nil {
		return nil, err
	}
	return extractRawItems(data), nil
}

// GetMetricSource fetches a single metric source by name.
func (c *Client) GetMetricSource(ctx context.Context, name string) (map[string]any, error) {
	data, err := c.getRaw(ctx, "/metrics/metric_source/"+name, nil)
	if err != nil {
		return nil, err
	}
	if d := j.GetMap(data, "data"); d != nil {
		return d, nil
	}
	return data, nil
}

// ListAllMetricsRaw fetches all metrics as raw maps (for the warehouse flow).
func (c *Client) ListAllMetricsRaw(ctx context.Context) ([]map[string]any, error) {
	var all []map[string]any
	page := 1
	for {
		data, err := c.getRaw(ctx, "/metrics/list", map[string]string{
			"page":  fmt.Sprintf("%d", page),
			"limit": "100",
		})
		if err != nil {
			return all, err
		}
		items := extractRawItems(data)
		if len(items) == 0 {
			break
		}
		all = append(all, items...)
		pagination := j.GetMap(data, "pagination")
		if pagination == nil || pagination["nextPage"] == nil {
			break
		}
		page++
	}
	return all, nil
}

// extractRawItems extracts the data array from Statsig API responses.
func extractRawItems(data map[string]any) []map[string]any {
	for _, key := range []string{"data", "results", "items"} {
		if raw := j.GetSlice(data, key); raw != nil {
			items := make([]map[string]any, 0, len(raw))
			for _, v := range raw {
				if m, ok := v.(map[string]any); ok {
					items = append(items, m)
				}
			}
			return items
		}
	}
	return nil
}
