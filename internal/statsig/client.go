// Package statsig provides a client for the Statsig Console API.
package statsig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/httputil"
)

const (
	defaultAPIBase = "https://statsigapi.net/console/v1"
	// apiVersion pins the Statsig Console API version. Statsig recommends
	// pinning so behavior is stable as the API evolves.
	apiVersion = "20240601"
	// pageSize is the per-page item count for page-number-paginated endpoints.
	pageSize = 100
	// maxPages caps pagination at 100,000 items (1000 × 100). Defensive bound;
	// real projects don't approach this.
	maxPages = 1000
)

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

// ============================================================================
// Metric endpoints (cursor pagination)
// ============================================================================

// ListAllMetrics fetches all metrics from the Statsig project, following the
// page-number pagination in the response's `pagination` block (same mechanism
// as the gates / dynamic-config endpoints). The list endpoint caps a page at
// `limit` items and advances via `page`; stopping on an empty `nextPage` or a
// short page is what keeps large projects (>100 metrics) from being under-read.
func (c *Client) ListAllMetrics(ctx context.Context) ([]Metric, error) {
	all := make([]Metric, 0)
	for page := 1; page <= maxPages; page++ {
		reqURL := fmt.Sprintf("%s/metrics/list?limit=%d&page=%d", c.apiBase, pageSize, page)
		batch, nextPage, err := c.fetchMetricsPage(ctx, reqURL)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)

		if nextPage == "" || len(batch) < pageSize {
			return all, nil
		}
	}
	return all, fmt.Errorf("Statsig metrics pagination exceeded %d pages", maxPages)
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

func (c *Client) fetchMetricsPage(ctx context.Context, reqURL string) ([]Metric, string, error) {
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

	var listResp metricListResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, "", fmt.Errorf("parsing response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
	}

	return listResp.Data, listResp.Pagination.NextPage, nil
}

// ============================================================================
// Gate / Dynamic Config endpoints (page-number pagination)
// ============================================================================

// ListGates fetches all Feature Gates from the Statsig project.
func (c *Client) ListGates(ctx context.Context) ([]Gate, error) {
	reqURL, err := url.JoinPath(c.apiBase, "gates")
	if err != nil {
		return nil, fmt.Errorf("building gates URL: %w", err)
	}

	gates := make([]Gate, 0)
	for page := 1; page <= maxPages; page++ {
		body, statusCode, err := c.makePagedRequest(ctx, reqURL, page)
		if err != nil {
			return nil, err
		}
		if statusCode != http.StatusOK {
			return nil, fmt.Errorf("listing Statsig gates: HTTP %d: %s", statusCode, httputil.Truncate(string(body), 300))
		}

		var r gatesListResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("parsing gates response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
		}

		gates = append(gates, r.Data...)

		if r.Pagination.NextPage == "" || len(r.Data) < pageSize {
			return gates, nil
		}
	}
	return gates, fmt.Errorf("Statsig gates pagination exceeded %d pages", maxPages)
}

// ListDynamicConfigs fetches all Dynamic Configs from the Statsig project.
func (c *Client) ListDynamicConfigs(ctx context.Context) ([]DynamicConfig, error) {
	reqURL, err := url.JoinPath(c.apiBase, "dynamic_configs")
	if err != nil {
		return nil, fmt.Errorf("building dynamic_configs URL: %w", err)
	}

	configs := make([]DynamicConfig, 0)
	for page := 1; page <= maxPages; page++ {
		body, statusCode, err := c.makePagedRequest(ctx, reqURL, page)
		if err != nil {
			return nil, err
		}
		if statusCode != http.StatusOK {
			return nil, fmt.Errorf("listing Statsig dynamic configs: HTTP %d: %s", statusCode, httputil.Truncate(string(body), 300))
		}

		var r dynamicConfigsListResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("parsing dynamic_configs response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
		}

		configs = append(configs, r.Data...)

		if r.Pagination.NextPage == "" || len(r.Data) < pageSize {
			return configs, nil
		}
	}
	return configs, fmt.Errorf("Statsig dynamic configs pagination exceeded %d pages", maxPages)
}

// ============================================================================
// Environment + Override endpoints (unpaged)
// ============================================================================

