package launchdarkly

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer spins up an httptest.Server, registers cleanup, and returns
// it plus a Client pointed at it.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient("api-test-key", "my-project", server.URL)
	return server, client
}

func TestActionableHint_UnitNotFound(t *testing.T) {
	msg := `Randomization unit "stableid" not found in project settings [user]`
	hint := actionableHint(400, msg)
	if hint == "" {
		t.Fatal("expected actionable hint for unit-not-found 400, got empty string")
	}
	if !strings.Contains(hint, "stableid") {
		t.Errorf("hint should name the missing unit %q, got: %s", "stableid", hint)
	}
	if !strings.Contains(hint, "--unit-type-mapping") {
		t.Errorf("hint should reference the --unit-type-mapping flag, got: %s", hint)
	}
	if !strings.Contains(hint, "JSON file") {
		t.Errorf("hint should clarify that --unit-type-mapping takes a file path, not inline JSON, got: %s", hint)
	}
}

func TestActionableHint_Unauthorized(t *testing.T) {
	hint := actionableHint(401, "unauthorized")
	if hint == "" {
		t.Fatal("expected actionable hint for 401, got empty string")
	}
	if !strings.Contains(hint, "Authorization") {
		t.Errorf("hint should reference where to manage tokens, got: %s", hint)
	}
}

func TestActionableHint_Forbidden(t *testing.T) {
	hint := actionableHint(403, "forbidden")
	if hint == "" {
		t.Fatal("expected actionable hint for 403, got empty string")
	}
	if !strings.Contains(hint, "Writer") {
		t.Errorf("hint should mention Writer role, got: %s", hint)
	}
}

func TestActionableHint_NotFound(t *testing.T) {
	hint := actionableHint(404, "project not found")
	if hint == "" {
		t.Fatal("expected actionable hint for 404, got empty string")
	}
	if !strings.Contains(hint, "--ld-project") {
		t.Errorf("hint should reference --ld-project flag, got: %s", hint)
	}
}

func TestActionableHint_NoHint(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		msg        string
	}{
		{"unrelated 400", 400, "some other validation error"},
		{"500 server error", 500, "internal server error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if hint := actionableHint(tc.statusCode, tc.msg); hint != "" {
				t.Errorf("expected no hint, got: %s", hint)
			}
		})
	}
}

// ============================================================================
// ListAllFlags
// ============================================================================

func TestListAllFlags_HappyPath(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/flags/my-project" {
			t.Errorf("got path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(listFlagsResponse{
			Items: []Flag{
				{Key: "show-banner", Name: "Show Banner"},
				{Key: "alpha-user", Name: "Alpha User"},
			},
		})
		// Simulate end of pagination on the second call.
	})

	got, err := client.ListAllFlags(context.Background(), "")
	if err != nil {
		t.Fatalf("ListAllFlags: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("got %d flags, want at least 2", len(got))
	}
}

func TestListAllFlags_PaginationStopsOnEmptyPage(t *testing.T) {
	var calls int
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			// Return exactly pageSize items so the loop advances.
			items := make([]Flag, flagListPageSize)
			for i := range items {
				items[i] = Flag{Key: "f"}
			}
			_ = json.NewEncoder(w).Encode(listFlagsResponse{Items: items})
		default:
			// Subsequent pages: empty. Loop should terminate.
			_ = json.NewEncoder(w).Encode(listFlagsResponse{Items: []Flag{}})
		}
	})

	got, err := client.ListAllFlags(context.Background(), "")
	if err != nil {
		t.Fatalf("ListAllFlags: %v", err)
	}
	if len(got) != flagListPageSize {
		t.Errorf("got %d flags, want %d", len(got), flagListPageSize)
	}
	if calls != 2 {
		t.Errorf("got %d server calls, want 2", calls)
	}
}

func TestListAllFlags_TagFilterSent(t *testing.T) {
	var gotFilter string
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filter")
		_ = json.NewEncoder(w).Encode(listFlagsResponse{})
	})

	if _, err := client.ListAllFlags(context.Background(), "imported-from-statsig"); err != nil {
		t.Fatalf("ListAllFlags: %v", err)
	}
	if gotFilter != "tags:imported-from-statsig" {
		t.Errorf("got filter=%q, want %q", gotFilter, "tags:imported-from-statsig")
	}
}

func TestListAllFlags_HTTPError(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})

	_, err := client.ListAllFlags(context.Background(), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include server message; got: %v", err)
	}
}

// ============================================================================
// CreateFlag
// ============================================================================

func TestCreateFlag_HappyPath(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v2/flags/my-project" {
			t.Errorf("got path %q", r.URL.Path)
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(Flag{Key: "show-banner", Name: "Show Banner"})
	})

	got, err := client.CreateFlag(context.Background(), Flag{Key: "show-banner", Name: "Show Banner"})
	if err != nil {
		t.Fatalf("CreateFlag: %v", err)
	}
	if got.Key != "show-banner" {
		t.Errorf("got key %q, want %q", got.Key, "show-banner")
	}
}

