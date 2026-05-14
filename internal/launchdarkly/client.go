// Package launchdarkly provides a client for the LaunchDarkly REST API.
package launchdarkly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/httputil"
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

// Sentinel errors returned by CreateEnvironment so callers can branch on the
// 409 (someone else created it; refetch + reuse) and 403/401 (token lacks env
// write permission; degrade rule import for that env) cases.
var (
	ErrEnvironmentExists    = errors.New("LD environment already exists")
	ErrEnvironmentForbidden = errors.New("LD environment create forbidden")
)

// flagListPageSize is the per-page size used when listing flags. The LD API
// caps this at 50.
const flagListPageSize = 50

// Client is a LaunchDarkly REST API client.
type Client struct {
	apiKey     string
	projectKey string
	apiBase    string
	httpClient *http.Client

	// EnvironmentKey is set by the warehouse command for environment-scoped operations.
	// The convert command does not use it.
	EnvironmentKey string
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

// ============================================================================
// Flag endpoints (offset/limit pagination)
// ============================================================================

// ListAllFlags fetches every flag in the project, paginating internally via
// offset/limit. When tagFilter is non-empty, the LD API filter is applied
// server-side as `tags:<tag>`.
func (c *Client) ListAllFlags(ctx context.Context, tagFilter string) ([]Flag, error) {
	all := make([]Flag, 0)
	offset := 0
	for {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(flagListPageSize))
		q.Set("offset", strconv.Itoa(offset))
		if tagFilter != "" {
			q.Set("filter", "tags:"+tagFilter)
		}

		reqURL := fmt.Sprintf("%s/api/v2/flags/%s?%s", c.apiBase, c.projectKey, q.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("creating list-flags request: %w", err)
		}
		c.setAuthHeaders(req)

		respBody, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, nil)
		if err != nil {
			return nil, err
		}
		if statusCode != http.StatusOK {
			return nil, c.apiError("listing LD flags", statusCode, respBody)
		}

		var r listFlagsResponse
		if err := json.Unmarshal(respBody, &r); err != nil {
			return nil, fmt.Errorf("parsing list-flags response: %w (body: %s)", err, httputil.Truncate(string(respBody), 200))
		}

		all = append(all, r.Items...)
		// A short page means we hit the end. LD returns exactly `limit` items
		// until the final page, which is < limit (often 0). Terminate on
		// either case in one check.
		if len(r.Items) < flagListPageSize {
			return all, nil
		}
		offset += flagListPageSize
	}
}

// CreateFlag creates a single flag shell. Returns a ConflictError on 409 so
// callers can dedupe idempotently — matching CreateMetric semantics.
func (c *Client) CreateFlag(ctx context.Context, flag Flag) (*Flag, error) {
	body, err := json.Marshal(flag)
	if err != nil {
		return nil, fmt.Errorf("marshaling flag: %w", err)
	}

	reqURL := fmt.Sprintf("%s/api/v2/flags/%s", c.apiBase, c.projectKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating create-flag request: %w", err)
	}
	c.setAuthHeaders(req)

	respBody, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, body)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusConflict {
		return nil, &ConflictError{Key: flag.Key}
	}
	// LD documents only 201 Created as the success status for flag creation.
	// Treat any other 2xx (including 200) as an unexpected response and
	// surface it via apiError rather than silently accepting it.
	if statusCode != http.StatusCreated {
		return nil, c.apiError(fmt.Sprintf("creating LD flag %s", flag.Key), statusCode, respBody)
	}

	var created Flag
	if err := json.Unmarshal(respBody, &created); err != nil {
		return nil, fmt.Errorf("parsing create-flag response: %w (body: %s)", err, httputil.Truncate(string(respBody), 200))
	}
	return &created, nil
}

// PatchFlag applies a JSON Patch (RFC 6902) operation list to a flag's
// configuration. Used to set per-environment rules, targets, fallthrough, etc.
// after a flag has been created (CreateFlag creates flag shells without env
// config). A nil/empty ops list is a no-op (returns nil without an HTTP call).
func (c *Client) PatchFlag(ctx context.Context, flagKey string, ops []JSONPatchOp) error {
	if len(ops) == 0 {
		return nil
	}

	body, err := json.Marshal(ops)
	if err != nil {
		return fmt.Errorf("marshaling JSON patch: %w", err)
	}

	reqURL := fmt.Sprintf("%s/api/v2/flags/%s/%s", c.apiBase, c.projectKey, flagKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating patch-flag request: %w", err)
	}
	c.setAuthHeaders(req)

	respBody, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, body)
	if err != nil {
		return err
	}
	if statusCode != http.StatusOK {
		return c.apiError(fmt.Sprintf("patching LD flag %s", flagKey), statusCode, respBody)
	}
	return nil
}

// ============================================================================
// Environment endpoints (_links.next cursor pagination)
// ============================================================================

