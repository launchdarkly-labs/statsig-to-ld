package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/state"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

const (
	checkoutRawInner  = "SELECT ts, user_id FROM analytics.events WHERE kind = 'checkout'"
	planTierCol       = "sql_filter_6e4004268a3c8fc1"
	filterPreviewCols = `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT"},{"name":"SQL_FILTER_6E4004268A3C8FC1","type":"BOOLEAN"}`
)

func withFilterLayer(inner string, cols ...string) string {
	return "SELECT *, " + strings.Join(cols, ", ") + " FROM (\n" + inner + "\n) AS statsig_filters"
}

func wrappedCheckout(inner string) string {
	return "SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\n" + inner + "\n) AS ld_src"
}

var planTierExpr = "(plan_tier > 2) AS " + planTierCol

func sqlFilterEngine(t *testing.T, srvURL string, filters ...string) *migrationEngine {
	e := newPhase3Engine(t, srvURL, false)
	if len(filters) == 0 {
		filters = []string{"plan_tier > 2"}
	}
	e.sqlFilters = map[string][]string{"Checkout Events": filters}
	return e
}

func TestPhase1_ExportRecordsTheMetricsSQLFilters(t *testing.T) {
	t.Chdir(t.TempDir())
	metricsStatus := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics/metric_source/list":
			_, _ = io.WriteString(w, `{"data":[{"name":"Checkout Events","sql":"SELECT 1"}]}`)
		case "/metrics/metric_source/Checkout Events":
			_, _ = io.WriteString(w, `{"data":{"name":"Checkout Events","sql":"SELECT 1"}}`)
		case "/metrics/list":
			w.WriteHeader(metricsStatus)
			_, _ = io.WriteString(w, `{"data":[{"name":"Pro Revenue","type":"user_warehouse","warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout Events",
			  "criteria":[{"type":"metadata","condition":"sql_filter","values":["COALESCE(plan_tier, 0) > 2"]}]}},
			  {"name":"Real Revenue","type":"user_warehouse","warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout Events",
			  "criteria":[{"type":"metadata","condition":"sql_filter","values":["NOT is_test"]}]}}],"pagination":{"nextPage":""}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	e := &migrationEngine{sg: statsig.NewClient("console-x", srv.URL), ctx: t.Context(), state: state.NewMigrationState(false)}
	if err := e.phase1Export(); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"Checkout Events": {"COALESCE(plan_tier, 0) > 2"}}
	if !reflect.DeepEqual(e.sqlFilters, want) {
		t.Errorf("sqlFilters = %#v, want %#v", e.sqlFilters, want)
	}
	files, _ := filepath.Glob("statsig_export_*.json")
	if len(files) != 1 {
		t.Fatalf("export files = %q", files)
	}
	loaded := &migrationEngine{exportFile: files[0], state: state.NewMigrationState(false)}
	loaded.phase1LoadFromFile()
	if !reflect.DeepEqual(loaded.sqlFilters, want) || len(loaded.report.Warnings) != 0 {
		t.Errorf("loaded sqlFilters = %#v warnings=%q, want %#v", loaded.sqlFilters, loaded.report.Warnings, want)
	}

	// Only the SQL filters need the metrics, so the run goes on without them.
	metricsStatus = http.StatusForbidden
	for _, only := range []string{"", "warehouse"} {
		e.only, e.report.Warnings = only, nil
		if err := e.phase1Export(); err != nil {
			t.Fatal(err)
		}
		if e.sqlFilters != nil {
			t.Errorf("sqlFilters = %#v after a failed read", e.sqlFilters)
		}
		if warned := len(e.report.Warnings) == 1 && strings.Contains(e.report.Warnings[0], "--update-custom-fields"); warned != (only == "") {
			t.Errorf("--only %q: warnings = %q", only, e.report.Warnings)
		}
	}
	// An export file from that run lists no SQL filters, so a run from it warns too.
	files, _ = filepath.Glob("statsig_export_*.json")
	raw, err := os.ReadFile(files[len(files)-1])
	if err != nil || strings.Contains(string(raw), "metric_sql_filters") {
		t.Errorf("export = %s (%v), want no metric_sql_filters", raw, err)
	}
}

func TestPhase1_OlderExportFileIsReported(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("export.json", []byte(`{"metric_sources":[{"name":"Checkout Events"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &migrationEngine{exportFile: "export.json", state: state.NewMigrationState(false)}
	e.phase1LoadFromFile()
	if len(e.report.Warnings) != 1 || !strings.Contains(e.report.Warnings[0], "SQL filter") {
		t.Errorf("warnings = %q", e.report.Warnings)
	}
}

func TestPhase3_SQLFiltersAreAddedToTheQuery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields bool
		want   string
	}{
		{"filters only", false, wrappedCheckout(withFilterLayer(checkoutRawInner, planTierExpr))},
		{"with custom fields", true, wrappedCheckout(withFilterLayer(checkoutCustomInner, planTierExpr))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, previewCols: filterPreviewCols}
			srv := f.server()
			defer srv.Close()

			e := sqlFilterEngine(t, srv.URL)
			if tc.fields {
				e.metricSources[0]["customFieldMapping"] = centsField
			}
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if len(f.created) != 1 || len(e.report.Warnings) != 0 {
				t.Fatalf("created %d, warnings=%q errors=%q", len(f.created), e.report.Warnings, e.report.Errors)
			}
			if got := f.created[0]["sqlQuery"]; got != tc.want {
				t.Errorf("sqlQuery =\n%v\nwant\n%s", got, tc.want)
			}
			if cols := fmt.Sprint(f.created[0]["columnMappings"].(map[string]any)["columns"]); !strings.Contains(cols, "SQL_FILTER_6E4004268A3C8FC1") {
				t.Errorf("columns = %s, want the SQL filter's column", cols)
			}
		})
	}
}

