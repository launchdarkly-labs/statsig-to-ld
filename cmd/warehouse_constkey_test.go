package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/state"
)

// fakeLD's preview upper-cases an unquoted alias, as Snowflake does.
type fakeLD struct {
	t          *testing.T
	list       string
	listStatus int
	// metricsTotal, when set, overrides the reported totalCount.
	metrics       string
	metricsStatus int
	metricsTotal  int
	patchStatus   int
	patchBody     string
	previewCols   string
	// previewValue is the preview's guessed value column, as LaunchDarkly returns one.
	previewValue string

	created      []map[string]any
	patches      map[string][]launchdarkly.JSONPatchOp
	patchCount   int
	previews     []string
	metricsReads int
}

func (f *fakeLD) server() *httptest.Server {
	f.patches = map[string][]launchdarkly.JSONPatchOp{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const dsPath = "/internal/projects/proj/metric-data-sources"
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v2/integration-configurations/keys/snowflake-experimentation"):
			_, _ = io.WriteString(w, `{"items":[{"_id":"cfg-1","configValues":{"selectedEnv":{"projectKey":"proj","environmentKey":"production"}}}]}`)
		case strings.HasPrefix(r.URL.Path, "/api/v2/integration-configurations/keys/"):
			_, _ = io.WriteString(w, `{"items":[]}`)
		case strings.HasSuffix(r.URL.Path, "/metric-data-source-preview"):
			sql := r.URL.Query().Get("sqlQuery")
			f.previews = append(f.previews, sql)
			cols := f.previewCols
			if cols == "" {
				cols = `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT","length":64},{"name":"EVENT_KEY","type":"TEXT"}`
			}
			if strings.HasPrefix(sql, "SELECT *, '") {
				cols += `,{"name":"LD_EVENT_KEY","type":"TEXT","length":15}`
			}
			guess := ""
			if f.previewValue != "" {
				guess = `"valueColumn":"` + f.previewValue + `",`
			}
			_, _ = io.WriteString(w, `{"rows":[],"timestampColumn":"TS","keyColumn":"EVENT_KEY",`+guess+`"columns":[`+cols+`]}`)
		case r.URL.Path == dsPath && r.Method == http.MethodGet:
			if f.listStatus != 0 {
				w.WriteHeader(f.listStatus)
				_, _ = io.WriteString(w, `{"code":"forbidden","message":"Access to the requested resource was denied"}`)
				return
			}
			list := f.list
			if list == "" {
				list = `{"items":[]}`
			}
			_, _ = io.WriteString(w, list)
		case r.URL.Path == dsPath && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.created = append(f.created, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{}`)
		case strings.HasPrefix(r.URL.Path, dsPath+"/") && r.Method == http.MethodPatch:
			var ops []launchdarkly.JSONPatchOp
			if err := json.NewDecoder(r.Body).Decode(&ops); err != nil {
				f.t.Errorf("PATCH body is not a JSON Patch array: %v", err)
			}
			f.patches[strings.TrimPrefix(r.URL.Path, dsPath+"/")] = ops
			f.patchCount++
			if f.patchStatus != 0 {
				w.WriteHeader(f.patchStatus)
				_, _ = io.WriteString(w, f.patchBody)
				return
			}
			_, _ = io.WriteString(w, `{}`)
		case r.URL.Path == "/api/v2/metrics/proj" && r.Method == http.MethodGet:
			f.metricsReads++
			if f.metricsStatus != 0 {
				w.WriteHeader(f.metricsStatus)
				_, _ = io.WriteString(w, `{"code":"forbidden"}`)
				return
			}
			items := f.metrics
			if items == "" {
				items = "[]"
			}
			var parsed []any
			if err := json.Unmarshal([]byte(items), &parsed); err != nil {
				f.t.Fatalf("metrics: %v", err)
			}
			total := len(parsed)
			if f.metricsTotal != 0 {
				total = f.metricsTotal
			}
			if r.URL.Query().Get("offset") != "0" || r.URL.Query().Has("cursor") {
				f.t.Errorf("metrics read with %q, want offset=0 (the fake holds one page)", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, fmt.Sprintf(`{"items":%s,"_links":{},"totalCount":%d}`, items, total))
		default:
			f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func checkoutSource() map[string]any {
	return map[string]any{
		"name":            "Checkout Events",
		"sourceType":      "query",
		"sql":             "SELECT ts, user_id FROM analytics.events WHERE kind = 'checkout'; -- 90d",
		"timestampColumn": "ts",
		"idTypeMapping":   []any{map[string]any{"statsigUnitID": "userID", "column": "user_id"}},
	}
}

func newPhase3Engine(t *testing.T, srvURL string, resume bool) *migrationEngine {
	ld := launchdarkly.NewClient("api-x", "proj", srvURL)
	ld.EnvironmentKey = "production"
	return &migrationEngine{
		ld:               ld,
		ctx:              context.Background(),
		state:            state.NewMigrationState(resume),
		projectKey:       "proj",
		environmentKey:   "production",
		constantEventKey: true,
		metricSources:    []map[string]any{checkoutSource()},
	}
}

// unwrappedList is a data source created before the constant event key.
const unwrappedList = `{"items":[{"key":"checkout-events","sqlQuery":"SELECT * FROM analytics.events","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"},"columns":[]}}]}`

const wantCheckoutSQL = "SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nSELECT ts, user_id FROM analytics.events WHERE kind = 'checkout'\n) AS ld_src"

func TestPhase3_ConstantEventKey_WrapsSQLAndPinsKeyColumn(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}

	if e.report.DataSources.Created != 1 || len(f.created) != 1 {
		t.Fatalf("created %d (report %+v), want 1; errors=%v", len(f.created), e.report.DataSources, e.report.Errors)
	}
	body := f.created[0]
	if body["sqlQuery"] != wantCheckoutSQL {
		t.Errorf("sqlQuery =\n%v\nwant\n%s", body["sqlQuery"], wantCheckoutSQL)
	}
	cm := body["columnMappings"].(map[string]any)
	if cm["keyColumn"] != "LD_EVENT_KEY" {
		t.Errorf("keyColumn = %v, want LD_EVENT_KEY (the preview's EVENT_KEY guess must not win)", cm["keyColumn"])
	}
	if cm["timestampColumn"] != "TS" {
		t.Errorf("timestampColumn = %v, want TS", cm["timestampColumn"])
	}
	if cols := cm["columns"].([]any); len(cols) != 4 {
		t.Errorf("columns = %v, want the wrapped preview's 4 columns", cols)
	}
	if len(f.previews) != 2 || !strings.HasPrefix(f.previews[1], "SELECT *, '") {
		t.Errorf("previews = %q, want raw then wrapped", f.previews)
	}
}

// numericPreviewCols give the preview numeric columns to guess a value column from.
const numericPreviewCols = `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT"},{"name":"QUANTITY","type":"NUMBER"},{"name":"ORDER_TOTAL","type":"NUMBER"}`

func TestPhase3_ValueColumnOnlyWhenStatsigMapsOne(t *testing.T) {
	for _, tc := range []struct {
		name            string
		fields          []any
		want, wantDraft any
	}{
		{"not mapped", nil, nil, nil},
		{"mapped", []any{map[string]any{"fieldName": "amount", "column": "order_total"}}, "ORDER_TOTAL", "order_total"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, previewCols: numericPreviewCols, previewValue: "QUANTITY"}
			srv := f.server()
			defer srv.Close()

			e := newPhase3Engine(t, srv.URL, false)
			if tc.fields != nil {
				e.metricSources[0]["customFieldMapping"] = tc.fields
			}
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if len(f.created) != 1 {
				t.Fatalf("created %d, want 1; errors=%v", len(f.created), e.report.Errors)
			}
			cm := f.created[0]["columnMappings"].(map[string]any)
			if got, present := cm["valueColumn"]; got != tc.want || present != (tc.want != nil) {
				t.Errorf("POSTed valueColumn = %v (present %v), want %v", got, present, tc.want)
			}
			if len(e.report.Warnings) != 0 || len(e.report.Notes) != 0 {
				t.Errorf("warnings=%q notes=%+v, want none", e.report.Warnings, e.report.Notes)
			}

			e.writeDryRunBodies("snowflake")
			raw, err := os.ReadFile("data-source-bodies.json")
			if err != nil {
				t.Fatal(err)
			}
			var bodies []map[string]any
			if err := json.Unmarshal(raw, &bodies); err != nil || len(bodies) != 1 {
				t.Fatalf("data-source-bodies.json = %s (%v)", raw, err)
			}
			cm = bodies[0]["columnMappings"].(map[string]any)
			if got, present := cm["valueColumn"]; got != tc.wantDraft || present != (tc.wantDraft != nil) {
				t.Errorf("dry-run valueColumn = %v (present %v), want %v", got, present, tc.wantDraft)
			}
		})
	}
}

