package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

var centsField = []any{map[string]any{"key": "price_usd_cents", "formula": "price_usd/100"}}

const customFieldPreviewCols = `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT"},{"name":"PRICE_USD_CENTS","type":"NUMBER"}`

const checkoutCustomInner = "SELECT *, price_usd/100 AS price_usd_cents FROM (\nSELECT ts, user_id FROM analytics.events WHERE kind = 'checkout'\n) AS statsig_src"

const wantCheckoutCustomSQL = "SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\n" + checkoutCustomInner + "\n) AS ld_src"

func customFieldEngine(t *testing.T, srvURL string) *migrationEngine {
	e := newPhase3Engine(t, srvURL, false)
	e.metricSources[0]["customFieldMapping"] = centsField
	return e
}

func TestPhase3_CustomFieldsAreAddedToTheQuery(t *testing.T) {
	for _, tc := range []struct {
		name        string
		constantKey bool
		want        string
	}{
		{"with the constant event key", true, wantCheckoutCustomSQL},
		{"without the constant event key", false, checkoutCustomInner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, previewCols: customFieldPreviewCols}
			srv := f.server()
			defer srv.Close()

			e := customFieldEngine(t, srv.URL)
			e.constantEventKey = tc.constantKey
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if len(f.created) != 1 {
				t.Fatalf("created %d, want 1; errors=%v", len(f.created), e.report.Errors)
			}
			if got := f.created[0]["sqlQuery"]; got != tc.want {
				t.Errorf("sqlQuery =\n%v\nwant\n%s", got, tc.want)
			}
			if len(f.previews) == 0 || f.previews[0] != checkoutCustomInner {
				t.Errorf("previews = %q, want the query with its custom fields first", f.previews)
			}
			if cols := fmt.Sprint(f.created[0]["columnMappings"].(map[string]any)["columns"]); !strings.Contains(cols, "PRICE_USD_CENTS") {
				t.Errorf("columns = %s, want the custom field's column", cols)
			}
		})
	}
}

func TestPhase3_UnusableCustomFieldFailsTheSource(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t}
	srv := f.server()
	defer srv.Close()

	e := newPhase3Engine(t, srv.URL, false)
	e.metricSources[0]["customFieldMapping"] = []any{map[string]any{"key": "recent", "formula": "ts > {statsig_start_date}"}}
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 || len(f.previews) != 0 || e.report.DataSources.Failed != 1 ||
		len(e.report.Errors) != 1 || !strings.Contains(e.report.Errors[0], `Statsig custom field "recent"`) {
		t.Errorf("created=%d previews=%d failed=%d errors=%q", len(f.created), len(f.previews), e.report.DataSources.Failed, e.report.Errors)
	}
}

func wrappedCheckoutWithSQL(t *testing.T, sql string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"items": []any{map[string]any{
		"key": "checkout-events", "sqlQuery": sql, "columnMappings": map[string]any{
			"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"},
			"columns": []any{map[string]any{"name": "TS", "type": "TIMESTAMP_NTZ"}, map[string]any{"name": "USER_ID", "type": "TEXT"}, map[string]any{"name": "LD_EVENT_KEY", "type": "TEXT", "length": 15}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPhase3_SourceCreatedWithoutCustomFieldsIsKeptAndReported(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, wantCheckoutSQL), previewCols: customFieldPreviewCols}
	srv := f.server()
	defer srv.Close()

	e := customFieldEngine(t, srv.URL)
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if f.patchCount != 0 || e.report.DataSources.Skipped != 1 {
		t.Errorf("patches=%d report=%+v, want it skipped", f.patchCount, e.report.DataSources)
	}
	if want := []reportNote{{Code: noteCustomFieldsMissing, DataSource: "checkout-events"}}; !reflect.DeepEqual(e.report.Notes, want) {
		t.Errorf("notes = %+v, want %+v", e.report.Notes, want)
	}
	if len(e.report.Warnings) != 1 || !strings.Contains(e.report.Warnings[0], "checkout-events") || !strings.Contains(e.report.Warnings[0], "--update-custom-fields") {
		t.Errorf("warnings = %q, want one naming the data source and --update-custom-fields", e.report.Warnings)
	}
}

func TestPhase3_UpdateCustomFieldsAddsThemInPlace(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, wantCheckoutSQL), previewCols: customFieldPreviewCols}
	srv := f.server()
	defer srv.Close()

	e := customFieldEngine(t, srv.URL)
	e.updateCustomFields = true
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if e.report.DataSources.Updated != 1 || len(e.report.Warnings) != 0 || len(e.report.Errors) != 0 {
		t.Fatalf("report=%+v warnings=%q errors=%q", e.report.DataSources, e.report.Warnings, e.report.Errors)
	}
	ops := f.patches["checkout-events"]
	var paths []string
	for _, op := range ops {
		paths = append(paths, op.Op+" "+op.Path)
	}
	wantPaths := []string{"test /sqlQuery", "test /columnMappings", "add /sqlQuery", "replace /columnMappings/columns"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("ops = %q, want %q", paths, wantPaths)
	}
	if ops[2].Value != wantCheckoutCustomSQL {
		t.Errorf("sqlQuery =\n%v\nwant\n%s", ops[2].Value, wantCheckoutCustomSQL)
	}
	if cols := fmt.Sprint(ops[3].Value); !strings.Contains(cols, "PRICE_USD_CENTS") || !strings.Contains(cols, "LD_EVENT_KEY") {
		t.Errorf("columns = %s, want the custom field's and the constant key's", cols)
	}
}

