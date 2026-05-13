package statsig

import (
	"context"
	"encoding/json"
	"fmt"
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
	client := NewClient("console-test-key", server.URL)
	return server, client
}

// ============================================================================
// ListGates
// ============================================================================

func TestListGates_HappyPath(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gates" {
			t.Errorf("got path %q, want /gates", r.URL.Path)
		}
		body := gatesListResponse{
			Data: []Gate{
				{ID: "show_banner", Name: "Show Banner", IsEnabled: true, Type: "TEMPORARY"},
				{ID: "alpha_user", Name: "Alpha User"},
			},
			Pagination: pagination{NextPage: ""},
		}
		_ = json.NewEncoder(w).Encode(body)
	})

	got, err := client.ListGates(context.Background())
	if err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d gates, want 2", len(got))
	}
	if got[0].ID != "show_banner" || got[1].ID != "alpha_user" {
		t.Errorf("unexpected gate IDs: %+v", got)
	}
}

func TestListGates_Pagination(t *testing.T) {
	// Build 105 fake gates split across two pages: 100 on page 1, 5 on page 2.
	// Page 1 has NextPage set AND len==pageSize → loop continues. Page 2 has
	// len<pageSize → loop terminates.
	allGates := make([]Gate, 105)
	for i := range allGates {
		allGates[i] = Gate{ID: fmt.Sprintf("gate_%d", i)}
	}

	var pageHits []string
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pageHits = append(pageHits, page)
		switch page {
		case "1":
			_ = json.NewEncoder(w).Encode(gatesListResponse{
				Data:       allGates[:100],
				Pagination: pagination{NextPage: "page-2"},
			})
		case "2":
			_ = json.NewEncoder(w).Encode(gatesListResponse{
				Data:       allGates[100:],
				Pagination: pagination{NextPage: ""},
			})
		default:
			t.Errorf("unexpected page param %q", page)
		}
	})

	got, err := client.ListGates(context.Background())
	if err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	if len(got) != 105 {
		t.Errorf("got %d gates, want 105 (server saw pages: %v)", len(got), pageHits)
	}
	if len(pageHits) != 2 || pageHits[0] != "1" || pageHits[1] != "2" {
		t.Errorf("expected pages [1, 2], got %v", pageHits)
	}
}

// TestListGates_Pagination_ExactlyFullLastPage covers the edge case the
// previous pagination test missed: a final page with exactly pageSize items
// AND NextPage="". The OR-termination must trigger on the empty NextPage
// alone, not require len<pageSize. Without this case, a project whose total
// gate count is a multiple of pageSize would hang on a phantom page-2.
func TestListGates_Pagination_ExactlyFullLastPage(t *testing.T) {
	full := make([]Gate, 100)
	for i := range full {
		full[i] = Gate{ID: fmt.Sprintf("gate_%d", i)}
	}

	var pageHits []string
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pageHits = append(pageHits, page)
		if page != "1" {
			t.Errorf("expected only page 1 to be fetched; got %q", page)
		}
		// Full page of items AND no next-page cursor.
		_ = json.NewEncoder(w).Encode(gatesListResponse{
			Data:       full,
			Pagination: pagination{NextPage: ""},
		})
	})

	got, err := client.ListGates(context.Background())
	if err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	if len(got) != 100 {
		t.Errorf("got %d gates, want 100", len(got))
	}
	if len(pageHits) != 1 {
		t.Errorf("expected exactly 1 page request (NextPage='' should terminate); got pages %v", pageHits)
	}
}

func TestListGates_HTTPError(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})

	_, err := client.ListGates(context.Background())
	if err == nil {
		t.Fatal("expected error from 500, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include server message; got: %v", err)
	}
}

func TestListGates_SendsAuthAndVersionHeaders(t *testing.T) {
	var gotKey, gotVersion, gotContentType string
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("STATSIG-API-KEY")
		gotVersion = r.Header.Get("STATSIG-API-VERSION")
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewEncoder(w).Encode(gatesListResponse{})
	})

	if _, err := client.ListGates(context.Background()); err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	if gotKey != "console-test-key" {
		t.Errorf("STATSIG-API-KEY = %q, want %q", gotKey, "console-test-key")
	}
	if gotVersion != apiVersion {
		t.Errorf("STATSIG-API-VERSION = %q, want %q", gotVersion, apiVersion)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", gotContentType, "application/json")
	}
}

// ============================================================================
// ListDynamicConfigs
// ============================================================================

func TestListDynamicConfigs_HappyPath(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dynamic_configs" {
			t.Errorf("got path %q, want /dynamic_configs", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(dynamicConfigsListResponse{
			Data: []DynamicConfig{
				{ID: "cta_copy", Name: "CTA Copy", DefaultValue: json.RawMessage(`{"text":"Sign up"}`)},
			},
			Pagination: pagination{NextPage: ""},
		})
	})

	got, err := client.ListDynamicConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListDynamicConfigs: %v", err)
	}
	if len(got) != 1 || got[0].ID != "cta_copy" {
		t.Errorf("unexpected configs: %+v", got)
	}
}