func TestPhase3_OverwriteAddsNoGuessedValueColumn(t *testing.T) {
	for _, old := range []string{"", "AMT"} {
		t.Run(fmt.Sprintf("existing %q", old), func(t *testing.T) {
			t.Chdir(t.TempDir())
			cm := map[string]any{"keyColumn": "EVENT_NAME", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"}, "columns": []any{}}
			if old != "" {
				cm["valueColumn"] = old
			}
			list, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
				"key": "checkout-events", "sqlQuery": "SELECT * FROM analytics.events", "columnMappings": cm,
			}}})
			f := &fakeLD{t: t, list: string(list), previewCols: numericPreviewCols, previewValue: "QUANTITY"}
			srv := f.server()
			defer srv.Close()

			e := newPhase3Engine(t, srv.URL, false)
			e.overwrite = true
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if e.report.DataSources.Updated != 1 {
				t.Fatalf("report %+v errors=%q, want one update", e.report.DataSources, e.report.Errors)
			}
			var ops []launchdarkly.JSONPatchOp
			for _, op := range f.patches["checkout-events"] {
				if op.Path == "/columnMappings/valueColumn" {
					ops = append(ops, op)
				}
			}
			if old == "" && len(ops) != 0 || old != "" && (len(ops) != 1 || ops[0].Op != "remove") {
				t.Errorf("valueColumn ops = %+v, want none added, and a column the query no longer returns removed", ops)
			}
		})
	}
}

// valueColumnList is an unwrapped data source whose value column the new query does not return.
const valueColumnList = `{"items":[{"key":"checkout-events","sqlQuery":"SELECT * FROM analytics.events","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","valueColumn":"AMT","contexts":{"user":"USER_ID"},"columns":[]}}]}`