func TestPhase3_UpdateCustomFieldsLeavesOtherQueriesAlone(t *testing.T) {
	handEdited := "SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nSELECT ts, user_id, price_usd/100 AS cents FROM analytics.events WHERE kind = 'checkout'\n) AS ld_src"
	editedKey := strings.Replace(wantCheckoutSQL, "'checkout-events'", "'purchase'", 1)
	for _, tc := range []struct {
		name, sql, warning string
	}{
		{"already has them", wantCheckoutCustomSQL, ""},
		{"edited in LaunchDarkly", handEdited, "Statsig SQL has changed"},
		{"event key edited in LaunchDarkly", editedKey, "Statsig SQL has changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, tc.sql), previewCols: customFieldPreviewCols}
			srv := f.server()
			defer srv.Close()

			e := customFieldEngine(t, srv.URL)
			e.updateCustomFields = true
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			warned := strings.Join(e.report.Warnings, " ")
			if f.patchCount != 0 || len(e.report.Notes) != 0 || (tc.warning == "") != (warned == "") || !strings.Contains(warned, tc.warning) {
				t.Errorf("patches=%d notes=%+v warnings=%q, want no PATCH and warning %q", f.patchCount, e.report.Notes, e.report.Warnings, tc.warning)
			}
		})
	}
}

