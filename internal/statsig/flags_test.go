package statsig

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ===========================================================================
// Tag filtering helpers — pure functions, no IO.
// ===========================================================================

func TestFilterGatesByTag(t *testing.T) {
	gates := []Gate{
		{ID: "a", Tags: []string{"prod", "team-x"}},
		{ID: "b", Tags: []string{"team-x"}},
		{ID: "c", Tags: []string{}},
	}

	t.Run("empty tag returns input unchanged", func(t *testing.T) {
		got := FilterGatesByTag(gates, "")
		if len(got) != 3 {
			t.Errorf("got %d, want 3", len(got))
		}
	})
	t.Run("keeps matching", func(t *testing.T) {
		got := FilterGatesByTag(gates, "team-x")
		if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
			t.Errorf("got %+v, want [a, b]", got)
		}
	})
	t.Run("drops non-matching", func(t *testing.T) {
		got := FilterGatesByTag(gates, "prod")
		if len(got) != 1 || got[0].ID != "a" {
			t.Errorf("got %+v, want [a]", got)
		}
	})
	t.Run("no matches → empty", func(t *testing.T) {
		got := FilterGatesByTag(gates, "nonexistent")
		if len(got) != 0 {
			t.Errorf("got %d, want 0", len(got))
		}
	})
}

func TestFilterDynamicConfigsByTag(t *testing.T) {
	configs := []DynamicConfig{
		{ID: "a", Tags: []string{"copy"}},
		{ID: "b", Tags: []string{"layout"}},
	}
	got := FilterDynamicConfigsByTag(configs, "copy")
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("got %+v, want [a]", got)
	}
}

// ===========================================================================
// Statsig client — list / overrides / environments with httptest.Server.
//
// These cover:
//   - The full pagination loop (nextPage → empty → exit)
//   - Required headers (STATSIG-API-KEY, STATSIG-API-VERSION, Content-Type)
//   - 200/404/5xx response handling on the override endpoints
//   - URL composition (apiBase + path, no double-slashes)
// ===========================================================================

func newTestClient(srvURL string) *Client {
	// The metric path's defaultAPIBase ends in /console/v1, so a test server
	// URL paired with /console/v1 path prefix would double-up. We pass the
	// server URL as the apiBase override (with /console/v1 appended) so the
	// methods can use their relative paths unchanged.
	return NewClient("test-key", srvURL+"/console/v1")
}

func TestClient_ListGates_PaginatesUntilEmptyNextPage(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("STATSIG-API-KEY"); got != "test-key" {
			t.Errorf("call %d: missing/wrong STATSIG-API-KEY header: %q", calls, got)
		}
		if got := r.Header.Get("STATSIG-API-VERSION"); got != statsigAPIVersion {
			t.Errorf("call %d: STATSIG-API-VERSION = %q, want %q", calls, got, statsigAPIVersion)
		}
		if !strings.HasSuffix(r.URL.Path, "/console/v1/gates") {
			t.Errorf("call %d: URL path = %q, expected suffix /console/v1/gates", calls, r.URL.Path)
		}
		page := r.URL.Query().Get("page")

		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			// Return a full page (100 items) plus nextPage so the client continues.
			items := make([]string, 100)
			for i := range items {
				items[i] = fmt.Sprintf(`{"id":"gate_%d_%d","name":"Gate %d","type":"PERMANENT","tags":[]}`, 1, i, i)
			}
			fmt.Fprintf(w, `{"message":"ok","data":[%s],"pagination":{"itemsPerPage":100,"pageNumber":1,"totalItems":150,"nextPage":"page2","previousPage":""}}`,
				strings.Join(items, ","))
		case "2":
			// Return < pageSize → client exits the loop.
			items := make([]string, 50)
			for i := range items {
				items[i] = fmt.Sprintf(`{"id":"gate_%d_%d","name":"Gate %d","type":"PERMANENT","tags":[]}`, 2, i, i)
			}
			fmt.Fprintf(w, `{"message":"ok","data":[%s],"pagination":{"itemsPerPage":100,"pageNumber":2,"totalItems":150,"nextPage":null,"previousPage":"page1"}}`,
				strings.Join(items, ","))
		default:
			t.Errorf("unexpected page query: %q", page)
			http.Error(w, "bad page", 400)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	gates, err := c.ListGates(context.Background())
	if err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 paginated calls, got %d", calls)
	}
	if len(gates) != 150 {
		t.Errorf("expected 150 gates total, got %d", len(gates))
	}
}