func TestPhase3_OverwriteKeepsValueColumnBoundMetricsRead(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		t.Chdir(t.TempDir())
		f := &fakeLD{t: t, list: valueColumnList, metrics: `[
			{"key":"order-total","eventKey":"checkout-events","isNumeric":true,"unitAggregationType":"sum","dataSource":{"key":"checkout-events"}},
			{"key":"items-per-checkout","eventKey":"checkout-events","isNumeric":true,"unitAggregationType":"sum","dataSource":{"key":"checkout-events"},
			 "denominator":{"eventName":"checkout","isNumeric":true,"unitAggregationType":"sum","dataSource":{"key":"launchdarkly-hosted"}}},
			{"key":"revenue-per-order","eventKey":"orders","isNumeric":true,"valueColumn":"AMOUNT","dataSource":{"key":"orders"},
			 "denominator":{"eventName":"checkout-events","isNumeric":true,"unitAggregationType":"sum","dataSource":{"key":"checkout-events"}}},
			{"key":"checkouts","eventKey":"checkout-events","isNumeric":false,"dataSource":{"key":"checkout-events"}}]`}
		srv := f.server()
		defer srv.Close()

		e := newPhase3Engine(t, srv.URL, false)
		e.overwrite, e.forceOverwrite = true, true
		if err := e.phase3aMigrateDataSources(); err != nil {
			t.Fatal(err)
		}
		want := `Data source "Checkout Events": not updated: its value column "AMT" is not in the updated query, and 3 bound numeric metric(s) with no value column of their own read it: items-per-checkout (numerator and denominator), order-total, revenue-per-order (denominator). Keep that column in the Statsig SQL, or set those metrics' value column in LaunchDarkly and refresh them on running experiments, then rerun`
		if f.patchCount != 0 || e.report.DataSources.Failed != 1 || len(e.report.Errors) != 1 || e.report.Errors[0] != want {
			t.Errorf("patches=%d report %+v errors=%q\nwant %q", f.patchCount, e.report.DataSources, e.report.Errors, want)
		}
		if f.metricsReads != 1 {
			t.Errorf("metrics read %d times, want once per run", f.metricsReads)
		}
	})

	for _, tc := range []struct{ name, metrics string }{
		{"no numeric metric", `[{"key":"checkouts","eventKey":"checkout-events","isNumeric":false,"dataSource":{"key":"checkout-events"}}]`},
		{"count_distinct", `[{"key":"buyers","eventKey":"checkout-events","isNumeric":true,"unitAggregationType":"count_distinct","unitAggregationField":"USER_ID","dataSource":{"key":"checkout-events"}}]`},
		{"own value column", `[{"key":"order-total","eventKey":"checkout-events","isNumeric":true,"unitAggregationType":"sum","valueColumn":"ORDER_TOTAL","dataSource":{"key":"checkout-events"}}]`},
		{"denominator inherits another data source", `[{"key":"revenue-per-order","eventKey":"orders","isNumeric":true,"valueColumn":"AMOUNT","dataSource":{"key":"orders"},
			"denominator":{"eventName":"orders","isNumeric":true,"unitAggregationType":"sum","dataSource":{"key":"launchdarkly-hosted"}}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: valueColumnList, metrics: tc.metrics}
			srv := f.server()
			defer srv.Close()

			e := newPhase3Engine(t, srv.URL, false)
			e.overwrite = true
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if e.report.DataSources.Updated != 1 || f.patchCount != 1 {
				t.Fatalf("report %+v errors=%q patches=%d, want one update", e.report.DataSources, e.report.Errors, f.patchCount)
			}
			if !slices.ContainsFunc(f.patches["checkout-events"], func(op launchdarkly.JSONPatchOp) bool {
				return op.Op == "remove" && op.Path == "/columnMappings/valueColumn"
			}) {
				t.Errorf("ops = %+v, want the value column removed", f.patches["checkout-events"])
			}
		})
	}
}

// With --constant-event-key=false, metrics are read only when the value column would be removed.
func TestPhase3_ValueColumnRemovalFailsClosedWhenMetricsCannotBeRead(t *testing.T) {
	for _, tc := range []struct {
		name, previewCols string
		wantRefused       bool
	}{
		{"value column removed", `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT"},{"name":"EVENT_NAME","type":"TEXT"}`, true},
		{"value column kept", `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT"},{"name":"EVENT_NAME","type":"TEXT"},{"name":"AMT","type":"NUMBER"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: valueColumnList, previewCols: tc.previewCols, metricsStatus: http.StatusForbidden}
			srv := f.server()
			defer srv.Close()

			e := newPhase3Engine(t, srv.URL, false)
			e.overwrite, e.constantEventKey = true, false
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if !tc.wantRefused {
				if e.report.DataSources.Updated != 1 || f.metricsReads != 0 {
					t.Errorf("report %+v errors=%q metricsReads=%d, want an update without reading metrics", e.report.DataSources, e.report.Errors, f.metricsReads)
				}
				return
			}
			if f.patchCount != 0 || e.report.DataSources.Failed != 1 || f.metricsReads != 1 {
				t.Fatalf("patches=%d report %+v metricsReads=%d, want a refusal", f.patchCount, e.report.DataSources, f.metricsReads)
			}
			if !strings.Contains(e.report.Errors[0], "not updated, because its bound metrics could not be checked") || !strings.Contains(e.report.Errors[0], "403") {
				t.Errorf("error = %q", e.report.Errors[0])
			}
		})
	}
}

func TestPhase3_DroppedStatsigValueColumnsOneSummaryLine(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	amount := []any{map[string]any{"fieldName": "amount", "column": "amount"}}
	e.metricSources[0]["customFieldMapping"] = amount
	orders := checkoutSource()
	orders["name"], orders["customFieldMapping"] = "Orders", amount
	views := checkoutSource()
	views["name"] = "Page Views"
	e.metricSources = append(e.metricSources, orders, views)
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Created != 3 {
		t.Fatalf("report %+v errors=%q, want three created", e.report.DataSources, e.report.Errors)
	}
	for _, body := range f.created {
		if vc, ok := body["columnMappings"].(map[string]any)["valueColumn"]; ok {
			t.Errorf("%v POSTed valueColumn %v, want none", body["key"], vc)
		}
	}
	want := "2 data source(s) did not get the value column their Statsig source defines, because their query does not return it: checkout-events, orders. Add the column to the Statsig source's query and to the data source's in LaunchDarkly, or set a value column on their numeric metrics."
	if len(e.report.Warnings) != 1 || e.report.Warnings[0] != want {
		t.Errorf("warnings = %q\nwant [%q]", e.report.Warnings, want)
	}
	wantNotes := []reportNote{{Code: noteValueColumnNotInQuery, DataSource: "checkout-events"}, {Code: noteValueColumnNotInQuery, DataSource: "orders"}}
	if !reflect.DeepEqual(e.report.Notes, wantNotes) {
		t.Errorf("notes = %+v, want %+v", e.report.Notes, wantNotes)
	}
}

func TestPhase3_ListFailureStopsThePhase(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, listStatus: http.StatusForbidden}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	err := e.phase3aMigrateDataSources()
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the list failure", err)
	}
	if len(f.created) != 0 || len(f.previews) != 0 {
		t.Errorf("nothing may be previewed or created after a failed list: created=%d previews=%d", len(f.created), len(f.previews))
	}
}

func TestPhase3_ExistingUnwrappedSource_WarnsWhatOverwriteWouldDo(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: unwrappedList}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Skipped != 1 || len(f.created) != 0 || f.patchCount != 0 || len(f.previews) != 0 {
		t.Fatalf("report %+v created=%d patches=%d previews=%d, want one skip and no warehouse or write calls", e.report.DataSources, len(f.created), f.patchCount, len(f.previews))
	}
	if len(e.report.Warnings) != 1 {
		t.Fatalf("warnings = %q, want one summary line", e.report.Warnings)
	}
	w := e.report.Warnings[0]
	for _, want := range []string{"1 data source(s) already exist without the constant event key", "rerun with --overwrite (it refuses any whose bound metrics use other event keys)", "built by hand"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not contain %q", w, want)
		}
	}
	if len(e.report.Notes) != 1 || !reflect.DeepEqual(e.report.Notes[0], reportNote{Code: noteExistsWithoutConstantKey, DataSource: "checkout-events"}) {
		t.Errorf("notes = %+v, want the source recorded", e.report.Notes)
	}
}

func TestPhase3_ExistingUnwrappedSources_OneSummaryLine(t *testing.T) {
	t.Chdir(t.TempDir())
	list := `{"items":[
		{"key":"checkout-events","sqlQuery":"SELECT * FROM analytics.events","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}},
		{"key":"orders","sqlQuery":"SELECT * FROM analytics.orders","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}}]}`
	f := &fakeLD{t: t, list: list}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	orders := checkoutSource()
	orders["name"] = "Orders"
	e.metricSources = append(e.metricSources, orders)
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Skipped != 2 || len(e.report.Warnings) != 1 || !strings.HasPrefix(e.report.Warnings[0], "2 data source(s)") {
		t.Errorf("report %+v warnings=%q, want two skips and one summary line", e.report.DataSources, e.report.Warnings)
	}
}

func TestPhase3_ResumeStillChecksExistingSources(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("migration_state.json", []byte(`{"data_sources_created":["checkout-events"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeLD{t: t, list: unwrappedList}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, true)
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if len(e.report.Warnings) != 1 {
		t.Errorf("resumed run skipped an unwrapped existing source without warning (report %+v)", e.report.DataSources)
	}

	f2 := &fakeLD{t: t, list: unwrappedList}
	srv2 := f2.server()
	defer srv2.Close()
	e = newPhase3Engine(t, srv2.URL, true)
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Updated != 1 || f2.patchCount != 1 {
		t.Errorf("resumed --overwrite run: report %+v patches=%d, want one update", e.report.DataSources, f2.patchCount)
	}
}

func wrappedCheckoutList(t *testing.T, cm map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"items": []any{map[string]any{
		"key": "checkout-events", "sqlQuery": wantCheckoutSQL, "columnMappings": cm,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPhase3_OverwriteNeverRewritesSourceWithConstantKey(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh state", true: "resume"}[resume], func(t *testing.T) {
			t.Chdir(t.TempDir())
			if resume {
				if err := os.WriteFile("migration_state.json", []byte(`{"data_sources_created":["checkout-events"]}`), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			f := &fakeLD{t: t, list: wrappedCheckoutList(t, map[string]any{
				"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "valueColumn": "ORDER_TOTAL",
				"contexts": map[string]any{"user": "USER_ID", "account": "ACCOUNT_ID"}, "columns": []any{},
			})}
			srv := f.server()
			defer srv.Close()

			e := newPhase3Engine(t, srv.URL, resume)
			e.overwrite = true
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if e.report.DataSources.Skipped != 1 || f.patchCount != 0 || len(f.previews) != 0 || f.metricsReads != 0 {
				t.Errorf("report %+v patches=%d previews=%d metricsReads=%d, want a skip with no calls", e.report.DataSources, f.patchCount, len(f.previews), f.metricsReads)
			}
			if len(e.report.Warnings) != 0 {
				t.Errorf("warnings = %q, want none (the Statsig SQL is unchanged)", e.report.Warnings)
			}
		})
	}
}

func TestPhase3_SourceWithConstantKeyWarnsWhenStatsigSQLChanged(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: wrappedCheckoutList(t, map[string]any{"keyColumn": "LD_EVENT_KEY"})}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.overwrite = true
	// Whitespace alone is not a change.
	e.metricSources[0]["sql"] = "SELECT ts,  user_id\n  FROM analytics.events WHERE kind = 'checkout';"
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if len(e.report.Warnings) != 0 {
		t.Fatalf("whitespace-only difference warned: %q", e.report.Warnings)
	}

	e = newPhase3Engine(t, srv.URL, false)
	e.overwrite = true
	e.metricSources[0]["sql"] = "SELECT ts, user_id FROM analytics.events WHERE kind = 'checkout' AND region = 'eu'"
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if f.patchCount != 0 || len(e.report.Warnings) != 1 || !strings.Contains(e.report.Warnings[0], "Statsig SQL has changed") {
		t.Errorf("patches=%d warnings=%q, want no PATCH and a changed-SQL warning", f.patchCount, e.report.Warnings)
	}
}

func TestPhase3_ConstantKeyOffNeverUnwrapsSource(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: wrappedCheckoutList(t, map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS"}),
		metrics: `[{"key":"checkouts-count","eventKey":"checkout-events","dataSource":{"key":"checkout-events"}}]`}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.constantEventKey = false
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if f.patchCount != 0 || e.report.DataSources.Skipped != 1 {
		t.Fatalf("patches=%v report %+v, want the wrapped source left alone", f.patches, e.report.DataSources)
	}
	if len(e.report.Warnings) != 0 || len(e.report.Notes) != 1 || e.report.Notes[0].Code != noteKeptConstantKey {
		t.Errorf("warnings=%q notes=%+v, want no warning and a %s note", e.report.Warnings, e.report.Notes, noteKeptConstantKey)
	}
}

func TestPhase3_OverwriteKeepsExistingMappings(t *testing.T) {
	t.Chdir(t.TempDir())
	list, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"key": "checkout-events", "sqlQuery": "SELECT * FROM analytics.events",
		"columnMappings": map[string]any{"keyColumn": "EVENT_KEY", "timestampColumn": "TS", "valueColumn": "ORDER_TOTAL",
			"contexts": map[string]any{"user": "USER_ID", "account": "account_id"}, "columns": []any{}},
	}}})
	f := &fakeLD{t: t, list: string(list),
		previewCols: `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT"},{"name":"ACCOUNT_ID","type":"TEXT"},{"name":"ORDER_TOTAL","type":"NUMBER"},{"name":"EVENT_KEY","type":"TEXT"}`}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Updated != 1 || len(f.created) != 0 {
		t.Fatalf("report %+v created=%d, want one update; errors=%v", e.report.DataSources, len(f.created), e.report.Errors)
	}
	ops := f.patches["checkout-events"]
	want := []struct{ op, path string }{
		{"test", "/sqlQuery"},
		{"test", "/columnMappings"},
		{"add", "/sqlQuery"},
		{"add", "/columnMappings/keyColumn"},
		{"replace", "/columnMappings/columns"},
		{"add", "/columnMappings/contexts/account"},
	}
	if len(ops) != len(want) {
		t.Fatalf("ops = %+v, want %v", ops, want)
	}
	for i, w := range want {
		if ops[i].Op != w.op || ops[i].Path != w.path {
			t.Errorf("op %d = %s %s, want %s %s", i, ops[i].Op, ops[i].Path, w.op, w.path)
		}
	}
	if ops[2].Value != wantCheckoutSQL || ops[3].Value != "LD_EVENT_KEY" || ops[5].Value != "ACCOUNT_ID" {
		t.Errorf("values = %v / %v / %v", ops[2].Value, ops[3].Value, ops[5].Value)
	}
	if cols := ops[4].Value.([]any); len(cols) != 6 {
		t.Errorf("columns = %v, want the wrapped preview's 6 columns", cols)
	}
	if len(e.report.Warnings) != 0 {
		t.Errorf("warnings = %q, want none", e.report.Warnings)
	}
}

func TestDataSourcePatch_FallsBackWhenExistingColumnsAreGone(t *testing.T) {
	existing := map[string]any{"key": "k", "tableName": "DB.ORDERS", "columnMappings": map[string]any{
		"keyColumn": "EVENT_NAME", "timestampColumn": "CREATED_AT", "valueColumn": "AMT",
		"contexts": map[string]any{"user": "UID", "account": "ACCT"},
	}}
	body := map[string]any{"key": "k", "sqlQuery": "SELECT 1", "columnMappings": map[string]any{
		"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS",
		"contexts": map[string]string{"user": "USER_ID"},
		"columns":  []map[string]any{{"name": "TS", "type": "TIMESTAMP_NTZ"}, {"name": "USER_ID", "type": "TEXT"}, {"name": "LD_EVENT_KEY", "type": "TEXT"}},
	}}
	res, err := dataSourcePatch(existing, body, true)
	if err != nil {
		t.Fatal(err)
	}
	ops, notes := res.ops, res.changes
	want := []launchdarkly.JSONPatchOp{
		{Op: "remove", Path: "/tableName"},
		{Op: "add", Path: "/sqlQuery", Value: "SELECT 1"},
		{Op: "add", Path: "/columnMappings/keyColumn", Value: "LD_EVENT_KEY"},
		{Op: "replace", Path: "/columnMappings/columns"},
		{Op: "replace", Path: "/columnMappings/timestampColumn", Value: "TS"},
		{Op: "remove", Path: "/columnMappings/valueColumn"},
		{Op: "remove", Path: "/columnMappings/contexts/account"},
		{Op: "add", Path: "/columnMappings/contexts/user", Value: "USER_ID"},
	}
	if len(ops) != len(want) {
		t.Fatalf("ops = %+v\nwant %+v", ops, want)
	}
	for i := range want {
		if ops[i].Op != want[i].Op || ops[i].Path != want[i].Path || (want[i].Value != nil && ops[i].Value != want[i].Value) {
			t.Errorf("op %d = %+v, want %+v", i, ops[i], want[i])
		}
	}
	if len(notes) != 4 {
		t.Errorf("notes = %q, want one per changed mapping", notes)
	}

	// No existing context column survives: the contexts are replaced whole.
	existing["columnMappings"].(map[string]any)["contexts"] = map[string]any{"account": "ACCT"}
	res, _ = dataSourcePatch(existing, body, true)
	ops = res.ops
	last := ops[len(ops)-1]
	if last.Op != "replace" || last.Path != "/columnMappings/contexts" {
		t.Errorf("last op = %+v, want replace /columnMappings/contexts", last)
	}
}

func TestDataSourcePatch_ConstantKeyOffKeepsKeyColumn(t *testing.T) {
	existing := map[string]any{"sqlQuery": "SELECT * FROM t", "columnMappings": map[string]any{
		"keyColumn": "event_name", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"},
	}}
	body := map[string]any{"sqlQuery": "SELECT * FROM u", "columnMappings": map[string]any{
		"keyColumn": "EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]string{"user": "USER_ID"},
		"columns": []map[string]any{{"name": "TS"}, {"name": "USER_ID"}, {"name": "EVENT_NAME"}, {"name": "EVENT_KEY"}},
	}}
	res, err := dataSourcePatch(existing, body, false)
	if err != nil {
		t.Fatal(err)
	}
	ops := res.ops
	found := false
	for _, op := range ops {
		if op.Path == "/columnMappings/keyColumn" {
			found = true
			if op.Value != "EVENT_NAME" {
				t.Errorf("keyColumn op = %+v, want the existing column in the new case", op)
			}
		}
	}
	if !found {
		t.Error("no keyColumn op, want the existing column rewritten to the new case")
	}

	body["columnMappings"].(map[string]any)["columns"] = []map[string]any{{"name": "TS"}, {"name": "USER_ID"}, {"name": "EVENT_KEY"}}
	res, err = dataSourcePatch(existing, body, false)
	if ops = res.ops; ops != nil || err == nil || err.Error() != `not updated: its key column "event_name" is not in the updated query; metrics on it would filter a different column. Keep that column in the Statsig SQL, or update the data source by hand` {
		t.Errorf("ops=%v err=%v, want no ops and the key column refusal", ops, err)
	}
}

const boundCheckoutMetrics = `[
	{"key":"order-total-sum","eventKey":"order_total","dataSource":{"key":"checkout-events"}},
	{"key":"checkouts-count","eventKey":"checkout-events","dataSource":{"key":"checkout-events"}},
	{"key":"avg-order-value","eventKey":"order_total","dataSource":{"key":"orders"},
	 "denominator":{"eventName":"checkout","dataSource":{"key":"checkout-events"}}},
	{"key":"page-views","eventKey":"page_view","dataSource":{"key":"page-views"}}]`

// Metrics bound through a ratio's denominator count too.
func TestPhase3_OverwriteRefusesWhenBoundMetricsWouldMatchNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: unwrappedList, metrics: boundCheckoutMetrics}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if f.patchCount != 0 || len(f.previews) != 0 || e.report.DataSources.Failed != 1 {
		t.Fatalf("patches=%d previews=%d report %+v, want a refusal before any call", f.patchCount, len(f.previews), e.report.DataSources)
	}
	msg := strings.Join(e.report.Errors, " ")
	for _, want := range []string{"order-total-sum", "avg-order-value", `denominator event name "checkout"`, "would match no rows", "older versions", "--force-overwrite"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "checkouts-count") || strings.Contains(msg, "page-views") {
		t.Errorf("error %q names a metric that is fine or unbound", msg)
	}
}

func TestPhase3_ForceOverwriteUpdatesAndNamesStaleMetrics(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: unwrappedList, metrics: boundCheckoutMetrics}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.overwrite, e.forceOverwrite = true, true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Updated != 1 || f.patchCount != 1 {
		t.Fatalf("report %+v patches=%d, want one update", e.report.DataSources, f.patchCount)
	}
	if len(e.report.Warnings) != 1 || !strings.Contains(e.report.Warnings[0], "order-total-sum") || !strings.Contains(e.report.Warnings[0], "avg-order-value") {
		t.Errorf("warnings = %q, want the stale metrics listed", e.report.Warnings)
	}
}

// The project's metrics are read once per run.
func TestPhase3_OverwriteFailsClosedWhenMetricsCannotBeRead(t *testing.T) {
	t.Chdir(t.TempDir())
	list := `{"items":[
		{"key":"checkout-events","sqlQuery":"SELECT * FROM analytics.events","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}},
		{"key":"orders","sqlQuery":"SELECT * FROM analytics.orders","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}}]}`
	f := &fakeLD{t: t, list: list, metricsStatus: http.StatusForbidden}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	orders := checkoutSource()
	orders["name"] = "Orders"
	e.metricSources = append(e.metricSources, orders)
	e.overwrite, e.forceOverwrite = true, true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if f.patchCount != 0 || e.report.DataSources.Failed != 2 {
		t.Fatalf("patches=%d report %+v, want both refused", f.patchCount, e.report.DataSources)
	}
	if !strings.Contains(e.report.Errors[0], "not updated, because its bound metrics could not be checked") || !strings.Contains(e.report.Errors[0], "403") {
		t.Errorf("error = %q", e.report.Errors[0])
	}
	if f.metricsReads != 1 {
		t.Errorf("metrics read %d times, want once per run", f.metricsReads)
	}
}

// A short list is read once more before the update is refused.
func TestPhase3_OverwriteFailsClosedOnAnIncompleteMetricList(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: unwrappedList, metrics: `[{"key":"checkouts-count","eventKey":"checkout-events","dataSource":{"key":"checkout-events"}}]`, metricsTotal: 2}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.overwrite, e.forceOverwrite = true, true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if f.patchCount != 0 || e.report.DataSources.Failed != 1 || f.metricsReads != 2 {
		t.Fatalf("patches=%d report %+v metricsReads=%d, want a refusal after one re-read", f.patchCount, e.report.DataSources, f.metricsReads)
	}
	if !strings.Contains(e.report.Errors[0], "could not read a complete list of the project's metrics: got 1 of 2") {
		t.Errorf("error = %q", e.report.Errors[0])
	}
}

func TestPhase3_DuplicateSanitizedKeysFailTheWholeGroup(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: unwrappedList}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	twin := checkoutSource()
	twin["name"] = "checkout events"
	twin["sql"] = "SELECT ts, user_id FROM analytics.refunds"
	orders := checkoutSource()
	orders["name"] = "Orders"
	e.metricSources = append(e.metricSources, twin, orders)
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if f.patchCount != 0 || e.report.DataSources.Failed != 2 || e.report.DataSources.Created != 1 {
		t.Fatalf("patches=%d report %+v, want both twins failed and Orders created; errors=%v", f.patchCount, e.report.DataSources, e.report.Errors)
	}
	for _, msg := range e.report.Errors {
		if !strings.Contains(msg, `"Checkout Events", "checkout events"`) {
			t.Errorf("error %q does not name both colliding sources", msg)
		}
	}

	if err := e.writeSourceMapping(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("source-mapping.json")
	if err != nil {
		t.Fatal(err)
	}
	var mapping map[string]string
	if err := json.Unmarshal(raw, &mapping); err != nil {
		t.Fatal(err)
	}
	if len(mapping) != 1 || mapping["Orders"] != "orders" {
		t.Errorf("source-mapping.json = %v, want only Orders", mapping)
	}
}

func TestDryRun_ReportsPlannedOutcomes(t *testing.T) {
	t.Chdir(t.TempDir())
	list := `{"items":[
		{"key":"checkout-events","sqlQuery":"SELECT * FROM analytics.events","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}},
		{"key":"orders","sqlQuery":"SELECT *, 'orders' AS LD_EVENT_KEY FROM (\nSELECT * FROM analytics.orders\n) AS ld_src","columnMappings":{"keyColumn":"LD_EVENT_KEY","timestampColumn":"TS","contexts":{"user":"USER_ID"}}},
		{"key":"refunds","sqlQuery":"SELECT * FROM analytics.refunds","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}}]}`
	f := &fakeLD{t: t, list: list, metrics: `[{"key":"order-total-sum","eventKey":"order_total","dataSource":{"key":"checkout-events"}}]`}
	srv := f.server()
	defer srv.Close()

	named := func(name, sql string) map[string]any {
		s := checkoutSource()
		s["name"], s["sql"] = name, sql
		return s
	}
	e := newPhase3Engine(t, srv.URL, false)
	e.dryRun, e.overwrite, e.ldReadable = true, true, true
	e.metricSources = []map[string]any{
		checkoutSource(),
		named("Orders", "SELECT * FROM analytics.orders"),
		named("Refunds", "SELECT * FROM analytics.refunds"),
		named("Page Views", "SELECT * FROM analytics.page_views"),
		named("Signups", "SELECT * FROM analytics.signups"),
		named("signups", "SELECT * FROM analytics.signups_v2"),
	}
	l := e.dryRunDataSources("snowflake")
	want := []string{
		"Checkout Events (query): would refuse (bound metrics: order-total-sum (event key \"order_total\"))",
		"Orders (query): would skip (already has the constant event key)",
		"Refunds (query): would update",
		"Page Views (query): would create",
		"Signups (query): would refuse (key collision: \"Signups\", \"signups\")",
		"signups (query): would refuse (key collision: \"Signups\", \"signups\")",
	}
	if !l.checked || strings.Join(l.lines, "\n") != strings.Join(want, "\n") || l.note != "" {
		t.Errorf("checked=%v note=%q lines:\n%s\nwant:\n%s", l.checked, l.note, strings.Join(l.lines, "\n"), strings.Join(want, "\n"))
	}
	if f.patchCount != 0 || len(f.created) != 0 || len(f.previews) != 0 {
		t.Errorf("dry run wrote or previewed: patches=%d created=%d previews=%d", f.patchCount, len(f.created), len(f.previews))
	}

	e.ldReadable = false
	l = e.dryRunDataSources("snowflake")
	if l.checked || !strings.Contains(l.note, "were not checked") || l.lines[0] != "Checkout Events (query): would create" {
		t.Errorf("without credentials: checked=%v note=%q lines=%q", l.checked, l.note, l.lines)
	}
}

func TestDataSourcePatch_SwitchesTableSourceToSQL(t *testing.T) {
	existing := map[string]any{"key": "k", "tableName": "DB.ORDERS", "columnMappings": map[string]any{"timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"}}}
	body := map[string]any{"key": "k", "sqlQuery": "SELECT 1", "columnMappings": map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS",
		"columns": []map[string]any{{"name": "TS"}, {"name": "USER_ID"}, {"name": "LD_EVENT_KEY"}}}}
	res, _ := dataSourcePatch(existing, body, true)
	ops := res.ops
	want := []launchdarkly.JSONPatchOp{
		{Op: "remove", Path: "/tableName"},
		{Op: "add", Path: "/sqlQuery", Value: "SELECT 1"},
		{Op: "add", Path: "/columnMappings/keyColumn", Value: "LD_EVENT_KEY"},
		{Op: "replace", Path: "/columnMappings/columns"},
	}
	if len(ops) != len(want) {
		t.Fatalf("ops = %+v, want %+v", ops, want)
	}
	for i := range want {
		if ops[i].Op != want[i].Op || ops[i].Path != want[i].Path {
			t.Errorf("op %d = %+v, want %+v", i, ops[i], want[i])
		}
	}
}

func TestWarehouseCmd_FlagsBound(t *testing.T) {
	for _, name := range []string{
		"statsig-key", "statsig-url", "statsig-export-file",
		"ld-key", "ld-url", "ld-project", "ld-environment", "ld-maintainer",
		"warehouse-type", "dry-run", "resume", "only",
		"overwrite", "force-overwrite", "update-mappings", "constant-event-key", "verbose", "no-color",
	} {
		if warehouseCmd.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s not registered on `warehouse`", name)
		}
	}
}