func TestCreateFlag_409ReturnsConflictError(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"message":"already exists"}`))
	})

	_, err := client.CreateFlag(context.Background(), Flag{Key: "show-banner"})
	if err == nil {
		t.Fatal("expected ConflictError, got nil")
	}
	if !IsConflict(err) {
		t.Errorf("expected ConflictError, got: %v", err)
	}
}

// ============================================================================
// ListEnvironments (cursor pagination via _links.next.href)
// ============================================================================

func TestListEnvironments_FollowsNextLink(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First call (no offset query) returns page 1 with a next href.
		// Second call (offset=2) returns page 2 with no next href.
		offset := r.URL.Query().Get("offset")
		switch offset {
		case "":
			resp := listEnvironmentsResponse{
				Items: []Environment{{Key: "dev", Name: "Development"}, {Key: "staging", Name: "Staging"}},
			}
			resp.Links.Next.Href = "/api/v2/projects/my-project/environments?offset=2"
			_ = json.NewEncoder(w).Encode(resp)
		case "2":
			_ = json.NewEncoder(w).Encode(listEnvironmentsResponse{
				Items: []Environment{{Key: "prod", Name: "Production"}},
			})
		default:
			t.Errorf("unexpected offset %q", offset)
		}
	}))
	t.Cleanup(server.Close)
	client := NewClient("api-test-key", "my-project", server.URL)

	got, err := client.ListEnvironments(context.Background())
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d envs, want 3", len(got))
	}
	if got[2].Key != "prod" {
		t.Errorf("third env key = %q, want %q", got[2].Key, "prod")
	}
}

// ============================================================================
// CreateEnvironment
// ============================================================================

func TestCreateEnvironment_HappyPath(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(Environment{Key: "qa", Name: "QA", Color: "808080"})
	})

	got, err := client.CreateEnvironment(context.Background(), Environment{Key: "qa", Name: "QA"})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if got.Key != "qa" || got.Color != "808080" {
		t.Errorf("got %+v", got)
	}
}

func TestCreateEnvironment_409ReturnsExists(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
	})

	_, err := client.CreateEnvironment(context.Background(), Environment{Key: "qa"})
	if !errors.Is(err, ErrEnvironmentExists) {
		t.Errorf("got %v, want ErrEnvironmentExists", err)
	}
}

func TestCreateEnvironment_403ReturnsForbidden(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
	})

	_, err := client.CreateEnvironment(context.Background(), Environment{Key: "qa"})
	if !errors.Is(err, ErrEnvironmentForbidden) {
		t.Errorf("got %v, want ErrEnvironmentForbidden", err)
	}
}

func TestCreateEnvironment_401ReturnsForbidden(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	})

	_, err := client.CreateEnvironment(context.Background(), Environment{Key: "qa"})
	if !errors.Is(err, ErrEnvironmentForbidden) {
		t.Errorf("got %v, want ErrEnvironmentForbidden", err)
	}
}

// ============================================================================
// PatchFlag
// ============================================================================

func TestPatchFlag_HappyPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = readAll(r.Body)
		w.WriteHeader(200)
	})

	ops := []JSONPatchOp{
		{Op: "replace", Path: "/environments/production/on", Value: true},
	}
	if err := client.PatchFlag(context.Background(), "show-banner", ops); err != nil {
		t.Fatalf("PatchFlag: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("got method %q, want PATCH", gotMethod)
	}
	if gotPath != "/api/v2/flags/my-project/show-banner" {
		t.Errorf("got path %q", gotPath)
	}
	if !strings.Contains(string(gotBody), `"replace"`) {
		t.Errorf("body should contain the op; got: %s", gotBody)
	}
}

func TestPatchFlag_EmptyOpsIsNoOp(t *testing.T) {
	var called bool
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	if err := client.PatchFlag(context.Background(), "show-banner", nil); err != nil {
		t.Fatalf("PatchFlag with nil ops: %v", err)
	}
	if called {
		t.Error("PatchFlag with empty ops should not hit the network")
	}
}

func TestPatchFlag_HTTPError(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})

	err := client.PatchFlag(context.Background(), "show-banner", []JSONPatchOp{{Op: "replace", Path: "/x", Value: 1}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ============================================================================
// EscapeJSONPointer
// ============================================================================

func TestEscapeJSONPointer(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"production", "production"},
		{"prod/west", "prod~1west"},
		{"foo~bar", "foo~0bar"},
		// Order matters: ~1 in input must not become / after escaping ~.
		{"~1", "~01"},
		{"a~b/c", "a~0b~1c"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := EscapeJSONPointer(tc.in); got != tc.want {
				t.Errorf("EscapeJSONPointer(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func readAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
	}
}
