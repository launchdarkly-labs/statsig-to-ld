package cmd

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestFetchDataSourceKeys_ListFailureStopsRealRun(t *testing.T) {
	srv := dataSourceListServer(t, http.StatusForbidden, `{"code":"forbidden"}`)
	defer srv.Close()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)

	if _, err := fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "", false, false); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("real run: err = %v, want the 403", err)
	}

	info, err := fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "", false, true)
	if err != nil || len(info.keys) != 0 || info.fetched {
		t.Errorf("dry run: info=%+v err=%v, want a warning, no keys, and not fetched", info, err)
	}

	info, err = fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "", true, true)
	if err != nil || info.keys["checkout-events"] != "checkout-events" || info.keys["page-views"] != "page-views" || info.constant != 0 {
		t.Errorf("dry run with assume: info=%+v err=%v, want every mapped source assumed and none counted as read", info, err)
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

	info, err := fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "hand-built", false, false)
	if err != nil {
		t.Fatal(err)
	}
	keys, cols := info.keys, info.cols
	if len(keys) != 1 || keys["checkout-events"] != "checkout-events" || !info.fetched || info.constant != 1 {
		t.Errorf("info = %+v, want only the wrapped checkout-events, read from LaunchDarkly", info)
	}
	if !slices.Equal(cols["checkout-events"], []string{"TS", "ORDER_TOTAL", "LD_EVENT_KEY"}) || len(cols["hand-built"]) != 1 {
		t.Errorf("cols = %v", cols)
	}

	// page-views is not in LaunchDarkly yet; hand-built is listed unwrapped, so assume skips it.
	info, err = fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "hand-built", true, false)
	if err != nil {
		t.Fatal(err)
	}
	keys = info.keys
	if keys["page-views"] != "page-views" || keys["checkout-events"] != "checkout-events" {
		t.Errorf("keys = %v, want page-views assumed", keys)
	}
	if _, ok := keys["hand-built"]; ok {
		t.Error("a data source LaunchDarkly reports as not wrapped must not be assumed constant-key")
	}
}

func TestFetchDataSourceKeys_WarnsOnceForSourcesWithoutConstantKey(t *testing.T) {
	list, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{
			"key":      "checkout-events",
			"sqlQuery": "SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nSELECT * FROM analytics.orders\n) AS ld_src",
			"columnMappings": map[string]any{"keyColumn": "LD_EVENT_KEY", "columns": []any{
				map[string]any{"name": "TS"}, map[string]any{"name": "LD_EVENT_KEY"},
			}},
		},
		map[string]any{
			"key": "page-views", "sqlQuery": "SELECT * FROM analytics.page_views",
			"columnMappings": map[string]any{"keyColumn": "EVENT_NAME", "columns": []any{map[string]any{"name": "EVENT_NAME"}}},
		},
	}})
	srv := dataSourceListServer(t, http.StatusOK, string(list))
	defer srv.Close()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)

	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	if _, err := fetchDataSourceKeys(context.Background(), ld, checkoutMapping, "", false, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "WARNING:") != 1 || !strings.Contains(out, "1 mapped data source(s) do not have the constant event key: page-views") || !strings.Contains(out, "warehouse --overwrite") {
		t.Errorf("log = %q, want one warning naming page-views and pointing at warehouse --overwrite", out)
	}
	if strings.Contains(out, "checkout-events") {
		t.Errorf("log = %q, the wrapped data source must not be named", out)
	}
}

func TestFetchDataSourceKeys_AssumeWithoutCredentials(t *testing.T) {
	info, err := fetchDataSourceKeys(context.Background(), nil, checkoutMapping, "orders", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.keys) != 3 || len(info.cols) != 0 || info.fetched {
		t.Errorf("info = %+v, want all three mapped sources assumed with unknown columns", info)
	}
}

