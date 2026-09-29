package launchdarkly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testMemberID    = "6917a463c1b64809c1124c34"
	testMemberEmail = "someone@example.com"
)

// maintainerServer answers the three endpoints maintainer resolution uses. Any
// handler set to nil returns 404, so a test can prove a path is not consulted.
type maintainerServer struct {
	callerIdentity http.HandlerFunc
	memberByID     http.HandlerFunc
	membersFilter  http.HandlerFunc
	paths          []string
}

func newMaintainerClient(t *testing.T, s *maintainerServer) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.paths = append(s.paths, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.URL.Path == "/api/v2/caller-identity" && s.callerIdentity != nil:
			s.callerIdentity(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/v2/members/") && s.memberByID != nil:
			s.memberByID(w, r)
		case r.URL.Path == "/api/v2/members" && s.membersFilter != nil:
			s.membersFilter(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return NewClient("api-test", "proj", srv.URL)
}

func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// "none" is the explicit opt-out: no maintainer, and no API calls to resolve one.
func TestResolveMaintainer_NoneOptsOut(t *testing.T) {
	s := &maintainerServer{}
	c := newMaintainerClient(t, s)

	res, err := c.ResolveMaintainer(context.Background(), "none")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MemberID != "" {
		t.Errorf("MemberID = %q, want empty", res.MemberID)
	}
	if !res.OptedOut {
		t.Error("OptedOut should be true for \"none\"")
	}
	if len(s.paths) != 0 {
		t.Errorf("no requests should be made for \"none\", got %v", s.paths)
	}
}

// An explicit member ID is validated against the project, so a typo fails before
// any metric is created rather than on every create.
func TestResolveMaintainer_ExplicitIDIsValidated(t *testing.T) {
	s := &maintainerServer{
		memberByID: jsonHandler(200, `{"_id":"`+testMemberID+`","email":"`+testMemberEmail+`"}`),
	}
	c := newMaintainerClient(t, s)

	res, err := c.ResolveMaintainer(context.Background(), testMemberID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MemberID != testMemberID {
		t.Errorf("MemberID = %q, want %q", res.MemberID, testMemberID)
	}
	if res.Email != testMemberEmail {
		t.Errorf("Email = %q, want %q", res.Email, testMemberEmail)
	}
	if res.Source != MaintainerFromFlagID {
		t.Errorf("Source = %v, want MaintainerFromFlagID", res.Source)
	}
}

func TestResolveMaintainer_ExplicitIDNotFound(t *testing.T) {
	s := &maintainerServer{memberByID: jsonHandler(404, `{"message":"not found"}`)}
	c := newMaintainerClient(t, s)

	_, err := c.ResolveMaintainer(context.Background(), testMemberID)
	if err == nil {
		t.Fatal("expected an error for an unknown member ID")
	}
	if !strings.Contains(err.Error(), testMemberID) {
		t.Errorf("error should name the ID it could not find, got: %v", err)
	}
}

func TestResolveMaintainer_EmailResolves(t *testing.T) {
	s := &maintainerServer{
		membersFilter: jsonHandler(200, `{"items":[{"_id":"`+testMemberID+`","email":"`+testMemberEmail+`"}]}`),
	}
	c := newMaintainerClient(t, s)

	res, err := c.ResolveMaintainer(context.Background(), testMemberEmail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MemberID != testMemberID {
		t.Errorf("MemberID = %q, want %q", res.MemberID, testMemberID)
	}
	if res.Source != MaintainerFromFlagEmail {
		t.Errorf("Source = %v, want MaintainerFromFlagEmail", res.Source)
	}
	if len(s.paths) != 1 || !strings.Contains(s.paths[0], "filter=email") {
		t.Errorf("should filter the members list by email, requests were %v", s.paths)
	}
}

// LD's email filter is an exact, case-sensitive match, so a near-miss returns
// zero members rather than an error. The message has to say that.
func TestResolveMaintainer_EmailNoMatchMentionsCaseSensitivity(t *testing.T) {
	s := &maintainerServer{membersFilter: jsonHandler(200, `{"items":[]}`)}
	c := newMaintainerClient(t, s)

	_, err := c.ResolveMaintainer(context.Background(), "Nobody@Example.com")
	if err == nil {
		t.Fatal("expected an error when no member matches the email")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "case") {
		t.Errorf("error should warn that the match is case-sensitive, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Nobody@Example.com") {
		t.Errorf("error should quote the address given, got: %v", err)
	}
}

// Default path: no flag, so take the token's member and validate it.
func TestResolveMaintainer_DefaultsToTokenMember(t *testing.T) {
	s := &maintainerServer{
		callerIdentity: jsonHandler(200, `{"memberId":"`+testMemberID+`","serviceToken":true}`),
		memberByID:     jsonHandler(200, `{"_id":"`+testMemberID+`","email":"`+testMemberEmail+`"}`),
	}
	c := newMaintainerClient(t, s)

	res, err := c.ResolveMaintainer(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MemberID != testMemberID {
		t.Errorf("MemberID = %q, want %q", res.MemberID, testMemberID)
	}
	if res.Source != MaintainerFromToken {
		t.Errorf("Source = %v, want MaintainerFromToken", res.Source)
	}
}

// A service token can outlive the member who created it. That must stop the run
// with an actionable message, not silently create unmaintained resources.
func TestResolveMaintainer_TokenMemberGoneIsFatalAndSuggestsTheFlag(t *testing.T) {
	s := &maintainerServer{
		callerIdentity: jsonHandler(200, `{"memberId":"`+testMemberID+`","serviceToken":true}`),
		memberByID:     jsonHandler(404, `{"message":"not found"}`),
	}
	c := newMaintainerClient(t, s)

	_, err := c.ResolveMaintainer(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error when the token's member no longer exists")
	}
	if !strings.Contains(err.Error(), "--ld-maintainer") {
		t.Errorf("error must offer --ld-maintainer as the fix, got: %v", err)
	}
}

func TestResolveMaintainer_CallerIdentityFailureSuggestsTheFlag(t *testing.T) {
	s := &maintainerServer{callerIdentity: jsonHandler(403, `{"message":"forbidden"}`)}
	c := newMaintainerClient(t, s)

	_, err := c.ResolveMaintainer(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error when caller-identity cannot be read")
	}
	if !strings.Contains(err.Error(), "--ld-maintainer") {
		t.Errorf("error must offer --ld-maintainer as the fix, got: %v", err)
	}
}

// A token with no member at all (nothing to fall back on) is also fatal.
func TestResolveMaintainer_NoMemberOnTokenIsFatal(t *testing.T) {
	s := &maintainerServer{
		callerIdentity: jsonHandler(200, `{"serviceToken":true}`),
	}
	c := newMaintainerClient(t, s)

	_, err := c.ResolveMaintainer(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error when the token carries no member ID")
	}
	if !strings.Contains(err.Error(), "--ld-maintainer") {
		t.Errorf("error must offer --ld-maintainer as the fix, got: %v", err)
	}
}

func TestValidateMaintainerFlag(t *testing.T) {
	ok := []string{"", "none", "None", testMemberID, testMemberEmail, "  none  "}
	for _, v := range ok {
		if err := ValidateMaintainerFlag(v); err != nil {
			t.Errorf("ValidateMaintainerFlag(%q) = %v, want nil", v, err)
		}
	}
	bad := []string{"nobody", "123", "not-an-email-or-id", "6917a463c1b64809c1124c3"}
	for _, v := range bad {
		if err := ValidateMaintainerFlag(v); err == nil {
			t.Errorf("ValidateMaintainerFlag(%q) = nil, want an error", v)
		}
	}
}