// Data source keys are unique per project, not per environment.
func TestPhase3_KeyTakenInAnotherEnvironmentIsRefused(t *testing.T) {
	list := `{"items":[{"key":"checkout-events","environmentKey":"staging","sqlQuery":"SELECT * FROM analytics.events","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}}]}`
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("overwrite=%v", overwrite), func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: list}
			srv := f.server()
			defer srv.Close()

			e := newPhase3Engine(t, srv.URL, false)
			orders := checkoutSource()
			orders["name"] = "Orders"
			e.metricSources = append(e.metricSources, orders)
			e.overwrite = overwrite
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if f.patchCount != 0 || f.metricsReads != 0 || e.report.DataSources.Failed != 1 || e.report.DataSources.Created != 1 || len(f.created) != 1 || f.created[0]["key"] != "orders" {
				t.Fatalf("patches=%d metricsReads=%d report %+v created=%v, want checkout-events refused and only orders created", f.patchCount, f.metricsReads, e.report.DataSources, f.created)
			}
			want := `Data source "Checkout Events": not created: the key "checkout-events" is taken by a data source in LaunchDarkly environment "staging", which was left unchanged, and this source was left out of source-mapping.json. Rename the source in Statsig, or run with --ld-environment staging`
			if len(e.report.Errors) != 1 || e.report.Errors[0] != want {
				t.Errorf("errors = %q\nwant %q", e.report.Errors, want)
			}
			if len(e.report.Warnings) != 0 {
				t.Errorf("warnings = %q, want none", e.report.Warnings)
			}
			if err := e.writeSourceMapping(); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile("source-mapping.json")
			var mapping map[string]string
			_ = json.Unmarshal(raw, &mapping)
			if len(mapping) != 1 || mapping["Orders"] != "orders" {
				t.Errorf("source-mapping.json = %v, want only Orders", mapping)
			}
		})
	}

	t.Run("same environment", func(t *testing.T) {
		t.Chdir(t.TempDir())
		f := &fakeLD{t: t, list: strings.Replace(list, "staging", "production", 1)}
		srv := f.server()
		defer srv.Close()
		e := newPhase3Engine(t, srv.URL, false)
		e.overwrite = true
		if err := e.phase3aMigrateDataSources(); err != nil {
			t.Fatal(err)
		}
		if e.report.DataSources.Updated != 1 {
			t.Errorf("report %+v errors=%q, want one update", e.report.DataSources, e.report.Errors)
		}
	})

	t.Run("dry run", func(t *testing.T) {
		t.Chdir(t.TempDir())
		f := &fakeLD{t: t, list: list}
		srv := f.server()
		defer srv.Close()
		e := newPhase3Engine(t, srv.URL, false)
		e.dryRun, e.ldReadable = true, true
		l := e.dryRunDataSources("snowflake")
		if len(l.lines) != 1 || l.lines[0] != `Checkout Events (query): would refuse (key used in environment "staging")` || !e.unmapped["checkout-events"] {
			t.Errorf("lines=%q unmapped=%v", l.lines, e.unmapped)
		}
	})
}