// A UNION of literal-tagged subqueries opens like the wrapper but has a multi-valued key column.
func TestFetchDataSourceKeys_UnionOfTaggedSubqueriesIsNotConstantKey(t *testing.T) {
	unionSQL := "SELECT *, 'signup' AS EVENT_NAME FROM (SELECT user_id, ts FROM signups) AS a\n" +
		"UNION ALL\nSELECT *, 'purchase' AS EVENT_NAME FROM (SELECT user_id, ts FROM purchases) AS b"
	list, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"key": "events", "sqlQuery": unionSQL, "integrationKey": "snowflake-experimentation",
		"columnMappings": map[string]any{"keyColumn": "EVENT_NAME", "columns": []any{
			map[string]any{"name": "USER_ID"}, map[string]any{"name": "TS"}, map[string]any{"name": "EVENT_NAME"}}},
	}}})
	srv := dataSourceListServer(t, http.StatusOK, string(list))
	defer srv.Close()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)
	mapping := map[string]string{"Events": "events"}
	info, err := fetchDataSourceKeys(context.Background(), ld, mapping, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.keys) != 0 {
		t.Fatalf("constant keys = %v, want none", info.keys)
	}

	var sg statsig.Metric
	raw := `{"type":"user_warehouse","name":"Purchases","id":"Purchases::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count","metricSourceName":"Events"}}`
	if err := json.Unmarshal([]byte(raw), &sg); err != nil {
		t.Fatal(err)
	}
	res, err := converter.Convert(&sg, converter.Options{SourceMapping: mapping, ConstantEventKeys: info.keys, DataSourceColumns: info.cols})
	if err != nil {
		t.Fatal(err)
	}
	if res.LDMetric.EventKey == "signup" {
		t.Errorf("eventKey = %q, the first union branch's literal", res.LDMetric.EventKey)
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
			stale := existingMetricsWithCode(rpt, converter.WarnExistingEventKeyMismatch)
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

func TestProcessMetric_ExistingMetricReadBackFailureIsUnverified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"forbidden"}`)
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

	if len(rpt.Metrics) != 1 || !slices.Equal(rpt.Metrics[0].WarningCodes, []string{converter.WarnExistingMetricUnverified}) {
		t.Fatalf("report = %+v, want one entry coded %s", rpt.Metrics, converter.WarnExistingMetricUnverified)
	}
	if stale := existingMetricsWithCode(rpt, converter.WarnExistingEventKeyMismatch); len(stale) != 0 {
		t.Errorf("unverified metric counted as a mismatch: %v", stale)
	}
	if unread := existingMetricsWithCode(rpt, converter.WarnExistingMetricUnverified); len(unread) != 1 {
		t.Errorf("unverified = %v, want the metric", unread)
	}
}

// An edited wrapper still projects the data source key on every row; another literal does not.
func TestFetchDataSourceKeys_EditedWrapperIsConstantKey(t *testing.T) {
	ds := func(key, sql string) map[string]any {
		return map[string]any{"key": key, "sqlQuery": sql, "integrationKey": "snowflake-experimentation",
			"columnMappings": map[string]any{"keyColumn": "LD_EVENT_KEY", "columns": []any{map[string]any{"name": "TS"}, map[string]any{"name": "LD_EVENT_KEY"}}}}
	}
	list, _ := json.Marshal(map[string]any{"items": []any{
		ds("checkout-events", "SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nSELECT * FROM analytics.orders\n) AS ld_src\nWHERE ts > '2024-01-01' LIMIT 1000"),
		ds("page-views", "SELECT *, 'page-views' LD_EVENT_KEY FROM (\nSELECT * FROM analytics.pages\n) ld_src"),
		ds("signups", "SELECT *, 'other' AS LD_EVENT_KEY FROM (\nSELECT * FROM analytics.signups\n) AS ld_src WHERE 1 = 1"),
	}})
	srv := dataSourceListServer(t, http.StatusOK, string(list))
	defer srv.Close()
	ld := launchdarkly.NewClient("api-x", "proj", srv.URL)
	mapping := map[string]string{"Checkout Events": "checkout-events", "Page Views": "page-views", "Signups": "signups"}
	info, err := fetchDataSourceKeys(context.Background(), ld, mapping, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"checkout-events": "checkout-events", "page-views": "page-views"}
	if len(info.keys) != 2 || info.keys["checkout-events"] != want["checkout-events"] || info.keys["page-views"] != want["page-views"] || info.constant != 2 {
		t.Errorf("keys = %v (constant %d), want %v", info.keys, info.constant, want)
	}
}

func TestProcessMetric_ConstantKeyNotesAreNotWarnings(t *testing.T) {
	var m statsig.Metric
	raw := `{"type":"user_warehouse","name":"Order Total","id":"Order Total::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout Events","valueColumn":"order_total"}}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	opts := constKeyConvOpts()
	opts.RegisteredAnalysisUnits = map[string]bool{"user": true}
	rpt := report.New()
	processMetric(context.Background(), m, opts, nil, rpt, "proj", true, 1, 1, new(int64))
	opts.DataSourceColumns = nil
	m2 := m
	m2.Name, m2.ID = "Order Total 2", "Order Total 2::user_warehouse"
	processMetric(context.Background(), m2, opts, nil, rpt, "proj", true, 1, 1, new(int64))
	rpt.Finalize(2)

	if rpt.Converted != 2 {
		t.Fatalf("report = %+v", rpt.Metrics)
	}
	for _, e := range rpt.Metrics {
		if slices.Contains(e.WarningCodes, converter.WarnConstantEventKey) || slices.Contains(e.WarningCodes, converter.WarnColumnUnverified) || !slices.Contains(e.NoteCodes, converter.WarnConstantEventKey) {
			t.Errorf("%s: warnings=%v notes=%v, want the constant-key codes as notes only", e.StatsigName, e.WarningCodes, e.NoteCodes)
		}
	}
	if n := metricsWithNoteCode(rpt, converter.WarnColumnUnverified); n != 1 {
		t.Errorf("unverified = %d, want the metric whose data source columns are unknown", n)
	}
}