// ============================================================================
// ListEnvironments
// ============================================================================

func TestListEnvironments_HappyPath(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/environments" {
			t.Errorf("got path %q, want /environments", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(environmentsResponse{
			Data: struct {
				Environments []Environment `json:"environments"`
			}{
				Environments: []Environment{
					{Name: "development"},
					{Name: "staging"},
					{Name: "production", IsProduction: true, RequiresReview: true},
				},
			},
		})
	})

	got, err := client.ListEnvironments(context.Background())
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d envs, want 3", len(got))
	}
	if !got[2].IsProduction {
		t.Errorf("production env should have IsProduction=true; got %+v", got[2])
	}
}

// ============================================================================
// GetGateOverrides / GetDynamicConfigOverrides
// ============================================================================

func TestGetGateOverrides_HappyPath(t *testing.T) {
	prodEnv := "production"
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gates/show_banner/overrides" {
			t.Errorf("got path %q, want /gates/show_banner/overrides", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(overridesResponse{
			Data: struct {
				EnvironmentOverrides []Override `json:"environmentOverrides"`
			}{
				EnvironmentOverrides: []Override{
					{Environment: &prodEnv, UnitID: "userID", PassingIDs: []string{"u1", "u2"}, FailingIDs: []string{"u3"}},
				},
			},
		})
	})

	got, err := client.GetGateOverrides(context.Background(), "show_banner")
	if err != nil {
		t.Fatalf("GetGateOverrides: %v", err)
	}
	if len(got) != 1 || got[0].UnitID != "userID" || len(got[0].PassingIDs) != 2 {
		t.Errorf("unexpected overrides: %+v", got)
	}
}

func TestGetGateOverrides_404ReturnsNilNil(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	got, err := client.GetGateOverrides(context.Background(), "no_such_gate")
	if err != nil {
		t.Fatalf("expected no error on 404, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil slice on 404, got: %+v", got)
	}
}

func TestGetGateOverrides_500ReturnsError(t *testing.T) {
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`internal error`))
	})

	_, err := client.GetGateOverrides(context.Background(), "show_banner")
	if err == nil {
		t.Fatal("expected error from 500, got nil")
	}
}

func TestGetDynamicConfigOverrides_RoutesToCorrectPath(t *testing.T) {
	var gotPath string
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(overridesResponse{})
	})

	if _, err := client.GetDynamicConfigOverrides(context.Background(), "cta_copy"); err != nil {
		t.Fatalf("GetDynamicConfigOverrides: %v", err)
	}
	if gotPath != "/dynamic_configs/cta_copy/overrides" {
		t.Errorf("got path %q, want /dynamic_configs/cta_copy/overrides", gotPath)
	}
}

// ============================================================================
// FilterGates / FilterDynamicConfigs
// ============================================================================

func TestFilterGates(t *testing.T) {
	gates := []Gate{
		{ID: "a", Tags: []string{"p0", "auth"}},
		{ID: "b", Tags: []string{"p1"}},
		{ID: "c", Tags: nil},
		{ID: "d", Tags: []string{"p0"}},
	}

	cases := []struct {
		name string
		tag  string
		want []string
	}{
		{"empty tag returns all", "", []string{"a", "b", "c", "d"}},
		{"single match", "p1", []string{"b"}},
		{"multiple matches", "p0", []string{"a", "d"}},
		{"no matches", "missing", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterGates(gates, tc.tag)
			gotIDs := make([]string, len(got))
			for i, g := range got {
				gotIDs[i] = g.ID
			}
			if !equalStringSlices(gotIDs, tc.want) {
				t.Errorf("FilterGates(%q) = %v, want %v", tc.tag, gotIDs, tc.want)
			}
		})
	}
}

func TestFilterDynamicConfigs(t *testing.T) {
	configs := []DynamicConfig{
		{ID: "a", Tags: []string{"p0"}},
		{ID: "b", Tags: []string{"p1"}},
		{ID: "c", Tags: []string{"p0", "p1"}},
	}

	cases := []struct {
		name string
		tag  string
		want []string
	}{
		{"empty tag returns all", "", []string{"a", "b", "c"}},
		{"p0 matches a and c", "p0", []string{"a", "c"}},
		{"no match", "p2", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterDynamicConfigs(configs, tc.tag)
			gotIDs := make([]string, len(got))
			for i, c := range got {
				gotIDs[i] = c.ID
			}
			if !equalStringSlices(gotIDs, tc.want) {
				t.Errorf("FilterDynamicConfigs(%q) = %v, want %v", tc.tag, gotIDs, tc.want)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