// Kept without a PATCH or warning even when its Statsig SQL has changed too.
func TestPhase3_EditedWrapperIsKeptWithoutWarning(t *testing.T) {
	edited := wantCheckoutSQL + "\nWHERE ts > '2024-01-01'"
	list, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"key": "checkout-events", "sqlQuery": edited,
		"columnMappings": map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"}, "columns": []any{}},
	}}})
	for _, tc := range []struct {
		name        string
		constantKey bool
	}{{"constant key", true}, {"constant key off", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: string(list)}
			srv := f.server()
			defer srv.Close()

			e := newPhase3Engine(t, srv.URL, false)
			e.overwrite, e.forceOverwrite, e.constantEventKey = true, true, tc.constantKey
			e.metricSources[0]["sql"] = "SELECT ts, user_id FROM analytics.events WHERE kind = 'checkout' AND region = 'eu'"
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if f.patchCount != 0 || len(f.previews) != 0 || f.metricsReads != 0 || e.report.DataSources.Skipped != 1 {
				t.Fatalf("patches=%d previews=%d metricsReads=%d report %+v, want a skip with no calls", f.patchCount, len(f.previews), f.metricsReads, e.report.DataSources)
			}
			if len(e.report.Warnings) != 0 || len(e.report.Errors) != 0 {
				t.Errorf("warnings=%q errors=%q, want none", e.report.Warnings, e.report.Errors)
			}
			if len(e.report.Notes) != 1 || !reflect.DeepEqual(e.report.Notes[0], reportNote{Code: noteEditedConstantKey, DataSource: "checkout-events"}) {
				t.Errorf("notes = %+v", e.report.Notes)
			}
		})
	}

	t.Run("dry run", func(t *testing.T) {
		t.Chdir(t.TempDir())
		f := &fakeLD{t: t, list: string(list)}
		srv := f.server()
		defer srv.Close()
		e := newPhase3Engine(t, srv.URL, false)
		e.dryRun, e.ldReadable, e.overwrite = true, true, true
		l := e.dryRunDataSources("snowflake")
		if len(l.lines) != 1 || l.lines[0] != "Checkout Events (query): would skip (kept: edited in LaunchDarkly)" || len(l.warnings) != 0 {
			t.Errorf("lines=%q warnings=%q", l.lines, l.warnings)
		}
	})
}