// metrics convert checks a filter's column against the column list, which only the
// preview fills in.
func TestPhase3_SQLFilterColumnsAreListedWithoutAPreview(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, noIntegration: true}
	srv := f.server()
	defer srv.Close()

	e := sqlFilterEngine(t, srv.URL)
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if len(f.previews) != 0 || len(f.created) != 1 {
		t.Fatalf("previews=%d created=%d errors=%q", len(f.previews), len(f.created), e.report.Errors)
	}
	cols := fmt.Sprint(f.created[0]["columnMappings"].(map[string]any)["columns"])
	if !strings.Contains(cols, "map[name:"+planTierCol+" type:BOOLEAN]") {
		t.Errorf("columns = %s, want the SQL filter's column", cols)
	}
}

func TestPhase3_SQLFiltersThatCannotBeAddedAreLeftOut(t *testing.T) {
	for _, tc := range []struct {
		name     string
		f        *fakeLD
		filters  []string
		note     string
		warnings []string
	}{
		{
			"one fails the preview",
			&fakeLD{filterPreviewStatus: 400},
			nil,
			noteSQLFiltersPreviewFailed,
			[]string{"were written without columns for their metrics' Statsig SQL filters", "SQL compilation error on checkout-events"},
		},
		{
			"the Statsig SQL fails the preview too",
			&fakeLD{failPreview: func(sql string) bool { return !strings.HasPrefix(sql, "SELECT *, '") }},
			nil,
			noteSQLFiltersPreviewFailed,
			[]string{"were written without columns for their metrics' Statsig SQL filters", "SQL compilation error on checkout-events"},
		},
		{
			"one uses a Statsig macro",
			&fakeLD{},
			[]string{"ts > {statsig_start_date}"},
			noteSQLFilterUnusable,
			[]string{"left out of their data source's query", `"ts > {statsig_start_date}" on checkout-events`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			tc.f.t = t
			srv := tc.f.server()
			defer srv.Close()

			e := sqlFilterEngine(t, srv.URL, tc.filters...)
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if len(tc.f.created) != 1 || tc.f.created[0]["sqlQuery"] != wantCheckoutSQL {
				t.Fatalf("created = %v, want the data source without SQL filter columns; errors=%q", tc.f.created, e.report.Errors)
			}
			if want := []reportNote{{Code: tc.note, DataSource: "checkout-events"}}; !reflect.DeepEqual(e.report.Notes, want) {
				t.Errorf("notes = %+v, want %+v", e.report.Notes, want)
			}
			warned := strings.Join(e.report.Warnings, " ")
			for _, w := range tc.warnings {
				if len(e.report.Warnings) != 1 || !strings.Contains(warned, w) {
					t.Errorf("warnings = %q, want one containing %q", e.report.Warnings, w)
				}
			}
		})
	}
}

