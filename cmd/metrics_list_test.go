package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

func TestListMetrics_Output(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"data": []map[string]any{
				{"id": "purchase_revenue::sum", "name": "purchase_revenue", "type": "sum"},
				{"id": "dau::event_user", "name": "daily_active_users", "type": "event_user"},
			},
			"pagination": map[string]any{"itemsPerPage": 100, "pageNumber": 1, "totalItems": 2, "nextPage": ""},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := statsig.NewClient("console-test", srv.URL)
	var buf bytes.Buffer
	if err := listMetrics(context.Background(), client, &buf); err != nil {
		t.Fatalf("listMetrics: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"NAME", "TYPE", "ID", "purchase_revenue", "sum", "daily_active_users", "event_user", "2 metrics"} {
		if !strings.Contains(out, want) {
			t.Errorf("listMetrics output missing %q; got:\n%s", want, out)
		}
	}
}
