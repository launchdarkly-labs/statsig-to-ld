package statsig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/httputil"
	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
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

// ListMetricSources fetches paginated metric sources from Statsig.
// Returns raw maps because the warehouse command exports fields
// the typed model does not cover; ListAllMetricSources is the typed equivalent.
func (c *Client) ListMetricSources(ctx context.Context) ([]map[string]any, error) {
	var sources []map[string]any
	for page := 1; page <= maxPages; page++ {
		data, err := c.getRaw(ctx, "/metrics/metric_source/list", map[string]string{
			"limit": strconv.Itoa(pageSize),
			"page":  strconv.Itoa(page),
		})
		if err != nil {
			return nil, err
		}

		items := extractRawItems(data)
		sources = append(sources, items...)

		// A short page always ends the walk.
		if len(items) < pageSize {
			return sources, nil
		}
		if next, ok := nextPageOf(data); ok && next == "" {
			return sources, nil
		}
	}
	return sources, fmt.Errorf("Statsig metric sources pagination exceeded %d pages", maxPages)
}

// nextPageOf reads pagination.nextPage out of a raw response body. The second
// return distinguishes an absent pagination block from a present but empty
// nextPage, which mean opposite things for whether to keep paging.
func nextPageOf(data map[string]any) (string, bool) {
	p := j.GetMap(data, "pagination")
	if p == nil {
		return "", false
	}
	return j.GetStr(p, "nextPage"), true
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