func TestPhase3_UpdateCustomFieldsAddsSQLFilterColumns(t *testing.T) {
	regionExpr := "(region = 'EU') AS " + statsig.SQLFilterColumn("region = 'EU'")
	for _, tc := range []struct {
		name, existing, want string
	}{
		{"created before custom fields", wantCheckoutSQL, wrappedCheckout(withFilterLayer(checkoutCustomInner, planTierExpr))},
		{"created with its custom fields", wantCheckoutCustomSQL, wrappedCheckout(withFilterLayer(checkoutCustomInner, planTierExpr))},
		{
			"created by an earlier version, whose custom fields are bare",
			wrappedCheckout(strings.Replace(checkoutCustomInner, "(price_usd/100)", "price_usd/100", 1)),
			wrappedCheckout(withFilterLayer(checkoutCustomInner, planTierExpr)),
		},
		{
			"keeps a column Statsig no longer has a filter for",
			wrappedCheckout(withFilterLayer(checkoutCustomInner, regionExpr)),
			wrappedCheckout(withFilterLayer(checkoutCustomInner, regionExpr, planTierExpr)),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, tc.existing), previewCols: filterPreviewCols}
			srv := f.server()
			defer srv.Close()

			e := sqlFilterEngine(t, srv.URL)
			e.metricSources[0]["customFieldMapping"] = centsField
			e.updateCustomFields = true
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if e.report.DataSources.Updated != 1 || len(e.report.Warnings) != 0 || len(e.report.Errors) != 0 {
				t.Fatalf("report=%+v warnings=%q errors=%q", e.report.DataSources, e.report.Warnings, e.report.Errors)
			}
			ops := f.patches["checkout-events"]
			if len(ops) < 3 || ops[2].Path != "/sqlQuery" || ops[2].Value != tc.want {
				t.Errorf("ops = %+v\nwant sqlQuery\n%s", ops, tc.want)
			}
		})
	}
}

func TestPhase3_SQLFilterColumnsAlreadyThereAreLeftAlone(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, wrappedCheckout(withFilterLayer(checkoutRawInner, planTierExpr))), previewCols: filterPreviewCols}
	srv := f.server()
	defer srv.Close()

	e := sqlFilterEngine(t, srv.URL)
	e.updateCustomFields = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if f.patchCount != 0 || len(e.report.Notes) != 0 || len(e.report.Warnings) != 0 {
		t.Errorf("patches=%d notes=%+v warnings=%q", f.patchCount, e.report.Notes, e.report.Warnings)
	}
}

// A filter that fails the preview does not hold back the custom fields.
func TestPhase3_UpdateCustomFieldsAddsTheFieldsWhenAFilterFailsThePreview(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, wantCheckoutSQL), previewCols: customFieldPreviewCols,
		failPreview: func(sql string) bool { return strings.Contains(sql, "plan_tier") }}
	srv := f.server()
	defer srv.Close()

	e := sqlFilterEngine(t, srv.URL)
	e.metricSources[0]["customFieldMapping"] = centsField
	e.updateCustomFields = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Updated != 1 || len(e.report.Errors) != 0 {
		t.Fatalf("report=%+v errors=%q", e.report.DataSources, e.report.Errors)
	}
	if ops := f.patches["checkout-events"]; len(ops) < 3 || ops[2].Path != "/sqlQuery" || ops[2].Value != wantCheckoutCustomSQL {
		t.Errorf("ops = %+v\nwant sqlQuery\n%s", ops, wantCheckoutCustomSQL)
	}
	if want := []reportNote{{Code: noteSQLFiltersPreviewFailed, DataSource: "checkout-events"}}; !reflect.DeepEqual(e.report.Notes, want) {
		t.Errorf("notes = %+v, want %+v", e.report.Notes, want)
	}
}