// ListEnvironments fetches all environments in the LD project, following the
// _links.next cursor.
func (c *Client) ListEnvironments(ctx context.Context) ([]Environment, error) {
	base, err := url.Parse(c.apiBase)
	if err != nil {
		return nil, fmt.Errorf("parsing apiBase: %w", err)
	}

	reqURL, err := url.JoinPath(c.apiBase, "api/v2/projects", c.projectKey, "environments")
	if err != nil {
		return nil, fmt.Errorf("building environments URL: %w", err)
	}

	envs := make([]Environment, 0)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("creating list-environments request: %w", err)
		}
		c.setAuthHeaders(req)

		respBody, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, nil)
		if err != nil {
			return nil, err
		}
		if statusCode != http.StatusOK {
			return nil, c.apiError("listing LD environments", statusCode, respBody)
		}

		var r listEnvironmentsResponse
		if err := json.Unmarshal(respBody, &r); err != nil {
			return nil, fmt.Errorf("parsing list-environments response: %w (body: %s)", err, httputil.Truncate(string(respBody), 200))
		}
		envs = append(envs, r.Items...)

		if r.Links.Next.Href == "" {
			return envs, nil
		}

		// Resolve the next href against the apiBase. The href is a
		// server-absolute reference like "/api/v2/projects/foo/environments?offset=20";
		// url.JoinPath would percent-encode the '?'. ResolveReference handles it.
		ref, parseErr := url.Parse(r.Links.Next.Href)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing _links.next.href: %w", parseErr)
		}
		// Reject hrefs that point to a different host. The LD API only ever
		// returns relative paths here; an absolute href to another host would
		// be a server-side bug at best and an SSRF redirect at worst. Bound
		// the next request to the same origin as apiBase regardless.
		if ref.Host != "" && ref.Host != base.Host {
			return nil, fmt.Errorf("rejecting _links.next.href host %q: does not match apiBase host %q", ref.Host, base.Host)
		}
		reqURL = base.ResolveReference(ref).String()
	}
}

// CreateEnvironment creates a new environment in the LD project. Returns
// ErrEnvironmentExists on 409 and ErrEnvironmentForbidden on 403/401 so the
// env-reconciler in PR 6 can branch on those cases.
func (c *Client) CreateEnvironment(ctx context.Context, env Environment) (*Environment, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshaling environment: %w", err)
	}

	reqURL, err := url.JoinPath(c.apiBase, "api/v2/projects", c.projectKey, "environments")
	if err != nil {
		return nil, fmt.Errorf("building environments URL: %w", err)
	}

	// Single attempt — httputil.DoWithRetry already handles transient 429/5xx
	// retry internally with backoff. An outer retry loop here was dead code:
	// every branch below returns immediately, so any iteration past the first
	// is unreachable.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating create-environment request: %w", err)
	}
	c.setAuthHeaders(req)

	respBody, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, body)
	if err != nil {
		return nil, err
	}
	switch statusCode {
	case http.StatusCreated, http.StatusOK:
		var created Environment
		if jsonErr := json.Unmarshal(respBody, &created); jsonErr != nil {
			return nil, fmt.Errorf("parsing create-environment response: %w (body: %s)", jsonErr, httputil.Truncate(string(respBody), 200))
		}
		return &created, nil
	case http.StatusConflict:
		return nil, ErrEnvironmentExists
	case http.StatusForbidden, http.StatusUnauthorized:
		return nil, ErrEnvironmentForbidden
	default:
		return nil, c.apiError(fmt.Sprintf("creating LD environment %s", env.Key), statusCode, respBody)
	}
}

// setAuthHeaders applies the LD API auth + content-type headers.
func (c *Client) setAuthHeaders(req *http.Request) {
	req.Close = true
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
}

// apiError builds a standard LD API error including an actionable hint when
// the status code matches a known pattern.
func (c *Client) apiError(action string, statusCode int, body []byte) error {
	var errResp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &errResp)
	msg := errResp.Message
	if msg == "" {
		msg = string(body)
	}
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	if hint := actionableHint(statusCode, msg); hint != "" {
		return fmt.Errorf("%s: HTTP %d: %s — %s", action, statusCode, msg, hint)
	}
	return fmt.Errorf("%s: HTTP %d: %s", action, statusCode, msg)
}

// actionableHint returns a human-readable hint for known LD API error patterns
// so users can resolve the issue without digging through the LD API docs.
// Returns an empty string when no hint is available.
func actionableHint(statusCode int, errMsg string) string {
	switch statusCode {
	case 400:
		if m := unitNotFoundRe.FindStringSubmatch(errMsg); m != nil {
			unit := m[1]
			return fmt.Sprintf(`re-run with --unit-type-mapping <file> where <file> is a JSON file containing {%q: "user"}, or add %q as a context kind under Project Settings → Contexts`, unit, unit)
		}
	case 401:
		return "verify your LD API access token is valid and not expired — check Account Settings → Authorization in the LaunchDarkly UI"
	case 403:
		return "your API access token does not have write permission for this project — ensure it has a Writer role or a custom role that includes metric creation"
	case 404:
		return "check that --ld-project matches an existing project key in your LaunchDarkly account"
	}
	return ""
}
