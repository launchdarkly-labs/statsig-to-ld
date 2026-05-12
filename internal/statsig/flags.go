// Package statsig: types and list methods for feature gates, dynamic configs,
// environments, and overrides — the inputs to the `flag-import` command.
//
// Ported from launchdarkly/goaltender/lambda_handlers/flag_import_worker/statsig.go
// (PRs #825, #828, #829). Lambda-specific scaffolding (finstrument tracing,
// slog, 401-triage instrumentation) is stripped; HTTP retry is delegated to
// internal/httputil for consistency with the metric path.
package statsig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/httputil"
)

const (
	// GateTypeTemporary is the Statsig gate.type value for gates marked as
	// temporary in the Statsig UI. Used to set LD flag.Temporary on import.
	GateTypeTemporary = "TEMPORARY"

	statsigAPIVersion = "20240601"
	statsigPageSize   = 100
	// statsigMaxPages caps pagination at 100,000 items (1000 pages × 100/page)
	// to bound memory.
	statsigMaxPages = 1000
)

// Gate represents a Feature Gate from /console/v1/gates. The list response
// already includes rules + nested conditions; no per-gate GET is needed for
// targeting import.
type Gate struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	IsEnabled   bool       `json:"isEnabled"`
	Tags        []string   `json:"tags"`
	Type        string     `json:"type"`
	IDType      string     `json:"idType"`
	Rules       []GateRule `json:"rules,omitempty"`
}

// GateRule is one gate's targeting rule. Environments is nullable: nil means
// "applies to all environments" per the Statsig docs. PassPercentage is
// nullable too (rare; treated as 0 by transformation).
type GateRule struct {
	ID             string      `json:"id,omitempty"`
	Name           string      `json:"name"`
	PassPercentage *float64    `json:"passPercentage,omitempty"`
	Conditions     []Condition `json:"conditions"`
	// Environments is nil when the field is absent OR null in the API response.
	// When present and an array (even empty), the rule scopes to those Statsig
	// envs. Distinguishing null vs [] requires the pointer.
	Environments *[]string `json:"environments,omitempty"`
}

// Condition is one clause within a targeting rule. CustomID is set for
// unit_id conditions (the Statsig non-userID unit). Field is set for
// custom_field conditions.
type Condition struct {
	Type        string `json:"type"`
	Operator    string `json:"operator,omitempty"`
	TargetValue any    `json:"targetValue,omitempty"`
	Field       string `json:"field,omitempty"`
	CustomID    string `json:"customID,omitempty"`
}

// DynamicConfig represents a Dynamic Config from /console/v1/dynamic_configs.
type DynamicConfig struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	IsEnabled    bool            `json:"isEnabled"`
	Tags         []string        `json:"tags"`
	DefaultValue json.RawMessage `json:"defaultValue"`
	Rules        []DCRule        `json:"rules"`
}

// DCRule is one targeting rule on a Dynamic Config.
type DCRule struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	PassPercentage float64         `json:"passPercentage"`
	ReturnValue    json.RawMessage `json:"returnValue"`
	// Variants is present on newer Statsig Dynamic Configs and defines the
	// canonical set of return values for the config. When present, import
	// treats variants as the source of LD variations. Older configs without
	// variants fall back to the rule-level returnValue / dc-level defaultValue
	// path.
	Variants []DCVariant `json:"variants,omitempty"`

	// Conditions and Environments mirror the gate-rule fields and are used for
	// targeting-rule import. Older shell-only DC variation building doesn't
	// read these.
	Conditions   []Condition `json:"conditions,omitempty"`
	Environments *[]string   `json:"environments,omitempty"`
}

// DCVariant is one named return value within a Dynamic Config rule.
// PassPercentage is set on newer multi-variant DCs to weight the rollout.
type DCVariant struct {
	Name           string          `json:"name"`
	ReturnValue    json.RawMessage `json:"returnValue"`
	PassPercentage float64         `json:"passPercentage,omitempty"`
}

// Environment is one environment in a Statsig project. Returned from
// /console/v1/environments. Used by the env-reconciler to enumerate the
// universe of Statsig envs to map / auto-create on the LD side.
type Environment struct {
	Name           string `json:"name"`
	IsProduction   bool   `json:"isProduction"`
	RequiresReview bool   `json:"requiresReview"`
}

// Override is one env-scoped override entry. Environment is nil when
// the override applies to all envs. PassingIDs map to variation 0 (gates =
// true, DCs = first/passing variant); FailingIDs map to variation 1 (gates =
// false, DCs = default). UnitID is "userID" for user-keyed overrides; other
// unit IDs are accepted but treated as the "user" context kind in v1.
type Override struct {
	Environment *string  `json:"environment"`
	UnitID      string   `json:"unitID"`
	PassingIDs  []string `json:"passingIDs"`
	FailingIDs  []string `json:"failingIDs"`
}

type pagination struct {
	ItemsPerPage int    `json:"itemsPerPage"`
	PageNumber   int    `json:"pageNumber"`
	TotalItems   int    `json:"totalItems"`
	NextPage     string `json:"nextPage"`
	PreviousPage string `json:"previousPage"`
}

type listGatesResponse struct {
	Message    string     `json:"message"`
	Data       []Gate     `json:"data"`
	Pagination pagination `json:"pagination"`
}

type listDynamicConfigsResponse struct {
	Message    string          `json:"message"`
	Data       []DynamicConfig `json:"data"`
	Pagination pagination      `json:"pagination"`
}