// ListEnvironments fetches all environments configured for the Statsig project.
func (c *Client) ListEnvironments(ctx context.Context) ([]Environment, error) {
	reqURL, err := url.JoinPath(c.apiBase, "environments")
	if err != nil {
		return nil, fmt.Errorf("building environments URL: %w", err)
	}

	body, statusCode, err := c.makeUnpagedGetRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("listing Statsig environments: HTTP %d: %s", statusCode, httputil.Truncate(string(body), 300))
	}

	var r environmentsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parsing environments response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
	}
	return r.Data.Environments, nil
}

// GetGateOverrides fetches all env-scoped overrides for a gate. Returns
// (nil, nil) on 404 — a gate without overrides is not an error condition.
func (c *Client) GetGateOverrides(ctx context.Context, gateID string) ([]Override, error) {
	return c.fetchOverrides(ctx, "gates", gateID)
}

// GetDynamicConfigOverrides fetches all env-scoped overrides for a Dynamic Config.
// Same shape as gate overrides: passing → variation 0, failing → variation 1.
// For multi-variant DCs this is lossy (the API only exposes binary pass/fail
// per user); the warning is documented in the README.
func (c *Client) GetDynamicConfigOverrides(ctx context.Context, configID string) ([]Override, error) {
	return c.fetchOverrides(ctx, "dynamic_configs", configID)
}

func (c *Client) fetchOverrides(ctx context.Context, kind, id string) ([]Override, error) {
	reqURL, err := url.JoinPath(c.apiBase, kind, id, "overrides")
	if err != nil {
		return nil, fmt.Errorf("building overrides URL: %w", err)
	}

	body, statusCode, err := c.makeUnpagedGetRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}
	switch statusCode {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, fmt.Errorf("fetching %s overrides for %s: HTTP %d: %s", kind, id, statusCode, httputil.Truncate(string(body), 300))
	}

	var r overridesResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parsing overrides response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
	}
	return r.Data.EnvironmentOverrides, nil
}

// ============================================================================
// Filter helpers (no I/O)
// ============================================================================

// FilterGates returns gates whose Tags contain `tag`. Empty tag returns input unchanged.
func FilterGates(gates []Gate, tag string) []Gate {
	if tag == "" {
		return gates
	}
	filtered := make([]Gate, 0, len(gates))
	for _, g := range gates {
		if slices.Contains(g.Tags, tag) {
			filtered = append(filtered, g)
		}
	}
	return filtered
}

// FilterDynamicConfigs returns configs whose Tags contain `tag`. Empty tag returns input unchanged.
func FilterDynamicConfigs(configs []DynamicConfig, tag string) []DynamicConfig {
	if tag == "" {
		return configs
	}
	filtered := make([]DynamicConfig, 0, len(configs))
	for _, c := range configs {
		if slices.Contains(c.Tags, tag) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// ============================================================================
// HTTP helpers
// ============================================================================

// makePagedRequest issues a GET with ?limit=100&page=N for the page-number-paginated endpoints.
func (c *Client) makePagedRequest(ctx context.Context, reqURL string, page int) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	q := url.Values{}
	q.Set("limit", strconv.Itoa(pageSize))
	q.Set("page", strconv.Itoa(page))
	req.URL.RawQuery = q.Encode()
	c.setStatsigHeaders(req)
	return httputil.DoWithRetry(ctx, c.httpClient, req, nil)
}

// makeUnpagedGetRequest issues a GET without pagination params. Used for
// /environments and /<kind>/<id>/overrides which return a single payload.
func (c *Client) makeUnpagedGetRequest(ctx context.Context, reqURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	c.setStatsigHeaders(req)
	return httputil.DoWithRetry(ctx, c.httpClient, req, nil)
}

// setStatsigHeaders applies the auth, version pin, and JSON content-type
// headers used by the gate / DC / env / override endpoints. The legacy metric
// path uses fetchMetricsPage and intentionally does not send the version pin
// to preserve existing behavior.
//
// Note: req.Close is intentionally NOT set — leaving it false lets net/http
// reuse the TCP connection across paged list calls (ListGates,
// ListDynamicConfigs), which is much cheaper than a fresh handshake per page
// for projects with hundreds of gates. The goaltender lambda set req.Close
// because of an unrelated EOF bug under its environment; we don't observe
// that here and prefer keep-alive.
func (c *Client) setStatsigHeaders(req *http.Request) {
	req.Header.Set("STATSIG-API-KEY", c.apiKey)
	req.Header.Set("STATSIG-API-VERSION", apiVersion)
	req.Header.Set("Content-Type", "application/json")
}
