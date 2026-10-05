package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/converter"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/report"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

var checkoutMapping = map[string]string{"Checkout Events": "checkout-events", "Page Views": "page-views"}

func dataSourceListServer(t *testing.T, status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/projects/proj/metric-data-sources" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

// A failed data source read must not quietly fall back to legacy event keys on
// a real run: every warehouse-native metric would be created matching no rows.
func TestFetchDataSourceKeys_ListFailureStopsRealRun(t *testing.T) {
	srv := dataSourceListServer(t, http.StatusForbidden, `{"code":"forbidden"}`)
	defer srv.Close()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)

	if _, _, err := fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "", false, false); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("real run: err = %v, want the 403", err)
	}

	keys, _, err := fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "", false, true)
	if err != nil || len(keys) != 0 {
		t.Errorf("dry run: keys=%v err=%v, want a warning and no keys", keys, err)
	}

	keys, _, err = fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "", true, true)
	if err != nil || keys["checkout-events"] != "checkout-events" || keys["page-views"] != "page-views" {
		t.Errorf("dry run with assume: keys=%v err=%v, want every mapped source assumed", keys, err)
	}
}

func TestFetchDataSourceKeys_ReadsLaunchDarklyAndAssumesOnlyMissingSources(t *testing.T) {
	list, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{
			"key":      "checkout-events",
			"sqlQuery": "SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nSELECT * FROM analytics.orders\n) AS ld_src",
			"columnMappings": map[string]any{"keyColumn": "LD_EVENT_KEY", "columns": []any{
				map[string]any{"name": "TS"}, map[string]any{"name": "ORDER_TOTAL"}, map[string]any{"name": "LD_EVENT_KEY"},
			}},
		},
		map[string]any{
			"key": "hand-built", "sqlQuery": "SELECT * FROM analytics.events",
			"columnMappings": map[string]any{"keyColumn": "EVENT_NAME", "columns": []any{map[string]any{"name": "EVENT_NAME"}}},
		},
	}})
	srv := dataSourceListServer(t, http.StatusOK, string(list))
	defer srv.Close()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)

	keys, cols, err := fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "hand-built", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys["checkout-events"] != "checkout-events" {
		t.Errorf("keys = %v, want only the wrapped checkout-events", keys)
	}
	if !slices.Equal(cols["checkout-events"], []string{"TS", "ORDER_TOTAL", "LD_EVENT_KEY"}) || len(cols["hand-built"]) != 1 {
		t.Errorf("cols = %v", cols)
	}

	// page-views is mapped but not in LaunchDarkly yet; hand-built exists and is
	// not wrapped, which LaunchDarkly reports, so the assumption leaves it alone.
	keys, _, err = fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "hand-built", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if keys["page-views"] != "page-views" || keys["checkout-events"] != "checkout-events" {
		t.Errorf("keys = %v, want page-views assumed", keys)
	}
	if _, ok := keys["hand-built"]; ok {
		t.Error("a data source LaunchDarkly reports as not wrapped must not be assumed constant-key")
	}
}

func TestFetchDataSourceKeys_AssumeWithoutCredentials(t *testing.T) {
	keys, cols, err := fetchDataSourceKeys(context.Background(), nil, checkoutMapping, "orders", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || len(cols) != 0 {
		t.Errorf("keys=%v cols=%v, want all three mapped sources assumed with unknown columns", keys, cols)
	}
}

func filteredCheckoutMetric() statsig.Metric {
	var m statsig.Metric
	raw := `{"type":"user_warehouse","name":"Pro Checkouts","id":"Pro Checkouts::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count","metricSourceName":"Checkout Events",
	    "criteria":[{"type":"metadata","condition":"sql_filter","values":["plan_tier > 2"]}]}}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		panic(err)
	}
	return m
}

func constKeyConvOpts() converter.Options {
	return converter.Options{
		SourceMapping:     checkoutMapping,
		ConstantEventKeys: map[string]string{"checkout-events": "checkout-events"},
		DataSourceColumns: map[string][]string{"checkout-events": {"TS", "USER_ID", "ORDER_TOTAL", "LD_EVENT_KEY"}},
	}
}

// --convert-lossy accepts approximations, but not a metric that would count
// every row of its data source because its filter could not be converted.
func TestProcessMetric_UnfilteredConstantKeyMetricSkippedEvenWithConvertLossy(t *testing.T) {
	prev := flagConvertLossy
	flagConvertLossy = true
	defer func() { flagConvertLossy = prev }()

	rpt := report.New()
	processMetric(context.Background(), filteredCheckoutMetric(), constKeyConvOpts(), nil, rpt, "proj", true, 1, 1, new(int64))

	if len(rpt.Metrics) != 1 {
		t.Fatalf("got %d report entries, want 1", len(rpt.Metrics))
	}
	e := rpt.Metrics[0]
	if e.Status != report.StatusSkippedIncompatible {
		t.Errorf("status = %q, want %q", e.Status, report.StatusSkippedIncompatible)
	}
	if !slices.Contains(e.BlockingCodes, converter.WarnUnfilteredConstantKey) || !strings.Contains(e.Reason, "every row") {
		t.Errorf("entry = %+v, want the blocking code and reason", e)
	}
	if len(e.Filters) != 1 || e.Filters[0].Applied {
		t.Errorf("filter diagnostics = %+v, want the blocked term kept", e.Filters)
	}
}

// A metric that already exists is skipped. If it predates the constant event
// key it matches no rows, and nothing else in the run fixes it, so it must be
// named rather than counted as a quiet skip.
func TestProcessMetric_ExistingMetricWithLegacyEventKeyIsReported(t *testing.T) {
	for _, tc := range []struct {
		name        string
		existingKey string
		wantWarning bool
	}{
		{"legacy event key", "order_total", true},
		{"constant event key", "checkout-events", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/v2/metrics/proj":
					w.WriteHeader(http.StatusConflict)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v2/metrics/proj/order-total-user-warehouse":
					_, _ = io.WriteString(w, `{"key":"order-total-user-warehouse","eventKey":"`+tc.existingKey+`","dataSource":{"key":"checkout-events"}}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			var m statsig.Metric
			raw := `{"type":"user_warehouse","name":"Order Total","id":"Order Total::user_warehouse","directionality":"increase",
			  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout Events","valueColumn":"order_total"}}`
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				t.Fatal(err)
			}
			rpt := report.New()
			ld := launchdarkly.NewClient("api-x", "proj", srv.URL)
			processMetric(context.Background(), m, constKeyConvOpts(), ld, rpt, "proj", false, 1, 1, new(int64))

			if len(rpt.Metrics) != 1 || rpt.Metrics[0].Status != report.StatusSkippedExisting {
				t.Fatalf("report = %+v, want one skipped_existing entry", rpt.Metrics)
			}
			stale := staleExistingMetrics(rpt)
			if tc.wantWarning {
				if len(stale) != 1 || !strings.Contains(rpt.Metrics[0].Warnings[0], `"order_total"`) {
					t.Errorf("stale=%v warnings=%v, want the metric named with its event key", stale, rpt.Metrics[0].Warnings)
				}
			} else if len(stale) != 0 || len(rpt.Metrics[0].Warnings) != 0 {
				t.Errorf("stale=%v warnings=%v, want none", stale, rpt.Metrics[0].Warnings)
			}
		})
	}
}
