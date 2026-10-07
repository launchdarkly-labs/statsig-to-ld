package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/state"
)

// fakeLD answers the Phase 3 endpoints. Its preview behaves like Snowflake for
// the wrapper: an unquoted alias comes back upper-cased.
type fakeLD struct {
	t *testing.T
	// list is the body of the data source list; listStatus overrides 200.
	list       string
	listStatus int
	// metrics is the items array of the project's metric list; metricsStatus
	// overrides 200.
	metrics       string
	metricsStatus int
	// previewCols are the source's columns as the preview returns them.
	previewCols string

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
			_, _ = io.WriteString(w, `{"rows":[],"timestampColumn":"TS","keyColumn":"EVENT_KEY","columns":[`+cols+`]}`)
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
			_, _ = io.WriteString(w, `{"items":`+items+`,"_links":{}}`)
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

// An existing data source created before the constant event key: plain SQL,
// and its key column is a real column.
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

// A failed list must not read as "no data sources exist": that would re-post
// every source and skip the existing-source checks.
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
		t.Fatalf("warnings = %q, want one", e.report.Warnings)
	}
	w := e.report.Warnings[0]
	for _, want := range []string{"--overwrite would replace its SQL", "refuses when metrics already bound", "built by hand"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not contain %q", w, want)
		}
	}
	if strings.Contains(strings.ToLower(w), "rerun with --overwrite") {
		t.Errorf("warning %q still tells the user to rerun with --overwrite unconditionally", w)
	}
}

// On --resume, a data source recorded in migration_state.json by an earlier
// run is still checked against LaunchDarkly, so one created before the
// constant event key is flagged rather than skipped silently.
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

	// With --overwrite, the resumed run updates it.
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

// A data source that already projects the constant is never rewritten, with
// or without the state file: another PATCH could only drop mappings edited in
// LaunchDarkly since, such as an added context kind or value column.
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

// --constant-event-key=false must never unwrap a data source: its metrics use
// the data source key as their event key and would match no rows.
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
	if len(e.report.Warnings) != 1 || !strings.Contains(e.report.Warnings[0], `"checkout-events"`) || !strings.Contains(e.report.Warnings[0], "match no rows") {
		t.Errorf("warnings = %q, want one naming the source and why it was not updated", e.report.Warnings)
	}
}

// --overwrite updates only the query, the key column, and the column list. The
// timestamp, value, and context mappings are kept while the new query returns
// them, in the new query's case.
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
	if ops[0].Value != wantCheckoutSQL || ops[1].Value != "LD_EVENT_KEY" || ops[3].Value != "ACCOUNT_ID" {
		t.Errorf("values = %v / %v / %v", ops[0].Value, ops[1].Value, ops[3].Value)
	}
	if cols := ops[2].Value.([]any); len(cols) != 6 {
		t.Errorf("columns = %v, want the wrapped preview's 6 columns", cols)
	}
	if len(e.report.Warnings) != 0 {
		t.Errorf("warnings = %q, want none", e.report.Warnings)
	}
}

// Mappings whose column the new query no longer returns fall back to this
// run's value, or are removed, and each change is reported.
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
	ops, notes := dataSourcePatch(existing, body, true)
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
	ops, _ = dataSourcePatch(existing, body, true)
	last := ops[len(ops)-1]
	if last.Op != "replace" || last.Path != "/columnMappings/contexts" {
		t.Errorf("last op = %+v, want replace /columnMappings/contexts", last)
	}
}

// With the constant key off, an existing key column the new query still
// returns is kept.
func TestDataSourcePatch_ConstantKeyOffKeepsKeyColumn(t *testing.T) {
	existing := map[string]any{"sqlQuery": "SELECT * FROM t", "columnMappings": map[string]any{
		"keyColumn": "event_name", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"},
	}}
	body := map[string]any{"sqlQuery": "SELECT * FROM u", "columnMappings": map[string]any{
		"keyColumn": "EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]string{"user": "USER_ID"},
		"columns": []map[string]any{{"name": "TS"}, {"name": "USER_ID"}, {"name": "EVENT_NAME"}, {"name": "EVENT_KEY"}},
	}}
	ops, _ := dataSourcePatch(existing, body, false)
	for _, op := range ops {
		if op.Path == "/columnMappings/keyColumn" && op.Value != "EVENT_NAME" {
			t.Errorf("keyColumn op = %+v, want the existing column in the new case", op)
		}
	}
}

const boundCheckoutMetrics = `[
	{"key":"order-total-sum","eventKey":"order_total","dataSource":{"key":"checkout-events"}},
	{"key":"checkouts-count","eventKey":"checkout-events","dataSource":{"key":"checkout-events"}},
	{"key":"avg-order-value","eventKey":"order_total","dataSource":{"key":"orders"},
	 "denominator":{"eventName":"checkout","dataSource":{"key":"checkout-events"}}},
	{"key":"page-views","eventKey":"page_view","dataSource":{"key":"page-views"}}]`

// The bound-metric check runs before anything is written. Metrics bound
// through a ratio's denominator count too.
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

// Fail closed: a data source is not updated when its bound metrics cannot be
// checked. The project's metrics are read once per run.
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
	if !strings.Contains(e.report.Errors[0], "could not read the project's metrics") || !strings.Contains(e.report.Errors[0], "403") {
		t.Errorf("error = %q", e.report.Errors[0])
	}
	if f.metricsReads != 1 {
		t.Errorf("metrics read %d times, want once per run", f.metricsReads)
	}
}

// Two Statsig sources that sanitize to one key would be written onto the same
// data source, the last one winning. Neither is created, updated, or mapped.
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

// A dry run with credentials reports what a real run would do with each
// source; without them it says existing sources were not checked.
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
	ops, _ := dataSourcePatch(existing, body, true)
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

// TestWarehouseCmd_FlagsBound verifies every user-facing flag is registered
// on warehouseCmd.
func TestWarehouseCmd_FlagsBound(t *testing.T) {
	for _, name := range []string{
		"statsig-key", "statsig-url", "statsig-export-file",
		"ld-key", "ld-url", "ld-project", "ld-environment", "ld-maintainer",
		"warehouse-type", "dry-run", "resume", "only",
		"overwrite", "force-overwrite", "constant-event-key", "verbose", "no-color",
	} {
		if warehouseCmd.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s not registered on `warehouse`", name)
		}
	}
}