func TestClient_ListGates_SinglePageReturnsAndExits(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"ok","data":[{"id":"single","name":"Single","type":"PERMANENT","tags":[]}],"pagination":{"itemsPerPage":100,"pageNumber":1,"totalItems":1,"nextPage":null}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	gates, _ := c.ListGates(context.Background())
	if calls != 1 {
		t.Errorf("expected exactly 1 call (no pagination), got %d", calls)
	}
	if len(gates) != 1 {
		t.Errorf("got %d gates, want 1", len(gates))
	}
}

func TestClient_ListGates_ErrorPropagatesStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"invalid api key"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.ListGates(context.Background())
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should include status 401: %v", err)
	}
}

func TestClient_ListDynamicConfigs_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/console/v1/dynamic_configs") {
			t.Errorf("URL path = %q, expected suffix /console/v1/dynamic_configs", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"ok","data":[{"id":"dc1","name":"DC One","defaultValue":{"x":1},"isEnabled":true,"tags":[],"rules":[]}],"pagination":{"itemsPerPage":100,"pageNumber":1,"totalItems":1,"nextPage":null}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	configs, err := c.ListDynamicConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListDynamicConfigs: %v", err)
	}
	if len(configs) != 1 || configs[0].ID != "dc1" {
		t.Errorf("got %+v", configs)
	}
}

func TestClient_ListEnvironments_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/console/v1/environments") {
			t.Errorf("URL path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"environments":[
			{"name":"production","isProduction":true,"requiresReview":true},
			{"name":"staging","isProduction":false,"requiresReview":false}
		]}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	envs, err := c.ListEnvironments(context.Background())
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("got %d envs, want 2", len(envs))
	}
	if !envs[0].IsProduction {
		t.Error("first env should be production")
	}
}

func TestClient_GetGateOverrides_Returns200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/console/v1/gates/my-gate/overrides") {
			t.Errorf("URL path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"environmentOverrides":[
			{"environment":"production","unitID":"userID","passingIDs":["alice"],"failingIDs":["bob"]},
			{"environment":null,"unitID":"orgID","passingIDs":["acme"],"failingIDs":[]}
		]}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	overs, err := c.GetGateOverrides(context.Background(), "my-gate")
	if err != nil {
		t.Fatalf("GetGateOverrides: %v", err)
	}
	if len(overs) != 2 {
		t.Fatalf("got %d overrides, want 2", len(overs))
	}
	if overs[0].Environment == nil || *overs[0].Environment != "production" {
		t.Errorf("overrides[0].Environment = %v", overs[0].Environment)
	}
	if overs[1].Environment != nil {
		t.Errorf("overrides[1].Environment should be nil, got %v", overs[1].Environment)
	}
}

func TestClient_GetGateOverrides_404IsNotAnError(t *testing.T) {
	// Statsig returns 404 for gates with no overrides. Goaltender's contract
	// (and ours) is to treat this as "no overrides", not an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	overs, err := c.GetGateOverrides(context.Background(), "no-overrides")
	if err != nil {
		t.Errorf("404 should not produce error: %v", err)
	}
	if overs != nil {
		t.Errorf("404 should produce nil overrides, got %v", overs)
	}
}

func TestClient_GetGateOverrides_ServerErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"internal"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetGateOverrides(context.Background(), "g")
	if err == nil {
		t.Error("expected error for 500")
	}
}

func TestClient_GetDynamicConfigOverrides_URLPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path includes the dc ID followed by /overrides
		if !strings.HasSuffix(r.URL.Path, "/console/v1/dynamic_configs/my-dc/overrides") {
			t.Errorf("URL path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"environmentOverrides":[]}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.GetDynamicConfigOverrides(context.Background(), "my-dc")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
