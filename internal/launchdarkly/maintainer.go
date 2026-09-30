package launchdarkly

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/httputil"
)

// MaintainerNone is the --ld-maintainer value that opts out of setting a
// maintainer at all.
const MaintainerNone = "none"

// A member ID is 24 hex chars and an email contains "@", so the two flag forms
// are unambiguous.
var memberIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{24}$`)

// MaintainerSource records how a maintainer was decided, for reporting.
type MaintainerSource int

const (
	MaintainerUnset MaintainerSource = iota
	MaintainerFromToken
	MaintainerFromFlagID
	MaintainerFromFlagEmail
	MaintainerOptedOut
)

func (s MaintainerSource) String() string {
	switch s {
	case MaintainerFromToken:
		return "resolved from the API token"
	case MaintainerFromFlagID:
		return "--ld-maintainer member ID"
	case MaintainerFromFlagEmail:
		return "--ld-maintainer email"
	case MaintainerOptedOut:
		return "--ld-maintainer none"
	default:
		return "unset"
	}
}

// Maintainer is a resolved maintainer. MemberID is empty only when OptedOut.
type Maintainer struct {
	MemberID string
	Email    string
	Source   MaintainerSource
	OptedOut bool
}

// ValidateMaintainerFlag checks the value's shape without calling the API, so a
// typo fails at startup.
func ValidateMaintainerFlag(value string) error {
	v := strings.TrimSpace(value)
	if v == "" || strings.EqualFold(v, MaintainerNone) {
		return nil
	}
	if strings.Contains(v, "@") || memberIDPattern.MatchString(v) {
		return nil
	}
	return fmt.Errorf("invalid --ld-maintainer value %q: expected an email address, a 24-character member ID, or %q", value, MaintainerNone)
}

// ResolveMaintainer decides which member maintains the resources a run creates.
// With no flag value it uses the API token's member, which LD does itself for a
// personal token but not for a service token. Returns an error rather than an
// empty maintainer; callers treat that as fatal.
func (c *Client) ResolveMaintainer(ctx context.Context, flagValue string) (Maintainer, error) {
	v := strings.TrimSpace(flagValue)

	if strings.EqualFold(v, MaintainerNone) {
		return Maintainer{Source: MaintainerOptedOut, OptedOut: true}, nil
	}

	if strings.Contains(v, "@") {
		member, err := c.findMemberByEmail(ctx, v)
		if err != nil {
			return Maintainer{}, err
		}
		return Maintainer{MemberID: member.ID, Email: member.Email, Source: MaintainerFromFlagEmail}, nil
	}

	if v != "" {
		member, err := c.getMember(ctx, v)
		if err != nil {
			return Maintainer{}, fmt.Errorf("--ld-maintainer member ID %q could not be verified: %w", v, err)
		}
		return Maintainer{MemberID: member.ID, Email: member.Email, Source: MaintainerFromFlagID}, nil
	}

	identity, err := c.GetCallerIdentity(ctx)
	if err != nil {
		return Maintainer{}, fmt.Errorf("could not read the API token's identity to set a maintainer (%w). "+
			"Pass --ld-maintainer <email|member-id> to name one, or --ld-maintainer none to create resources without a maintainer", err)
	}
	if identity.MemberID == "" {
		return Maintainer{}, fmt.Errorf("the API token is not associated with a LaunchDarkly member, so no maintainer could be resolved. " +
			"Pass --ld-maintainer <email|member-id> to name one, or --ld-maintainer none to create resources without a maintainer")
	}

	member, err := c.getMember(ctx, identity.MemberID)
	if err != nil {
		return Maintainer{}, fmt.Errorf("the API token's member (%s) is not a current member of this account, so it cannot maintain resources (%w). "+
			"Pass --ld-maintainer <email|member-id> to name one, or --ld-maintainer none to create resources without a maintainer", identity.MemberID, err)
	}
	return Maintainer{MemberID: member.ID, Email: member.Email, Source: MaintainerFromToken}, nil
}

// CallerIdentity is the subset of GET /api/v2/caller-identity this tool uses.
type CallerIdentity struct {
	MemberID     string `json:"memberId"`
	ServiceToken bool   `json:"serviceToken"`
	TokenName    string `json:"tokenName"`
}

// GetCallerIdentity reports which identity the configured token represents.
func (c *Client) GetCallerIdentity(ctx context.Context) (CallerIdentity, error) {
	reqURL, err := url.JoinPath(c.apiBase, "api/v2/caller-identity")
	if err != nil {
		return CallerIdentity{}, fmt.Errorf("building caller-identity URL: %w", err)
	}
	body, _, err := c.getJSON(ctx, reqURL, "reading the LD caller identity")
	if err != nil {
		return CallerIdentity{}, err
	}
	var identity CallerIdentity
	if err := json.Unmarshal(body, &identity); err != nil {
		return CallerIdentity{}, fmt.Errorf("parsing caller-identity response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
	}
	return identity, nil
}

type member struct {
	ID    string `json:"_id"`
	Email string `json:"email"`
}

func (c *Client) getMember(ctx context.Context, memberID string) (member, error) {
	reqURL, err := url.JoinPath(c.apiBase, "api/v2/members", memberID)
	if err != nil {
		return member{}, fmt.Errorf("building member URL: %w", err)
	}
	body, status, err := c.getJSON(ctx, reqURL, "reading the LD account member")
	if err != nil {
		if status == http.StatusNotFound {
			return member{}, fmt.Errorf("no member %q exists on this LaunchDarkly account", memberID)
		}
		return member{}, err
	}
	var m member
	if err := json.Unmarshal(body, &m); err != nil {
		return member{}, fmt.Errorf("parsing member response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
	}
	if m.ID == "" {
		m.ID = memberID
	}
	return m, nil
}

// findMemberByEmail resolves an email to a member. LD's email filter is an
// exact, case-sensitive match, so a near miss returns no members at all.
func (c *Client) findMemberByEmail(ctx context.Context, email string) (member, error) {
	base, err := url.JoinPath(c.apiBase, "api/v2/members")
	if err != nil {
		return member{}, fmt.Errorf("building members URL: %w", err)
	}
	reqURL := base + "?filter=" + url.QueryEscape("email:"+email) + "&limit=5"

	body, _, err := c.getJSON(ctx, reqURL, "looking up the LD account member by email")
	if err != nil {
		return member{}, fmt.Errorf("could not look up --ld-maintainer email %q: %w", email, err)
	}
	var resp struct {
		Items []member `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return member{}, fmt.Errorf("parsing members response: %w (body: %s)", err, httputil.Truncate(string(body), 200))
	}

	switch len(resp.Items) {
	case 0:
		return member{}, fmt.Errorf("no LaunchDarkly member has the email %q. LaunchDarkly matches this address exactly, including case, "+
			"so check the spelling and capitalisation against the member list, or pass a 24-character member ID instead", email)
	case 1:
		return resp.Items[0], nil
	default:
		return member{}, fmt.Errorf("%d LaunchDarkly members matched the email %q; pass a 24-character member ID instead", len(resp.Items), email)
	}
}

// getJSON performs an authenticated GET. The status is returned alongside the
// error so callers can handle a 404 themselves: apiError's hints are written for
// project-scoped calls and misdescribe a member lookup.
func (c *Client) getJSON(ctx context.Context, reqURL, action string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request for %s: %w", action, err)
	}
	c.setAuthHeaders(req)

	body, statusCode, err := httputil.DoWithRetry(ctx, c.httpClient, req, nil)
	if err != nil {
		return nil, 0, err
	}
	if statusCode != http.StatusOK {
		return nil, statusCode, c.apiError(action, statusCode, body)
	}
	return body, statusCode, nil
}