func TestDryRun_ReportsMissingCustomFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update bool
		want   string
	}{
		{"without the flag", false, "Checkout Events (query): would skip (lacks its Statsig custom fields; --update-custom-fields can add them)"},
		{"with --update-custom-fields", true, `Checkout Events (query): would update (adds custom fields "price_usd_cents")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, wantCheckoutSQL), previewCols: customFieldPreviewCols}
			srv := f.server()
			defer srv.Close()

			e := customFieldEngine(t, srv.URL)
			e.dryRun, e.ldReadable, e.updateCustomFields = true, true, tc.update
			l := e.dryRunDataSources("snowflake")
			if len(l.lines) != 1 || l.lines[0] != tc.want {
				t.Errorf("lines = %q, want %q", l.lines, tc.want)
			}
			if f.patchCount != 0 || len(f.created) != 0 {
				t.Errorf("dry run wrote: patches=%d created=%d", f.patchCount, len(f.created))
			}
			if warned := strings.Contains(strings.Join(l.warnings, " "), "--update-custom-fields"); warned == tc.update {
				t.Errorf("warnings = %q", l.warnings)
			}
		})
	}
}

var macroField = []any{map[string]any{"key": "recent", "formula": "ts > {statsig_start_date}"}}

func TestDryRun_UnusableCustomFieldIsReportedWithoutPreviews(t *testing.T) {
	t.Run("new data source is refused", func(t *testing.T) {
		t.Chdir(t.TempDir())
		e := newPhase3Engine(t, "http://unused", false)
		e.dryRun = true
		e.metricSources[0]["customFieldMapping"] = macroField
		l := e.dryRunDataSources("snowflake")
		if len(l.lines) != 1 || !strings.HasPrefix(l.lines[0], "Checkout Events (query): would refuse (") || !strings.Contains(l.lines[0], `Statsig custom field "recent"`) {
			t.Errorf("lines = %q", l.lines)
		}
	})
	t.Run("existing data source is kept with the reason", func(t *testing.T) {
		for _, update := range []bool{false, true} {
			t.Chdir(t.TempDir())
			f := &fakeLD{t: t, list: wrappedCheckoutWithSQL(t, wantCheckoutSQL), noIntegration: true}
			srv := f.server()
			defer srv.Close()
			e := newPhase3Engine(t, srv.URL, false)
			e.dryRun, e.ldReadable, e.updateCustomFields = true, true, update
			e.metricSources[0]["customFieldMapping"] = macroField
			l := e.dryRunDataSources("snowflake")
			warned := strings.Join(l.warnings, " ")
			if len(l.lines) != 1 || l.lines[0] != "Checkout Events (query): would skip (already has the constant event key)" ||
				!strings.Contains(warned, `Statsig custom field "recent"`) || strings.Contains(warned, "Rerun with --update-custom-fields") {
				t.Errorf("update=%v: lines=%q warnings=%q", update, l.lines, l.warnings)
			}
		}
	})
}

func TestPhase3_CustomFieldPreviewProblemsFailTheSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		f      *fakeLD
		reason string
	}{
		{"failed preview without the constant event key", &fakeLD{previewStatus: 400}, "custom fields failed"},
		{"a field named like a source column", &fakeLD{previewCols: customFieldPreviewCols + `,{"name":"price_usd_cents","type":"NUMBER"}`}, `Statsig custom field(s) "price_usd_cents"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			tc.f.t = t
			srv := tc.f.server()
			defer srv.Close()
			e := customFieldEngine(t, srv.URL)
			e.constantEventKey = tc.f.previewStatus == 0
			if err := e.phase3aMigrateDataSources(); err != nil {
				t.Fatal(err)
			}
			if len(tc.f.created) != 0 || e.report.DataSources.Failed != 1 || len(e.report.Errors) != 1 || !strings.Contains(e.report.Errors[0], tc.reason) {
				t.Errorf("created=%d failed=%d errors=%q, want %q", len(tc.f.created), e.report.DataSources.Failed, e.report.Errors, tc.reason)
			}
		})
	}
}

func TestPhase3_DuplicatesInTheStatsigSQLAloneAreNotBlamedOnCustomFields(t *testing.T) {
	t.Chdir(t.TempDir())
	f := &fakeLD{t: t, previewCols: customFieldPreviewCols + `,{"name":"user_id","type":"TEXT"}`}
	srv := f.server()
	defer srv.Close()
	e := customFieldEngine(t, srv.URL)
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || len(e.report.Errors) != 0 {
		t.Errorf("created=%d errors=%q, want it created", len(f.created), e.report.Errors)
	}
}

func TestWarehouse_UpdateCustomFieldsNeedsTheConstantEventKey(t *testing.T) {
	whFlagCustomFields, whFlagConstantEventKey, whFlagStatsigExportFile = true, false, "export.json"
	t.Cleanup(func() { whFlagCustomFields, whFlagConstantEventKey, whFlagStatsigExportFile = false, true, "" })
	if err := runWarehouse(warehouseCmd, nil); err == nil || !strings.HasPrefix(err.Error(), "--update-custom-fields requires --constant-event-key") {
		t.Errorf("err = %v", err)
	}
}

func TestWriteDryRunBodies_IncludesCustomFields(t *testing.T) {
	t.Chdir(t.TempDir())
	e := customFieldEngine(t, "http://unused")
	e.writeDryRunBodies("snowflake")
	raw, err := os.ReadFile("data-source-bodies.json")
	if err != nil {
		t.Fatal(err)
	}
	var bodies []map[string]any
	if err := json.Unmarshal(raw, &bodies); err != nil || len(bodies) != 1 || bodies[0]["sqlQuery"] != wantCheckoutCustomSQL {
		t.Errorf("data-source-bodies.json = %s (%v), want the query with custom fields", raw, err)
	}
}
