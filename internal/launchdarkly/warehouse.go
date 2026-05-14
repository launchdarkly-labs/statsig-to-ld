package launchdarkly

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/httputil"
	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
)

// requestJSON performs an HTTP request and returns the status code and parsed JSON body.
func (c *Client) requestJSON(ctx context.Context, method, path string, body any) (int, map[string]any, error) {
	reqURL := c.apiBase + path

	var bodyBytes []byte
	var bodyReader *bytes.Reader
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	var req *http.Request
	var err error
	if bodyReader != nil {
		req, err = http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, reqURL, nil)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("LD-API-Version", "beta")

	respBody, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, bodyBytes)
	if err != nil {
		return 0, nil, err
	}

	var result map[string]any
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &result)
	}
	return statusCode, result, nil
}

// -- Data export destinations --

// ListDestinations returns data export destinations for an environment.
func (c *Client) ListDestinations(ctx context.Context) []map[string]any {
	status, body, _ := c.requestJSON(ctx, "GET", fmt.Sprintf("/api/v2/destinations/%s/%s", c.projectKey, c.EnvironmentKey), nil)
	if status == 200 && body != nil {
		return j.ExtractItemsList(body)
	}
	return nil
}

// CreateDestination creates a data export destination.
func (c *Client) CreateDestination(ctx context.Context, payload map[string]any) (map[string]any, error) {
	status, body, err := c.requestJSON(ctx, "POST", fmt.Sprintf("/api/v2/destinations/%s/%s", c.projectKey, c.EnvironmentKey), payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("failed to create data export destination: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// GenerateDataExportSetup generates a data export setup script.
func (c *Client) GenerateDataExportSetup(ctx context.Context, kind string, payload map[string]any) (map[string]any, error) {
	path := fmt.Sprintf("/api/v2/destinations/projects/%s/environments/%s/kinds/%s/setup", c.projectKey, c.EnvironmentKey, kind)
	status, body, err := c.requestJSON(ctx, "POST", path, payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("failed to generate data export setup: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// CompleteDataExportSetup completes the data export setup.
func (c *Client) CompleteDataExportSetup(ctx context.Context, kind string, payload map[string]any) (map[string]any, error) {
	path := fmt.Sprintf("/api/v2/destinations/projects/%s/environments/%s/kinds/%s/complete-setup", c.projectKey, c.EnvironmentKey, kind)
	status, body, err := c.requestJSON(ctx, "POST", path, payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("failed to complete data export setup: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// -- Integration configurations --

// ListIntegrationConfigs lists integration configurations for a given key.
func (c *Client) ListIntegrationConfigs(ctx context.Context, integrationKey string) []map[string]any {
	status, body, _ := c.requestJSON(ctx, "GET", fmt.Sprintf("/api/v2/integration-configurations/keys/%s", integrationKey), nil)
	if status == 200 && body != nil {
		items := j.ExtractItemsList(body)
		if len(items) > 0 {
			return items
		}
		if j.GetStr(body, "id") != "" {
			return []map[string]any{body}
		}
	}
	return nil
}

// CreateIntegrationConfig creates an integration configuration.
func (c *Client) CreateIntegrationConfig(ctx context.Context, integrationKey string, payload map[string]any) (map[string]any, error) {
	status, body, err := c.requestJSON(ctx, "POST", fmt.Sprintf("/api/v2/integration-configurations/keys/%s", integrationKey), payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("failed to create integration config: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// GenerateSnowflakeSetup generates a Snowflake experimentation setup script.
func (c *Client) GenerateSnowflakeSetup(ctx context.Context, payload map[string]any) (map[string]any, error) {
	status, body, err := c.requestJSON(ctx, "POST", "/api/v2/integration-configurations/keys/snowflake-experimentation/setup", payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("failed to generate Snowflake setup: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// GenerateRedshiftSetup generates a Redshift experimentation setup script.
func (c *Client) GenerateRedshiftSetup(ctx context.Context, payload map[string]any) (map[string]any, error) {
	status, body, err := c.requestJSON(ctx, "POST", "/api/v2/integration-configurations/keys/redshift-experimentation/setup", payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("failed to generate Redshift setup: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// CompleteSetup completes an integration configuration setup.
func (c *Client) CompleteSetup(ctx context.Context, configID string) (map[string]any, error) {
	status, body, err := c.requestJSON(ctx, "POST", fmt.Sprintf("/api/v2/integration-configurations/%s/complete-setup", configID), nil)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("failed to complete setup: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// -- Metric data sources --

// PreviewDataSource runs a query preview to discover warehouse columns.
func (c *Client) PreviewDataSource(ctx context.Context, integrationConfigID, sqlQuery string) (map[string]any, error) {
	path := fmt.Sprintf("/internal/projects/%s/warehouse-native-integrations/%s/metric-data-source-preview?sqlQuery=%s",
		c.projectKey, integrationConfigID, url.QueryEscape(sqlQuery))
	status, body, err := c.requestJSON(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("failed to preview data source: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// ListMetricDataSources lists metric data sources for a project.
func (c *Client) ListMetricDataSources(ctx context.Context) []map[string]any {
	status, body, _ := c.requestJSON(ctx, "GET", fmt.Sprintf("/internal/projects/%s/metric-data-sources", c.projectKey), nil)
	if status == 200 && body != nil {
		return j.ExtractItemsList(body)
	}
	return nil
}

// CreateMetricDataSource creates a metric data source.
func (c *Client) CreateMetricDataSource(ctx context.Context, payload map[string]any) (map[string]any, error) {
	status, body, err := c.requestJSON(ctx, "POST", fmt.Sprintf("/internal/projects/%s/metric-data-sources", c.projectKey), payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("failed to create metric data source: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// -- Metrics (raw, for warehouse flow) --

// ListMetricsRaw lists all metrics as raw maps (paginated).
func (c *Client) ListMetricsRaw(ctx context.Context) []map[string]any {
	var all []map[string]any
	status, body, _ := c.requestJSON(ctx, "GET", fmt.Sprintf("/api/v2/metrics/%s?limit=50", c.projectKey), nil)
	if status == 200 && body != nil {
		all = append(all, j.ExtractItemsList(body)...)
		for {
			links := j.GetMap(body, "_links")
			next := j.GetMap(links, "next")
			if next == nil {
				break
			}
			href := j.GetStr(next, "href")
			if href == "" {
				break
			}
			status, body, _ = c.requestJSON(ctx, "GET", href, nil)
			if status != 200 {
				break
			}
			all = append(all, j.ExtractItemsList(body)...)
		}
	}
	return all
}

// CreateMetricRaw creates a metric using a raw map payload.
func (c *Client) CreateMetricRaw(ctx context.Context, payload map[string]any) (map[string]any, error) {
	status, body, err := c.requestJSON(ctx, "POST", fmt.Sprintf("/api/v2/metrics/%s", c.projectKey), payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("failed to create metric: %d\n%s", status, j.ToJSON(body))
	}
	return body, nil
}

// -- Verification --

// GetProject fetches project details.
func (c *Client) GetProject(ctx context.Context) map[string]any {
	status, body, _ := c.requestJSON(ctx, "GET", fmt.Sprintf("/api/v2/projects/%s", c.projectKey), nil)
	if status == 200 {
		return body
	}
	return nil
}

// GetEnvironment fetches environment details.
func (c *Client) GetEnvironment(ctx context.Context) map[string]any {
	status, body, _ := c.requestJSON(ctx, "GET", fmt.Sprintf("/api/v2/projects/%s/environments/%s", c.projectKey, c.EnvironmentKey), nil)
	if status == 200 {
		return body
	}
	return nil
}

// CheckAPIKeyAccess verifies the API key has access to the project.
func (c *Client) CheckAPIKeyAccess(ctx context.Context) (bool, string) {
	status, _, _ := c.requestJSON(ctx, "GET", fmt.Sprintf("/api/v2/metrics/%s?limit=1", c.projectKey), nil)
	switch status {
	case 200:
		return true, "API key has read access to the project"
	case 403:
		return false, "API key does not have access to this project."
	case 401:
		return false, "API key is invalid or expired."
	case 404:
		return false, fmt.Sprintf("Project '%s' not found.", c.projectKey)
	default:
		return false, fmt.Sprintf("Unexpected status %d", status)
	}
}

// CheckAPIKeyRole returns the role and name of the API key.
func (c *Client) CheckAPIKeyRole(ctx context.Context) (string, string) {
	status, body, _ := c.requestJSON(ctx, "GET", "/api/v2/tokens", nil)
	if status == 200 && body != nil {
		items := j.ExtractItemsList(body)
		for _, token := range items {
			role := j.GetStr(token, "role")
			name := j.GetStr(token, "name")
			if role != "" {
				return role, name
			}
			if j.GetSlice(token, "inlineRole") != nil || j.GetSlice(token, "customRoleIds") != nil {
				return "custom", name
			}
		}
	}
	status, body, _ = c.requestJSON(ctx, "GET", "/api/v2/caller-identity", nil)
	if status == 200 && body != nil {
		return j.GetStr(body, "role"), j.GetStr(body, "name")
	}
	return "", ""
}
