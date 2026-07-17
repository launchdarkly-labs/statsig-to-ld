package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

func TestDumpRawMetrics_WritesVerbatimJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Includes a field the Metric struct doesn't model; it must survive.
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "rev::user_warehouse", "name": "rev", "type": "user_warehouse",
				 "warehouseNative": {"aggregation": "sum", "mysteryField": {"nested": 42}}}
			],
			"pagination": {"itemsPerPage": 100, "pageNumber": 1, "totalItems": 1, "nextPage": ""}
		}`))
	}))
	defer srv.Close()

	client := statsig.NewClient("console-test", srv.URL)
	path := filepath.Join(t.TempDir(), "raw.json")

	if err := dumpRawMetrics(context.Background(), client, path); err != nil {
		t.Fatalf("dumpRawMetrics: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading dump file: %v", err)
	}
	got := string(data)
	for _, want := range []string{"warehouseNative", "aggregation", "mysteryField", "nested", "42", "user_warehouse"} {
		if !strings.Contains(got, want) {
			t.Errorf("dump file missing %q (raw JSON must be preserved verbatim); got:\n%s", want, got)
		}
	}
}