func TestPhase3_UpdateTestsTheListedStateFirst(t *testing.T) {
	t.Chdir(t.TempDir())
	listedCM := map[string]any{"keyColumn": "EVENT_NAME", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"},
		"columns": []any{map[string]any{"name": "TS", "type": "TIMESTAMP_NTZ", "nullable": false}, map[string]any{"name": "USER_ID", "type": "TEXT", "length": 16777216, "nullable": true}}}
	list, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"key": "checkout-events", "environmentKey": "production", "sqlQuery": "SELECT * FROM analytics.events", "columnMappings": listedCM,
	}}})
	f := &fakeLD{t: t, list: string(list)}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	ops := f.patches["checkout-events"]
	if len(ops) < 3 || ops[0].Op != "test" || ops[0].Path != "/sqlQuery" || ops[0].Value != "SELECT * FROM analytics.events" ||
		ops[1].Op != "test" || ops[1].Path != "/columnMappings" {
		t.Fatalf("ops = %+v, want test /sqlQuery and test /columnMappings first", ops)
	}
	got, _ := json.Marshal(ops[1].Value)
	want, _ := json.Marshal(listedCM)
	if string(got) != string(want) {
		t.Errorf("test /columnMappings value =\n%s\nwant the listed value\n%s", got, want)
	}

	// LaunchDarkly answers a failed test with 400.
	f2 := &fakeLD{t: t, list: string(list), patchStatus: http.StatusBadRequest, patchBody: `{"code":"invalid_request","message":"Error applying json-patch document"}`}
	srv2 := f2.server()
	defer srv2.Close()
	e = newPhase3Engine(t, srv2.URL, false)
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	wantErr := `Data source "Checkout Events": not updated: it changed in LaunchDarkly after this run read it; rerun to pick up the change`
	if e.report.DataSources.Failed != 1 || len(e.report.Errors) != 1 || e.report.Errors[0] != wantErr {
		t.Errorf("report %+v errors=%q, want %q", e.report.DataSources, e.report.Errors, wantErr)
	}
}