// ListGates fetches all Feature Gates with page-number pagination.
func (c *Client) ListGates(ctx context.Context) ([]Gate, error) {
	gates := make([]Gate, 0)
	for page := 1; page <= statsigMaxPages; page++ {
		body, status, err := c.pagedGet(ctx, "/gates", page)
		if err != nil {
			return gates, err
		}
		if status != http.StatusOK {
			return gates, fmt.Errorf("listing Statsig gates: HTTP %d: %s", status, httputil.Truncate(string(body), 300))
		}
		var r listGatesResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return gates, fmt.Errorf("parsing gates response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
		}
		gates = append(gates, r.Data...)
		if r.Pagination.NextPage == "" || len(r.Data) < statsigPageSize {
			return gates, nil
		}
	}
	return gates, fmt.Errorf("statsig gates pagination exceeded %d pages", statsigMaxPages)
}

// ListDynamicConfigs fetches all Dynamic Configs with page-number pagination.
func (c *Client) ListDynamicConfigs(ctx context.Context) ([]DynamicConfig, error) {
	configs := make([]DynamicConfig, 0)
	for page := 1; page <= statsigMaxPages; page++ {
		body, status, err := c.pagedGet(ctx, "/dynamic_configs", page)
		if err != nil {
			return configs, err
		}
		if status != http.StatusOK {
			return configs, fmt.Errorf("listing Statsig dynamic configs: HTTP %d: %s", status, httputil.Truncate(string(body), 300))
		}
		var r listDynamicConfigsResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return configs, fmt.Errorf("parsing dynamic configs response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
		}
		configs = append(configs, r.Data...)
		if r.Pagination.NextPage == "" || len(r.Data) < statsigPageSize {
			return configs, nil
		}
	}
	return configs, fmt.Errorf("statsig dynamic configs pagination exceeded %d pages", statsigMaxPages)
}

// ListEnvironments fetches all environments configured for the Statsig project.
// Response shape: {"data": {"environments": [{"name":..., "isProduction":..., "requiresReview":...}, ...]}}
func (c *Client) ListEnvironments(ctx context.Context) ([]Environment, error) {
	body, status, err := c.unpagedGet(ctx, "/environments")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("listing Statsig environments: HTTP %d: %s", status, httputil.Truncate(string(body), 300))
	}
	var r struct {
		Data struct {
			Environments []Environment `json:"environments"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parsing environments response: %w", err)
	}
	return r.Data.Environments, nil
}

// GetGateOverrides fetches all env-scoped overrides for a gate. Returns nil
// on 404 (gate has no overrides — not an error).
func (c *Client) GetGateOverrides(ctx context.Context, gateID string) ([]Override, error) {
	return c.fetchOverrides(ctx, "/gates", gateID)
}

// GetDynamicConfigOverrides fetches all env-scoped overrides for a dynamic
// config. Same response shape as gate overrides; for multi-variant DCs this is
// lossy (the API only exposes binary pass/fail per user).
func (c *Client) GetDynamicConfigOverrides(ctx context.Context, configID string) ([]Override, error) {
	return c.fetchOverrides(ctx, "/dynamic_configs", configID)
}

func (c *Client) fetchOverrides(ctx context.Context, basePath, id string) ([]Override, error) {
	body, status, err := c.unpagedGet(ctx, basePath+"/"+url.PathEscape(id)+"/overrides")
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, fmt.Errorf("fetching Statsig overrides for %s: HTTP %d: %s", id, status, httputil.Truncate(string(body), 300))
	}
	var r struct {
		Data struct {
			EnvironmentOverrides []Override `json:"environmentOverrides"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parsing overrides response: %w", err)
	}
	return r.Data.EnvironmentOverrides, nil
}

// FilterGatesByTag returns gates whose Tags contain `tag`. Empty tag returns
// the input unchanged.
func FilterGatesByTag(gates []Gate, tag string) []Gate {
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

// FilterDynamicConfigsByTag returns configs whose Tags contain `tag`. Empty
// tag returns the input unchanged.
func FilterDynamicConfigsByTag(configs []DynamicConfig, tag string) []DynamicConfig {
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

// pagedGet issues a Console API GET with limit + page query params. Routes
// through httputil.DoWithRetry for consistent retry behavior with the metric
// path.
func (c *Client) pagedGet(ctx context.Context, path string, page int) ([]byte, int, error) {
	q := url.Values{}
	q.Add("limit", strconv.Itoa(statsigPageSize))
	q.Add("page", strconv.Itoa(page))
	reqURL := c.apiBase + path + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Close = true
	req.Header.Set("STATSIG-API-KEY", c.apiKey)
	req.Header.Set("STATSIG-API-VERSION", statsigAPIVersion)
	req.Header.Set("Content-Type", "application/json")

	return httputil.DoWithRetry(ctx, c.httpClient, req, nil)
}

// unpagedGet issues a Console API GET with no list-pagination params. Used for
// the environments and overrides endpoints which return a single payload.
func (c *Client) unpagedGet(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.apiBase+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Close = true
	req.Header.Set("STATSIG-API-KEY", c.apiKey)
	req.Header.Set("STATSIG-API-VERSION", statsigAPIVersion)
	req.Header.Set("Content-Type", "application/json")

	return httputil.DoWithRetry(ctx, c.httpClient, req, nil)
}