// With --constant-event-key=false the SQL filter layer is outermost. --overwrite keeps
// its columns, which bound metrics may filter on.
func TestPhase3_OverwriteKeepsSQLFilterColumns(t *testing.T) {
	regionExpr := "(region = 'EU') AS " + statsig.SQLFilterColumn("region = 'EU'")
	list, err := json.Marshal(map[string]any{"items": []any{map[string]any{
		"key": "checkout-events", "sqlQuery": withFilterLayer(checkoutRawInner, regionExpr),
		"columnMappings": map[string]any{"keyColumn": "EVENT_NAME", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"}, "columns": []any{}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		failPreview func(string) bool
		want        string
		notes       []reportNote
	}{
		{"adds the new filter", nil, wrappedCheckout(withFilterLayer(checkoutRawInner, regionExpr, planTierExpr)), nil},
		{
			"the new filter fails the preview",
			func(sql string) bool { return strings.Contains(sql, "plan_tier") },
			wrappedCheckout(withFilterLayer(checkoutRawInner, regionExpr)),
			[]reportNote{{Code: noteSQLFiltersPreviewFailed, DataSource: "checkout-events"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: string(list), previewCols: filterPreviewCols, failPreview: tc.failPreview}
			srv := f.server()
			defer srv.Close()

			e := sqlFilterEngine(t, srv.URL)
			e.overwrite = true
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if e.report.DataSources.Updated != 1 {
				t.Fatalf("report=%+v errors=%q", e.report.DataSources, e.report.Errors)
			}
			var got any
			for _, op := range f.patches["checkout-events"] {
				if op.Op != "test" && op.Path == "/sqlQuery" {
					got = op.Value
				}
			}
			if got != tc.want {
				t.Errorf("sqlQuery =\n%v\nwant\n%s", got, tc.want)
			}
			if len(e.report.Notes) != len(tc.notes) || len(tc.notes) > 0 && !reflect.DeepEqual(e.report.Notes, tc.notes) {
				t.Errorf("notes = %+v, want %+v", e.report.Notes, tc.notes)
			}
		})
	}
}

// A data source the run does not write says nothing about the filters it leaves out.
func TestPhase3_UnusableSQLFiltersOfSourcesNotWrittenAreNotReported(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry run %v", dryRun), func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, wrappedCheckout(withFilterLayer(checkoutRawInner, planTierExpr))), previewCols: filterPreviewCols}
			srv := f.server()
			defer srv.Close()

			e := sqlFilterEngine(t, srv.URL, "plan_tier > 2", "ts > {statsig_start_date}")
			e.updateCustomFields = true
			var warnings []string
			if dryRun {
				e.dryRun, e.ldReadable = true, true
				warnings = e.dryRunDataSources("snowflake").warnings
			} else {
				if err := e.phase3aMigrateDataSources(); err != nil {
					t.Fatal(err)
				}
				warnings = e.report.Warnings
			}
			if f.patchCount != 0 || len(e.report.Notes) != 0 || len(warnings) != 0 {
				t.Errorf("patches=%d notes=%+v warnings=%q", f.patchCount, e.report.Notes, warnings)
			}
		})
	}
}

func TestPhase3_SourceWithAnEditedEventKeyIsKeptWithTheReason(t *testing.T) {
	t.Chdir(t.TempDir())
	edited := strings.Replace(wrappedCheckout(checkoutRawInner), "'checkout-events'", "'checkout'", 1)
	f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, edited), previewCols: filterPreviewCols}
	srv := f.server()
	defer srv.Close()

	e := sqlFilterEngine(t, srv.URL)
	e.updateCustomFields = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	warned := strings.Join(e.report.Warnings, " ")
	if f.patchCount != 0 || len(e.report.Warnings) != 1 || !strings.Contains(warned, "event key was edited in LaunchDarkly") || strings.Contains(warned, "Statsig SQL has changed") {
		t.Errorf("patches=%d warnings=%q", f.patchCount, e.report.Warnings)
	}
}

// Without its columns the update would rewrite the query unchanged.
func TestPhase3_UpdateWhoseSQLFiltersFailThePreviewFails(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, wantCheckoutSQL), filterPreviewStatus: 400}
	srv := f.server()
	defer srv.Close()

	e := sqlFilterEngine(t, srv.URL)
	e.updateCustomFields = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if f.patchCount != 0 || e.report.DataSources.Failed != 1 || len(e.report.Errors) != 1 || !strings.Contains(e.report.Errors[0], "SQL filters failed") {
		t.Errorf("patches=%d report=%+v errors=%q", f.patchCount, e.report.DataSources, e.report.Errors)
	}
}

func TestDryRun_ReportsMissingSQLFilterColumns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update bool
		want   string
	}{
		{"without the flag", false, "Checkout Events (query): would skip (lacks columns for its metrics' Statsig SQL filters; --update-custom-fields can add them)"},
		{"with --update-custom-fields", true, `Checkout Events (query): would update (adds SQL filter columns "` + planTierCol + `")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, wantCheckoutSQL), previewCols: filterPreviewCols}
			srv := f.server()
			defer srv.Close()

			e := sqlFilterEngine(t, srv.URL)
			e.dryRun, e.ldReadable, e.updateCustomFields = true, true, tc.update
			l := e.dryRunDataSources("snowflake")
			if len(l.lines) != 1 || l.lines[0] != tc.want {
				t.Errorf("lines = %q, want %q", l.lines, tc.want)
			}
			if warned := strings.Contains(strings.Join(l.warnings, " "), "--update-custom-fields"); warned == tc.update {
				t.Errorf("warnings = %q", l.warnings)
			}
		})
	}
}

func TestWriteDryRunBodies_IncludesSQLFilters(t *testing.T) {
	t.Chdir(t.TempDir())
	e := sqlFilterEngine(t, "http://unused")
	e.writeDryRunBodies("snowflake")
	raw, err := os.ReadFile("data-source-bodies.json")
	if err != nil {
		t.Fatal(err)
	}
	var bodies []map[string]any
	if err := json.Unmarshal(raw, &bodies); err != nil || len(bodies) != 1 || bodies[0]["sqlQuery"] != wrappedCheckout(withFilterLayer(checkoutRawInner, planTierExpr)) {
		t.Fatalf("data-source-bodies.json = %s (%v), want the query with its SQL filter columns", raw, err)
	}
	if cols := fmt.Sprint(bodies[0]["columnMappings"].(map[string]any)["columns"]); !strings.Contains(cols, "map[name:"+planTierCol+" type:BOOLEAN]") {
		t.Errorf("columns = %s, want the SQL filter's column", cols)
	}
}
