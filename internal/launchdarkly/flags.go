// LD flag types + methods for the `flag-import` command. Ported from
// launchdarkly/goaltender/lambda_handlers/flag_import_worker/ldclient.go
// (PRs #825, #828, #829). Lambda-specific scaffolding (finstrument tracing,
// slog) is stripped; HTTP retry is delegated to internal/httputil for
// consistency with the metric path.
package launchdarkly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/httputil"
)

// Flag is the LD-flag request body for POST /api/v2/flags/{proj}.
type Flag struct {
	Defaults     Defaults    `json:"defaults,omitempty"`
	Description  string      `json:"description"`
	Key          string      `json:"key"`
	MaintainerID string      `json:"maintainerId,omitempty"`
	Name         string      `json:"name"`
	Tags         []string    `json:"tags"`
	Temporary    bool        `json:"temporary"`
	Variations   []Variation `json:"variations"`
}

// Defaults pairs an LD on/off variation pair by index into Variations.
type Defaults struct {
	OnVariation  int `json:"onVariation"`
	OffVariation int `json:"offVariation"`
}

// Variation is one LD flag variation.
type Variation struct {
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`
	Value       any    `json:"value"`
}

// FailedFlag carries per-flag failure / info notes for the migration report.
// Reused for both real failures and informational warnings to keep the report
// shape simple.
type FailedFlag struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// Environment is the subset of /api/v2/projects/{proj}/environments fields the
// flag importer cares about. The LD API returns many more fields (sdkKey,
// mobileKey, defaultTtl, etc.) which we ignore.
type Environment struct {
	Key   string   `json:"key"`
	Name  string   `json:"name"`
	Color string   `json:"color"`
	Tags  []string `json:"tags,omitempty"`
}

// JSONPatchOp is one RFC 6902 JSON Patch operation. Used to PATCH a flag's
// per-environment configuration without resending the full flag body.
type JSONPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// FlagConflictError indicates the LD flag already exists (HTTP 409). Mirrors
// ConflictError on the metric path so callers can use a single idempotency
// pattern.
type FlagConflictError struct {
	Key string
}

func (e *FlagConflictError) Error() string {
	return fmt.Sprintf("LD flag %q already exists (409 Conflict)", e.Key)
}

// IsFlagConflict returns true if the error indicates the flag already exists.
func IsFlagConflict(err error) bool {
	var target *FlagConflictError
	return errors.As(err, &target)
}

// Sentinel errors for environment creation. Callers branch on these to handle
// the 409 (someone else created it; refetch + reuse) and 403 (token lacks env
// write permission; degrade rule import for that env) cases.
var (
	ErrEnvironmentExists    = errors.New("LD environment already exists")
	ErrEnvironmentForbidden = errors.New("LD environment create forbidden")
)

// listFlagsResponse models the paginated GET /api/v2/flags/{proj} response.
type listFlagsResponse struct {
	Items      []Flag `json:"items"`
	TotalCount int    `json:"totalCount"`
}

// listEnvironmentsResponse models the paginated GET response for environments.
type listEnvironmentsResponse struct {
	Items []Environment `json:"items"`
	Links struct {
		Next struct {
			Href string `json:"href"`
		} `json:"next"`
	} `json:"_links"`
}

// ListFlags fetches all flags in the project, paging by offset until the API
// returns an empty page. Used to dedup new imports against pre-existing flags.
func (c *Client) ListFlags(ctx context.Context) ([]Flag, error) {
	endpoint := fmt.Sprintf("%s/api/v2/flags/%s", c.apiBase, c.projectKey)
	out := make([]Flag, 0)
	limit := 50
	offset := 0
	for {
		q := url.Values{}
		q.Add("limit", fmt.Sprintf("%d", limit))
		q.Add("offset", fmt.Sprintf("%d", offset))
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?"+q.Encode(), nil)
		if err != nil {
			return out, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Authorization", c.apiKey)
		req.Header.Set("Content-Type", "application/json")

		body, status, err := httputil.DoWithRetry(ctx, c.httpClient, req, nil)
		if err != nil {
			return out, err
		}
		if status != http.StatusOK {
			return out, fmt.Errorf("listing LD flags: HTTP %d: %s", status, httputil.Truncate(string(body), 300))
		}
		var r listFlagsResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return out, fmt.Errorf("parsing flags response: %w", err)
		}
		if len(r.Items) == 0 {
			return out, nil
		}
		out = append(out, r.Items...)
		offset += limit
	}
}

// CreateFlag posts a single flag to LD. Returns a FlagConflictError on 409 so
// the caller can record the flag as already existing.
func (c *Client) CreateFlag(ctx context.Context, flag Flag) (*Flag, error) {
	endpoint := fmt.Sprintf("%s/api/v2/flags/%s", c.apiBase, c.projectKey)

	body, err := json.Marshal(flag)
	if err != nil {
		return nil, fmt.Errorf("marshaling LD flag: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := httputil.DoWithRetry(ctx, c.httpClient, req, body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusConflict {
		return nil, &FlagConflictError{Key: flag.Key}
	}
	if status != http.StatusCreated && status != http.StatusOK {
		var errResp struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &errResp)
		msg := errResp.Message
		if msg == "" {
			msg = string(respBody)
		}
		return nil, fmt.Errorf("creating LD flag %q: HTTP %d: %s", flag.Key, status, httputil.Truncate(msg, 300))
	}
	var created Flag
	if err := json.Unmarshal(respBody, &created); err != nil {
		// Non-fatal: the LD response shape is richer than our Flag struct,
		// fields we don't model are ignored. If unmarshal fails entirely,
		// callers usually only need to know the flag was created.
		return &flag, nil
	}
	return &created, nil
}

// ListEnvironments fetches all environments in the LD project, following the
// _links.next pagination cursor. The LD pagination URL returned in _links.next
// is server-absolute (e.g. /api/v2/projects/foo/environments?offset=20).
func (c *Client) ListEnvironments(ctx context.Context) ([]Environment, error) {
	endpoint := fmt.Sprintf("%s/api/v2/projects/%s/environments", c.apiBase, c.projectKey)
	envs := make([]Environment, 0)
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Authorization", c.apiKey)
		req.Header.Set("Content-Type", "application/json")

		body, status, err := httputil.DoWithRetry(ctx, c.httpClient, req, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("listing LD environments: HTTP %d: %s", status, httputil.Truncate(string(body), 300))
		}
		var r listEnvironmentsResponse
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("parsing environments response: %w", err)
		}
		envs = append(envs, r.Items...)
		if r.Links.Next.Href == "" {
			return envs, nil
		}
		// The next href is a server-absolute URL reference (path + query, e.g.
		// "/api/v2/projects/foo/environments?offset=20"). Use ResolveReference
		// so the query string survives the join — url.JoinPath would percent-
		// encode the '?' into the path.
		base, parseErr := url.Parse(c.apiBase)
		if parseErr != nil {
			return nil, parseErr
		}
		ref, parseErr := url.Parse(r.Links.Next.Href)
		if parseErr != nil {
			return nil, parseErr
		}
		endpoint = base.ResolveReference(ref).String()
	}
}

// CreateEnvironment creates a new environment in the LD project. Returns the
// created environment on success. Returns ErrEnvironmentExists on 409 and
// ErrEnvironmentForbidden on 403 so callers can branch on those cases.
func (c *Client) CreateEnvironment(ctx context.Context, env Environment) (Environment, error) {
	endpoint := fmt.Sprintf("%s/api/v2/projects/%s/environments", c.apiBase, c.projectKey)

	body, err := json.Marshal(env)
	if err != nil {
		return Environment{}, fmt.Errorf("marshaling environment: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return Environment{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := httputil.DoWithRetry(ctx, c.httpClient, req, body)
	if err != nil {
		return Environment{}, err
	}
	switch status {
	case http.StatusCreated, http.StatusOK:
		var created Environment
		if jsonErr := json.Unmarshal(respBody, &created); jsonErr != nil {
			return Environment{}, jsonErr
		}
		return created, nil
	case http.StatusConflict:
		return Environment{}, ErrEnvironmentExists
	case http.StatusForbidden, http.StatusUnauthorized:
		return Environment{}, ErrEnvironmentForbidden
	default:
		return Environment{}, fmt.Errorf("creating LD environment %q: HTTP %d: %s", env.Key, status, httputil.Truncate(string(respBody), 300))
	}
}

// PatchFlag applies a JSON Patch (RFC 6902) operation array to a flag. Used
// to set per-environment rules, targets, fallthrough, etc. after the flag
// shell has been created.
func (c *Client) PatchFlag(ctx context.Context, flagKey string, ops []JSONPatchOp) error {
	if len(ops) == 0 {
		return nil
	}
	endpoint := fmt.Sprintf("%s/api/v2/flags/%s/%s", c.apiBase, c.projectKey, flagKey)

	body, err := json.Marshal(ops)
	if err != nil {
		return fmt.Errorf("marshaling patch ops: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := httputil.DoWithRetry(ctx, c.httpClient, req, body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("patching LD flag %q: HTTP %d: %s", flagKey, status, httputil.Truncate(string(respBody), 300))
	}
	return nil
}

// EscapeJSONPointer escapes a string for use as a single JSON Pointer (RFC 6901)
// reference token. Per the spec '~' must be encoded as '~0' and '/' as '~1',
// applied in that order so '~1' in the input doesn't become '/' after escaping
// '~' first. Used when constructing patch paths like
// /environments/<envKey>/rules where the env key may contain those characters.
func EscapeJSONPointer(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '~':
			out = append(out, '~', '0')
		case '/':
			out = append(out, '~', '1')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
