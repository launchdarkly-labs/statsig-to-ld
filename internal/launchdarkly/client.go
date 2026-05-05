// Package launchdarkly provides a client for the LaunchDarkly REST API.
package launchdarkly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/httputil"
)

// unitNotFoundRe matches the LD API 400 message that indicates a randomization
// unit on the metric does not exist as a context kind in the project, e.g.
// `Randomization unit "stableid" not found in project settings [user]`.
var unitNotFoundRe = regexp.MustCompile(`Randomization unit "([^"]+)" not found in project settings`)

const defaultAPIBase = "https://app.launchdarkly.com"

// ConflictError indicates the LD metric already exists (HTTP 409).
type ConflictError struct {
	Key string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("LD metric %q already exists (409 Conflict)", e.Key)
}

// IsConflict returns true if the error indicates an LD metric already exists.
func IsConflict(err error) bool {
	var target *ConflictError
	return errors.As(err, &target)
}

// Client is a LaunchDarkly REST API client.
type Client struct {
	apiKey     string
	projectKey string
	apiBase    string
	httpClient *http.Client
}

// NewClient creates a new LaunchDarkly REST API client.
// If baseURL is empty, the default LaunchDarkly API URL is used.
func NewClient(apiKey, projectKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultAPIBase
	}
	return &Client{
		apiKey:     apiKey,
		projectKey: projectKey,
		apiBase:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// CreateMetric creates a metric in LaunchDarkly. Returns a ConflictError if
// the metric already exists (409), enabling idempotent re-runs.
func (c *Client) CreateMetric(ctx context.Context, metric MetricPost) (*MetricResponse, error) {
	url := fmt.Sprintf("%s/api/v2/metrics/%s", c.apiBase, c.projectKey)

	body, err := json.Marshal(metric)
	if err != nil {
		return nil, fmt.Errorf("marshaling LD metric: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	// LD v2 API expects the raw API key in Authorization (no Bearer prefix)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	respBody, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, body)
	if err != nil {
		return nil, err
	}

	if statusCode == 409 {
		return nil, &ConflictError{Key: metric.Key}
	}

	if statusCode != 201 {
		var errResp struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &errResp)
		errMsg := errResp.Message
		if errMsg == "" {
			errMsg = string(respBody)
		}
		if len(errMsg) > 300 {
			errMsg = errMsg[:300] + "..."
		}
		hint := actionableHint(statusCode, errMsg)
		if hint != "" {
			return nil, fmt.Errorf("LD API returned HTTP %d: %s — %s", statusCode, errMsg, hint)
		}
		return nil, fmt.Errorf("LD API returned HTTP %d: %s", statusCode, errMsg)
	}

	var ldResp MetricResponse
	if err := json.Unmarshal(respBody, &ldResp); err != nil {
		return nil, fmt.Errorf("parsing LD response: %w", err)
	}

	return &ldResp, nil
}

// actionableHint returns a human-readable hint for known LD API error patterns
// so users can resolve the issue without digging through the LD API docs.
// Returns an empty string when no hint is available.
func actionableHint(statusCode int, errMsg string) string {
	if statusCode == 400 {
		if m := unitNotFoundRe.FindStringSubmatch(errMsg); m != nil {
			unit := m[1]
			return fmt.Sprintf(`re-run with --unit-type-mapping mapping %q to an existing LD context kind (e.g. {%q: "user"}), or add %q as a context kind under Project Settings → Contexts`, unit, unit, unit)
		}
	}
	return ""
}
