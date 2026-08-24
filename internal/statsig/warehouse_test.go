package statsig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// The warehouse command read metric sources with a single unpaged request, so
// any account with more than one page of sources was silently truncated at
// pageSize. A customer run reported "exactly 100", which was the whole tell.
func TestListMetricSources_FollowsPagination(t *testing.T) {
	var pagesRequested []string
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pagesRequested = append(pagesRequested, page)

		var items []map[string]any
		var nextPage string
		switch page {
		case "1", "":
			for i := 0; i < pageSize; i++ {
				items = append(items, map[string]any{"name": fmt.Sprintf("source-%d", i)})
			}
			nextPage = "2"
		case "2":
			for i := 0; i < 30; i++ {
				items = append(items, map[string]any{"name": fmt.Sprintf("source-%d", pageSize+i)})
			}
			nextPage = ""
		default:
			t.Errorf("unexpected page requested: %q", page)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       items,
			"pagination": map[string]any{"nextPage": nextPage},
		})
	})

	sources, err := client.ListMetricSources(context.Background())
	if err != nil {
		t.Fatalf("ListMetricSources: %v", err)
	}
	if len(sources) != pageSize+30 {
		t.Errorf("got %d sources, want %d (both pages)", len(sources), pageSize+30)
	}
	if len(pagesRequested) != 2 {
		t.Errorf("requested pages %v, want two requests", pagesRequested)
	}
}

// A single short page must not trigger a second request.
func TestListMetricSources_StopsOnShortPage(t *testing.T) {
	requests := 0
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":       []map[string]any{{"name": "only-source"}},
			"pagination": map[string]any{"nextPage": ""},
		})
	})

	sources, err := client.ListMetricSources(context.Background())
	if err != nil {
		t.Fatalf("ListMetricSources: %v", err)
	}
	if len(sources) != 1 {
		t.Errorf("got %d sources, want 1", len(sources))
	}
	if requests != 1 {
		t.Errorf("made %d requests, want 1", requests)
	}
}
