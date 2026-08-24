package launchdarkly

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/httputil"
)

type experimentationSettingsResponse struct {
	RandomizationUnits []struct {
		RandomizationUnit string `json:"randomizationUnit"`
		Default           bool   `json:"default"`
	} `json:"randomizationUnits"`
}

// ListRegisteredAnalysisUnits returns the context kinds registered as
// randomization units in the project's experimentation settings.
//
// These are the only values LaunchDarkly accepts in a metric's analysisUnits: a
// create naming anything else is rejected with
// `Randomization unit "x" not found in project settings [...]`. The list is not
// derived from the project's context kinds, it is configured separately, so a
// context kind can exist and still be absent here.
func (c *Client) ListRegisteredAnalysisUnits(ctx context.Context) ([]string, error) {
	reqURL, err := url.JoinPath(c.apiBase, "api/v2/projects", c.projectKey, "experimentation-settings")
	if err != nil {
		return nil, fmt.Errorf("building experimentation-settings URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating experimentation-settings request: %w", err)
	}
	c.setAuthHeaders(req)

	respBody, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, nil)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, c.apiError("reading LD experimentation settings", statusCode, respBody)
	}

	var r experimentationSettingsResponse
	if err := json.Unmarshal(respBody, &r); err != nil {
		return nil, fmt.Errorf("parsing experimentation-settings response: %w (body: %s)",
			err, httputil.Truncate(string(respBody), 200))
	}

	units := make([]string, 0, len(r.RandomizationUnits))
	for _, u := range r.RandomizationUnits {
		if u.RandomizationUnit != "" {
			units = append(units, u.RandomizationUnit)
		}
	}
	return units, nil
}