func TestPhase3_ConstantKeyOffRefusesWhenKeyColumnIsGone(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: unwrappedList}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.overwrite, e.constantEventKey = true, false
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	want := `Data source "Checkout Events": not updated: its key column "EVENT_NAME" is not in the updated query; metrics on it would filter a different column. Keep that column in the Statsig SQL, or update the data source by hand`
	if f.patchCount != 0 || e.report.DataSources.Failed != 1 || len(e.report.Errors) != 1 || e.report.Errors[0] != want {
		t.Fatalf("patches=%d report %+v errors=%q, want %q", f.patchCount, e.report.DataSources, e.report.Errors, want)
	}

	f2 := &fakeLD{t: t, list: unwrappedList, previewCols: `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT"},{"name":"EVENT_NAME","type":"TEXT"}`}
	srv2 := f2.server()
	defer srv2.Close()
	e = newPhase3Engine(t, srv2.URL, false)
	e.overwrite, e.constantEventKey = true, false
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Updated != 1 {
		t.Fatalf("report %+v errors=%q, want one update", e.report.DataSources, e.report.Errors)
	}
	for _, op := range f2.patches["checkout-events"] {
		if op.Path == "/columnMappings/keyColumn" {
			t.Errorf("keyColumn changed to %v, want EVENT_NAME kept", op.Value)
		}
	}
}

// accountList is an unwrapped data source with an account context the new query does not return.
const accountList = `{"items":[{"key":"checkout-events","sqlQuery":"SELECT * FROM analytics.events","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID","account":"ACCOUNT_ID"},"columns":[]}}]}`

