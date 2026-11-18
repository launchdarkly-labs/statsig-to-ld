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
	planTierCol       = "sql_filter_6e400426"
	filterPreviewCols = `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT"},{"name":"SQL_FILTER_6E400426","type":"BOOLEAN"}`
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
			  "criteria":[{"type":"metadata","condition":"sql_filter","values":["plan_tier > 2"]}]}}],"pagination":{"nextPage":""}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	e := &migrationEngine{sg: statsig.NewClient("console-x", srv.URL), ctx: t.Context(), state: state.NewMigrationState(false)}
	if err := e.phase1Export(); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"Checkout Events": {"plan_tier > 2"}}
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

	metricsStatus = http.StatusForbidden
	if err := e.phase1Export(); err == nil || !strings.Contains(err.Error(), "SQL filters") {
		t.Errorf("a failed metrics read: err = %v", err)
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
			if cols := fmt.Sprint(f.created[0]["columnMappings"].(map[string]any)["columns"]); !strings.Contains(cols, "SQL_FILTER_6E400426") {
				t.Errorf("columns = %s, want the SQL filter's column", cols)
			}
		})
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
		t.Errorf("data-source-bodies.json = %s (%v), want the query with its SQL filter columns", raw, err)
	}
}
