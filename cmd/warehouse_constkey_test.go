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
	// boundMetrics is the "metrics" collection returned for ?expand=metrics.
	boundMetrics string

	created  []map[string]any
	patches  map[string][]launchdarkly.JSONPatchOp
	previews []string
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
			cols := `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT","length":64},{"name":"EVENT_KEY","type":"TEXT"}`
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
			_, _ = io.WriteString(w, `{}`)
		case strings.HasPrefix(r.URL.Path, dsPath+"/") && r.Method == http.MethodGet:
			if r.URL.Query().Get("expand") != "metrics" {
				f.t.Errorf("data source GET without expand=metrics: %s", r.URL.String())
			}
			metrics := f.boundMetrics
			if metrics == "" {
				metrics = `{"items":[],"totalCount":0}`
			}
			_, _ = io.WriteString(w, `{"key":"`+strings.TrimPrefix(r.URL.Path, dsPath+"/")+`","metrics":`+metrics+`}`)
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

func TestPhase3_ExistingUnwrappedSource_WarnsToRerunWithOverwrite(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: unwrappedList}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Skipped != 1 || len(f.created) != 0 || len(f.patches) != 0 {
		t.Fatalf("report %+v created=%d patches=%d, want one skip and no writes", e.report.DataSources, len(f.created), len(f.patches))
	}
	if len(e.report.Warnings) != 1 || !strings.Contains(strings.ToLower(e.report.Warnings[0]), "rerun with --overwrite to update it in place") {
		t.Errorf("warnings = %q, want the --overwrite guidance", e.report.Warnings)
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
	if e.report.DataSources.Updated != 1 || len(f2.patches) != 1 {
		t.Errorf("resumed --overwrite run: report %+v patches=%d, want one update", e.report.DataSources, len(f2.patches))
	}
}

// An already-wrapped source the state file records as done needs nothing.
func TestPhase3_ResumeSkipsWrappedSourceEvenWithOverwrite(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("migration_state.json", []byte(`{"data_sources_created":["checkout-events"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	wrapped, _ := json.Marshal(map[string]any{"items": []any{map[string]any{
		"key": "checkout-events", "sqlQuery": wantCheckoutSQL,
		"columnMappings": map[string]any{"keyColumn": "LD_EVENT_KEY"},
	}}})
	f := &fakeLD{t: t, list: string(wrapped)}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, true)
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Skipped != 1 || len(f.patches) != 0 || len(e.report.Warnings) != 0 {
		t.Errorf("report %+v patches=%d warnings=%q, want a quiet skip", e.report.DataSources, len(f.patches), e.report.Warnings)
	}
}

// --overwrite updates an existing source in place with the same wrapped SQL
// and preview-derived column mappings a create would send, and names the
// metrics already bound to it that the new key column no longer matches.
func TestPhase3_OverwritePatchesExistingSource(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: unwrappedList, boundMetrics: `{"totalCount":3,"items":[
		{"key":"order-total-sum","eventKey":"order_total","dataSource":{"key":"checkout-events"}},
		{"key":"checkouts-count","eventKey":"checkout-events","dataSource":{"key":"checkout-events"}}]}`}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.overwrite = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Updated != 1 || len(f.created) != 0 {
		t.Fatalf("report %+v created=%d, want one update and no create; errors=%v", e.report.DataSources, len(f.created), e.report.Errors)
	}
	ops := f.patches["checkout-events"]
	if len(ops) != 2 {
		t.Fatalf("ops = %+v, want sqlQuery and columnMappings", ops)
	}
	if ops[0].Op != "add" || ops[0].Path != "/sqlQuery" || ops[0].Value != wantCheckoutSQL {
		t.Errorf("op 0 = %+v, want add /sqlQuery with the wrapped SQL", ops[0])
	}
	if ops[1].Op != "replace" || ops[1].Path != "/columnMappings" {
		t.Fatalf("op 1 = %+v, want replace /columnMappings", ops[1])
	}
	cm := ops[1].Value.(map[string]any)
	if cm["keyColumn"] != "LD_EVENT_KEY" || len(cm["columns"].([]any)) != 4 {
		t.Errorf("columnMappings = %v, want keyColumn LD_EVENT_KEY and the wrapped preview's 4 columns", cm)
	}

	if len(e.report.Warnings) != 1 {
		t.Fatalf("warnings = %q, want one naming the stale metric", e.report.Warnings)
	}
	w := e.report.Warnings[0]
	if !strings.Contains(w, "order-total-sum") || strings.Contains(w, "checkouts-count") || !strings.Contains(w, "1 more bound metric") {
		t.Errorf("warning = %q, want order-total-sum named, checkouts-count not, and the unchecked one counted", w)
	}
}

func TestDataSourcePatch_SwitchesTableSourceToSQL(t *testing.T) {
	existing := map[string]any{"key": "k", "tableName": "DB.ORDERS", "columnMappings": map[string]any{}}
	body := map[string]any{"key": "k", "sqlQuery": "SELECT 1", "columnMappings": map[string]any{"keyColumn": "LD_EVENT_KEY"}}
	ops := dataSourcePatch(existing, body)
	want := []launchdarkly.JSONPatchOp{
		{Op: "remove", Path: "/tableName"},
		{Op: "add", Path: "/sqlQuery", Value: "SELECT 1"},
		{Op: "replace", Path: "/columnMappings", Value: body["columnMappings"]},
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