func runOverwrite(t *testing.T, f *fakeLD) *migrationEngine {
	t.Helper()
	t.Chdir(t.TempDir())
	srv := f.server()
	t.Cleanup(srv.Close)
	e := newPhase3Engine(t, srv.URL, false)
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPhase3_OverwriteRefusesToDropAContextKindBoundMetricsUse(t *testing.T) {
	for _, units := range []string{`"analysisUnits":["user","account"]`, `"randomizationUnits":["account"]`} {
		f := &fakeLD{t: t, list: accountList, metrics: `[{"key":"accounts-converted","eventKey":"checkout-events",` + units + `,"dataSource":{"key":"checkout-events"}}]`}
		e := runOverwrite(t, f)
		want := `Data source "Checkout Events": not updated: the updated query does not return the column of its context kind(s) "account", which bound metrics use as analysis or randomization units: accounts-converted (account). Keep the column in the Statsig SQL, or change those metrics' units in LaunchDarkly and refresh them on running experiments, then rerun`
		if f.patchCount != 0 || len(e.report.Errors) != 1 || e.report.Errors[0] != want {
			t.Errorf("%s: patches=%d errors=%q\nwant %q", units, f.patchCount, e.report.Errors, want)
		}
	}

	f := &fakeLD{t: t, list: accountList, metrics: `[{"key":"users-converted","eventKey":"checkout-events","analysisUnits":["user"],"dataSource":{"key":"checkout-events"}}]`}
	e := runOverwrite(t, f)
	want := `Data source "checkout-events" was updated, but the new query does not return all of its mapped columns: context account: ACCOUNT_ID → (none). Check its mappings in LaunchDarkly.`
	if e.report.DataSources.Updated != 1 || len(e.report.Warnings) != 1 || e.report.Warnings[0] != want {
		t.Errorf("report %+v warnings=%q\nwant %q", e.report.DataSources, e.report.Warnings, want)
	}
}

func TestPhase3_OverwriteFallbackChecksColumnTypes(t *testing.T) {
	cols := `{"name":"CREATED_AT","type":"TEXT"},{"name":"NOTE","type":"TEXT"},{"name":"USER_ID","type":"TEXT"},{"name":"EVENT_KEY","type":"TEXT"}`
	list := `{"items":[{"key":"checkout-events","sqlQuery":"SELECT * FROM analytics.events","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","valueColumn":"AMT","contexts":{"user":"USER_ID"},"columns":[]}}]}`
	source := func(e *migrationEngine) {
		e.metricSources[0]["timestampColumn"] = "created_at"
		e.metricSources[0]["customFieldMapping"] = []any{map[string]any{"fieldName": "value", "column": "note"}}
	}

	// The old timestamp column is gone too, and LaunchDarkly requires one in the column list.
	f := &fakeLD{t: t, list: list, previewCols: cols}
	e := runMappings(t, f, func(e *migrationEngine) { e.overwrite, e.updateMappings = true, false; source(e) })
	want := `Data source "Checkout Events": not updated: its timestamp column "TS" is not in the updated query, and the Statsig source's "CREATED_AT" is TEXT, not a timestamp or date. Keep "TS" in the Statsig SQL, or correct the Statsig source's timestamp column, then rerun`
	if f.patchCount != 0 || len(e.report.Errors) != 1 || e.report.Errors[0] != want {
		t.Errorf("patches=%d errors=%q\nwant %q", f.patchCount, e.report.Errors, want)
	}

	// A non-numeric value column is not used; with no bound reader the old one is removed.
	f = &fakeLD{t: t, list: list, previewCols: `{"name":"TS","type":"TIMESTAMP_NTZ"},` + cols}
	e = runMappings(t, f, func(e *migrationEngine) { e.overwrite, e.updateMappings = true, false; source(e) })
	if got := opLines(f.patches["checkout-events"]); !strings.Contains(got, "remove /columnMappings/valueColumn") || strings.Contains(got, "add /columnMappings/valueColumn") {
		t.Errorf("ops:\n%s", got)
	}
	wantWarnings := []string{
		`Data source "checkout-events" was updated, but the new query does not return all of its mapped columns: value column: AMT → (none). Check its mappings in LaunchDarkly.`,
		`1 data source(s) did not take some mappings from the Statsig export, because the export's column has the wrong type: value "NOTE" (TEXT) on checkout-events. A timestamp column needs a timestamp or date type, and a value column a numeric one; correct the column in Statsig, or cast it in the query, then rerun.`,
	}
	if strings.Join(e.report.Warnings, "\n") != strings.Join(wantWarnings, "\n") || noteCodes(e.report.Notes) != noteExportColumnWrongType {
		t.Errorf("warnings:\n%s\nwant:\n%s\nnotes=%+v", strings.Join(e.report.Warnings, "\n"), strings.Join(wantWarnings, "\n"), e.report.Notes)
	}
}

// A ratio's denominator with no data source of its own reads the numerator's, so its
// event name must hold the data source key too.
func TestPhase3_OverwriteChecksInheritingDenominators(t *testing.T) {
	f := &fakeLD{t: t, list: unwrappedList, metrics: `[
		{"key":"per-visit","eventKey":"checkout-events","dataSource":{"key":"checkout-events"},"denominator":{"eventName":"visit","dataSource":{"key":"launchdarkly-hosted"}}},
		{"key":"per-session","eventKey":"checkout-events","dataSource":{"key":"checkout-events"},"denominator":{"eventName":"session"}},
		{"key":"elsewhere","eventKey":"x","dataSource":{"key":"orders"},"denominator":{"eventName":"y","dataSource":{"key":"launchdarkly-hosted"}}}]`}
	e := runOverwrite(t, f)
	if f.patchCount != 0 || len(e.report.Errors) != 1 || !strings.Contains(e.report.Errors[0], `2 metric(s) bound to it use an event key other than "checkout-events" and would match no rows after the update: per-session (denominator event name "session"), per-visit (denominator event name "visit")`) {
		t.Errorf("patches=%d errors=%q", f.patchCount, e.report.Errors)
	}
}

func TestBoundMetrics_NumeratorOrDenominator(t *testing.T) {
	f := &fakeLD{t: t, metrics: `[
		{"key":"num","dataSource":{"key":"ds"},"analysisUnits":["user"]},
		{"key":"den","dataSource":{"key":"other"},"denominator":{"dataSource":{"key":"ds"}},"randomizationUnits":["account"]},
		{"key":"hosted-den","dataSource":{"key":"ds"},"denominator":{"dataSource":{"key":"launchdarkly-hosted"}}},
		{"key":"no-den-source","dataSource":{"key":"ds"},"denominator":{}},
		{"key":"num-only","dataSource":{"key":"ds"},"denominator":{"dataSource":{"key":"other"}}},
		{"key":"hosted-elsewhere","dataSource":{"key":"other"},"denominator":{"dataSource":{"key":"launchdarkly-hosted"}}}]`}
	srv := f.server()
	defer srv.Close()
	e := newPhase3Engine(t, srv.URL, false)
	bound, err := e.boundMetrics("ds")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range bound {
		got = append(got, fmt.Sprintf("%s num=%v den=%v units=%v", b.key, b.num != nil, b.den != nil, b.units))
	}
	want := []string{
		"den num=false den=true units=[account]",
		"hosted-den num=true den=true units=[]",
		"no-den-source num=true den=true units=[]",
		"num num=true den=false units=[user]",
		"num-only num=true den=false units=[]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("bound:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
