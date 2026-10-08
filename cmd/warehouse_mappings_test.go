package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

// mappingSource is checkoutSource with every mapping the export can define.
func mappingSource() map[string]any {
	s := checkoutSource()
	s["timestampColumn"] = "created_at"
	s["idTypeMapping"] = []any{
		map[string]any{"statsigUnitID": "userID", "column": "uid"},
		map[string]any{"statsigUnitID": "account", "column": "account_id"},
	}
	s["customFieldMapping"] = []any{map[string]any{"fieldName": "value", "column": "order_total"}}
	return s
}

func mappingColumns() []any {
	var cols []any
	for _, name := range []string{"TS", "CREATED_AT", "USER_ID", "UID", "ACCOUNT_ID", "ORDER_TOTAL", "LD_EVENT_KEY"} {
		cols = append(cols, map[string]any{"name": name, "type": "TEXT"})
	}
	return cols
}

func dsList(t *testing.T, sql string, cm map[string]any) string {
	t.Helper()
	if _, ok := cm["columns"]; !ok {
		cm["columns"] = mappingColumns()
	}
	raw, err := json.Marshal(map[string]any{"items": []any{map[string]any{
		"key": "checkout-events", "environmentKey": "production", "sqlQuery": sql, "columnMappings": cm,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func opLines(ops []launchdarkly.JSONPatchOp) string {
	var lines []string
	for _, op := range ops {
		line := op.Op + " " + op.Path
		if op.Op != "test" && op.Op != "remove" {
			v, _ := json.Marshal(op.Value)
			line += " " + string(v)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func runMappings(t *testing.T, f *fakeLD, setup func(*migrationEngine)) *migrationEngine {
	t.Helper()
	t.Chdir(t.TempDir())
	srv := f.server()
	t.Cleanup(srv.Close)
	e := newPhase3Engine(t, srv.URL, false)
	e.updateMappings = true
	e.metricSources = []map[string]any{mappingSource()}
	if setup != nil {
		setup(e)
	}
	if err := e.phase3aMigrateDataSources(); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestUpdateMappings_WrappedSourceGetsExportMappings(t *testing.T) {
	cm := map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"}}
	f := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL, cm)}
	e := runMappings(t, f, nil)

	if e.report.DataSources.Updated != 1 || f.patchCount != 1 || len(f.previews) != 0 || f.metricsReads != 0 {
		t.Fatalf("report %+v patches=%d previews=%d metricsReads=%d errors=%q, want one PATCH and no preview or metrics read",
			e.report.DataSources, f.patchCount, len(f.previews), f.metricsReads, e.report.Errors)
	}
	ops := f.patches["checkout-events"]
	want := `test /sqlQuery
test /columnMappings
replace /columnMappings/timestampColumn "CREATED_AT"
add /columnMappings/valueColumn "ORDER_TOTAL"
add /columnMappings/contexts/account "ACCOUNT_ID"
replace /columnMappings/contexts/user "UID"`
	if got := opLines(ops); got != want {
		t.Errorf("ops:\n%s\nwant:\n%s", got, want)
	}
	if ops[0].Value != wantCheckoutSQL {
		t.Errorf("test /sqlQuery value = %v", ops[0].Value)
	}
	if got, _ := json.Marshal(ops[1].Value); !strings.Contains(string(got), `"timestampColumn":"TS"`) {
		t.Errorf("test /columnMappings value = %s, want the listed mappings", got)
	}
	wantNote := reportNote{Code: noteMappingsUpdated, DataSource: "checkout-events",
		Changes: "timestamp: TS → CREATED_AT; value column: (none) → ORDER_TOTAL; context account: (none) → ACCOUNT_ID; context user: USER_ID → UID"}
	if len(e.report.Notes) != 1 || e.report.Notes[0] != wantNote {
		t.Errorf("notes = %+v\nwant %+v", e.report.Notes, wantNote)
	}
	if len(e.report.Warnings) != 0 {
		t.Errorf("warnings = %q, want none", e.report.Warnings)
	}
}

func TestUpdateMappings_UnwrappedWithoutOverwriteChangesOnlyMappings(t *testing.T) {
	cm := map[string]any{"keyColumn": "EVENT_NAME", "timestampColumn": "CREATED_AT", "valueColumn": "ORDER_TOTAL",
		"contexts": map[string]any{"user": "USER_ID", "account": "ACCOUNT_ID"}}
	f := &fakeLD{t: t, list: dsList(t, "SELECT * FROM analytics.events", cm)}
	e := runMappings(t, f, nil)

	want := `test /sqlQuery
test /columnMappings
replace /columnMappings/contexts/user "UID"`
	if got := opLines(f.patches["checkout-events"]); got != want || len(f.previews) != 0 {
		t.Errorf("ops:\n%s\nwant:\n%s\npreviews=%d", got, want, len(f.previews))
	}
	// Still lacks the constant event key, so the summary line stays.
	if e.report.DataSources.Updated != 1 || len(e.report.Warnings) != 1 || !strings.HasPrefix(e.report.Warnings[0], "1 data source(s) already exist without the constant event key") {
		t.Errorf("report %+v warnings=%q", e.report.DataSources, e.report.Warnings)
	}
}

func TestUpdateMappings_NoChangesSkipsThePatch(t *testing.T) {
	cm := map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "CREATED_AT", "valueColumn": "ORDER_TOTAL",
		"contexts": map[string]any{"user": "UID", "account": "ACCOUNT_ID"}}
	f := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL, cm)}
	e := runMappings(t, f, nil)
	if f.patchCount != 0 || e.report.DataSources.Skipped != 1 || len(e.report.Warnings) != 0 || len(e.report.Notes) != 0 {
		t.Errorf("patches=%d report %+v warnings=%q notes=%+v, want a quiet skip", f.patchCount, e.report.DataSources, e.report.Warnings, e.report.Notes)
	}
}

// Units are listed per metric, so a ratio bound only through its denominator counts.
func TestUpdateMappings_RefusesToRemoveAContextKindBoundMetricsUse(t *testing.T) {
	cm := map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID", "account": "ACCOUNT_ID"}}
	metrics := `[
		{"key":"accounts-converted","analysisUnits":["account"],"randomizationUnits":["account"],"dataSource":{"key":"checkout-events"}},
		{"key":"ratio-by-account","randomizationUnits":["user","account"],"dataSource":{"key":"orders"},"denominator":{"dataSource":{"key":"checkout-events"}}},
		{"key":"user-only","analysisUnits":["user"],"dataSource":{"key":"checkout-events"}},
		{"key":"elsewhere","analysisUnits":["account"],"dataSource":{"key":"page-views"}}]`
	f := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL, cm), metrics: metrics}
	e := runMappings(t, f, func(e *migrationEngine) {
		e.metricSources[0]["idTypeMapping"] = []any{map[string]any{"statsigUnitID": "userID", "column": "uid"}}
	})
	if f.patchCount != 0 || e.report.DataSources.Failed != 1 {
		t.Fatalf("patches=%d report %+v, want a refusal", f.patchCount, e.report.DataSources)
	}
	want := `Data source "Checkout Events": not updated: the Statsig export no longer maps context kind(s) "account", which bound metrics use as analysis units: accounts-converted (account), ratio-by-account (account). Change those metrics' analysis units in LaunchDarkly, or map the kind in the Statsig source, then rerun`
	if len(e.report.Errors) != 1 || e.report.Errors[0] != want {
		t.Errorf("errors = %q\nwant %q", e.report.Errors, want)
	}

	// Changing a kind's column is allowed.
	f2 := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL, cm), metrics: metrics}
	e = runMappings(t, f2, nil)
	if e.report.DataSources.Updated != 1 || f2.metricsReads != 0 {
		t.Errorf("report %+v metricsReads=%d errors=%q, want an update without reading metrics", e.report.DataSources, f2.metricsReads, e.report.Errors)
	}
}

func TestUpdateMappings_KeepsAValueColumnBoundNumericMetricsRead(t *testing.T) {
	cm := map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "valueColumn": "ORDER_TOTAL",
		"contexts": map[string]any{"user": "UID", "account": "ACCOUNT_ID"}}
	metrics := `[
		{"key":"order-total-sum","isNumeric":true,"dataSource":{"key":"checkout-events"}},
		{"key":"order-total-own","isNumeric":true,"valueColumn":"ORDER_TOTAL","dataSource":{"key":"checkout-events"}},
		{"key":"distinct-orders","isNumeric":true,"unitAggregationType":"count_distinct","unitAggregationField":"ORDER_ID","dataSource":{"key":"checkout-events"}},
		{"key":"checkouts","isNumeric":false,"dataSource":{"key":"checkout-events"}},
		{"key":"avg-order-value","isNumeric":false,"dataSource":{"key":"orders"},"denominator":{"isNumeric":true,"dataSource":{"key":"checkout-events"}}},
		{"key":"ratio-inherited","isNumeric":false,"dataSource":{"key":"checkout-events"},"denominator":{"isNumeric":true,"dataSource":{"key":"launchdarkly-hosted"}}},
		{"key":"page-value","isNumeric":true,"dataSource":{"key":"page-views"}}]`
	noValue := func(e *migrationEngine) { delete(e.metricSources[0], "customFieldMapping") }

	f := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL, cm), metrics: metrics}
	e := runMappings(t, f, noValue)
	if got := opLines(f.patches["checkout-events"]); got != "test /sqlQuery\ntest /columnMappings\nreplace /columnMappings/timestampColumn \"CREATED_AT\"" {
		t.Errorf("ops:\n%s\nwant the timestamp change only", got)
	}
	want := `Data source "checkout-events": its value column "ORDER_TOTAL" was kept: the Statsig export maps none, but bound numeric metrics without their own value column read it (avg-order-value (denominator), order-total-sum, ratio-inherited (denominator)). Set their value column, then rerun to remove it.`
	if e.report.DataSources.Updated != 1 || len(e.report.Warnings) != 1 || e.report.Warnings[0] != want {
		t.Errorf("report %+v warnings = %q\nwant %q", e.report.DataSources, e.report.Warnings, want)
	}

	f2 := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL, cm), metrics: `[{"key":"order-total-own","isNumeric":true,"valueColumn":"ORDER_TOTAL","dataSource":{"key":"checkout-events"}}]`}
	e = runMappings(t, f2, noValue)
	if got := opLines(f2.patches["checkout-events"]); !strings.HasSuffix(got, "remove /columnMappings/valueColumn") || len(e.report.Warnings) != 0 {
		t.Errorf("ops:\n%s\nwarnings=%q, want the value column removed quietly", got, e.report.Warnings)
	}
}

func TestUpdateMappings_FailsClosedWhenMetricsCannotBeRead(t *testing.T) {
	cm := map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "valueColumn": "ORDER_TOTAL", "contexts": map[string]any{"user": "UID"}}
	f := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL, cm), metricsStatus: http.StatusForbidden}
	e := runMappings(t, f, func(e *migrationEngine) { delete(e.metricSources[0], "customFieldMapping") })
	if f.patchCount != 0 || e.report.DataSources.Failed != 1 || !strings.Contains(e.report.Errors[0], "not updated, because its bound metrics could not be checked") {
		t.Errorf("patches=%d report %+v errors=%q, want a refusal", f.patchCount, e.report.DataSources, e.report.Errors)
	}
}

func TestUpdateMappings_ExportColumnMissingFromTheDataSource(t *testing.T) {
	cm := map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"}}
	f := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL, cm)}
	e := runMappings(t, f, func(e *migrationEngine) {
		e.metricSources[0]["timestampColumn"] = "event_time"
		e.metricSources[0]["idTypeMapping"] = []any{
			map[string]any{"statsigUnitID": "userID", "column": "uid"},
			map[string]any{"statsigUnitID": "account", "column": "acct"},
		}
	})
	want := `test /sqlQuery
test /columnMappings
add /columnMappings/valueColumn "ORDER_TOTAL"
replace /columnMappings/contexts/user "UID"`
	if got := opLines(f.patches["checkout-events"]); got != want {
		t.Errorf("ops:\n%s\nwant:\n%s", got, want)
	}
	wantWarn := `Data source "checkout-events": its query does not return the columns the Statsig export maps for timestamp ("event_time"), context account ("acct"), so those mappings were not taken from the export; correct them in Statsig or add them to its query, then rerun.`
	if len(e.report.Warnings) != 1 || e.report.Warnings[0] != wantWarn {
		t.Errorf("warnings = %q\nwant %q", e.report.Warnings, wantWarn)
	}
}

func TestUpdateMappings_WithOverwriteTakesMappingsFromTheExport(t *testing.T) {
	cm := map[string]any{"keyColumn": "EVENT_KEY", "timestampColumn": "TS", "valueColumn": "ORDER_TOTAL",
		"contexts": map[string]any{"user": "USER_ID", "account": "account_id"}, "columns": []any{}}
	f := &fakeLD{t: t, list: dsList(t, "SELECT * FROM analytics.events", cm),
		previewCols: `{"name":"TS","type":"TIMESTAMP_NTZ"},{"name":"CREATED_AT","type":"TIMESTAMP_NTZ"},{"name":"USER_ID","type":"TEXT"},{"name":"UID","type":"TEXT"},{"name":"ACCOUNT_ID","type":"TEXT"},{"name":"ORDER_TOTAL","type":"NUMBER"},{"name":"EVENT_KEY","type":"TEXT"}`}
	e := runMappings(t, f, func(e *migrationEngine) { e.overwrite = true })
	if e.report.DataSources.Updated != 1 {
		t.Fatalf("report %+v errors=%q", e.report.DataSources, e.report.Errors)
	}
	ops := f.patches["checkout-events"]
	var paths []string
	for _, op := range ops {
		paths = append(paths, op.Op+" "+op.Path)
	}
	want := []string{"test /sqlQuery", "test /columnMappings", "add /sqlQuery", "add /columnMappings/keyColumn", "replace /columnMappings/columns",
		"replace /columnMappings/timestampColumn", "replace /columnMappings/contexts/account", "replace /columnMappings/contexts/user"}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ops:\n%s\nwant:\n%s", strings.Join(paths, "\n"), strings.Join(want, "\n"))
	}
	if ops[2].Value != wantCheckoutSQL || ops[3].Value != "LD_EVENT_KEY" || ops[5].Value != "CREATED_AT" || ops[6].Value != "ACCOUNT_ID" || ops[7].Value != "UID" {
		t.Errorf("values: %v / %v / %v / %v / %v", ops[2].Value, ops[3].Value, ops[5].Value, ops[6].Value, ops[7].Value)
	}
	if len(e.report.Notes) != 1 || e.report.Notes[0].Changes != "timestamp: TS → CREATED_AT; context account: account_id → ACCOUNT_ID; context user: USER_ID → UID" {
		t.Errorf("notes = %+v", e.report.Notes)
	}

	// Without --update-mappings, --overwrite still keeps the existing mappings.
	f2 := &fakeLD{t: t, list: f.list, previewCols: f.previewCols}
	e = runMappings(t, f2, func(e *migrationEngine) { e.overwrite, e.updateMappings = true, false })
	if got := opLines(f2.patches["checkout-events"]); strings.Contains(got, "timestampColumn") || strings.Contains(got, "contexts/user") || !strings.Contains(got, `contexts/account "ACCOUNT_ID"`) {
		t.Errorf("ops without --update-mappings:\n%s", got)
	}
}

func TestUpdateMappings_WithOverwriteFallsBackForMissingColumns(t *testing.T) {
	cm := map[string]any{"keyColumn": "EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"}, "columns": []any{}}
	f := &fakeLD{t: t, list: dsList(t, "SELECT * FROM analytics.events", cm)}
	e := runMappings(t, f, func(e *migrationEngine) { e.overwrite = true })
	// The default preview has TS, USER_ID, and EVENT_KEY only: no export mapping resolves.
	got := opLines(f.patches["checkout-events"])
	if strings.Contains(got, "timestampColumn") || strings.Contains(got, "contexts") || strings.Contains(got, "valueColumn") {
		t.Errorf("ops:\n%s\nwant the existing mappings kept", got)
	}
	if len(e.report.Warnings) != 2 || !strings.Contains(e.report.Warnings[0], `timestamp ("created_at"), value ("order_total"), context account ("account_id"), context user ("uid")`) {
		t.Errorf("warnings = %q", e.report.Warnings)
	}
}

func TestUpdateMappings_KeyInAnotherEnvironmentIsStillRefused(t *testing.T) {
	list := `{"items":[{"key":"checkout-events","environmentKey":"staging","sqlQuery":"SELECT * FROM analytics.events","columnMappings":{"keyColumn":"EVENT_NAME","timestampColumn":"TS","contexts":{"user":"USER_ID"}}}]}`
	f := &fakeLD{t: t, list: list}
	e := runMappings(t, f, nil)
	if f.patchCount != 0 || e.report.DataSources.Failed != 1 || !strings.Contains(e.report.Errors[0], `environment "staging"`) {
		t.Errorf("patches=%d report %+v errors=%q", f.patchCount, e.report.DataSources, e.report.Errors)
	}
}

func TestUpdateMappings_PatchErrorsSayWhatToDo(t *testing.T) {
	cm := map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID"}}
	for body, want := range map[string]string{
		`{"code":"invalid_request","message":"Error applying json-patch document"}`: "not updated: it changed in LaunchDarkly after this run read it; rerun to pick up the change",
		`{"code":"invalid_request","message":"Columns do not match query results"}`: "not updated: LaunchDarkly reran its query to check the change, and the warehouse now returns different columns than the data source lists. Run its query preview in LaunchDarkly and save it to refresh the columns, then rerun",
	} {
		f := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL, cm), patchStatus: http.StatusBadRequest, patchBody: body}
		e := runMappings(t, f, nil)
		if e.report.DataSources.Failed != 1 || len(e.report.Errors) != 1 || e.report.Errors[0] != `Data source "Checkout Events": `+want {
			t.Errorf("report %+v errors=%q\nwant %q", e.report.DataSources, e.report.Errors, want)
		}
	}
}

func TestUpdateMappings_DryRun(t *testing.T) {
	t.Chdir(t.TempDir())
	item := func(key, sql string, cm map[string]any) map[string]any {
		cm["columns"] = mappingColumns()
		return map[string]any{"key": key, "environmentKey": "production", "sqlQuery": sql, "columnMappings": cm}
	}
	raw, _ := json.Marshal(map[string]any{"items": []any{
		item("checkout-events", wantCheckoutSQL, map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "valueColumn": "ORDER_TOTAL",
			"contexts": map[string]any{"user": "USER_ID", "account": "ACCOUNT_ID"}}),
		item("orders", "SELECT * FROM analytics.orders", map[string]any{"keyColumn": "EVENT_NAME", "timestampColumn": "CREATED_AT", "valueColumn": "ORDER_TOTAL",
			"contexts": map[string]any{"user": "UID", "account": "ACCOUNT_ID"}}),
		item("refunds", "SELECT * FROM analytics.refunds", map[string]any{"keyColumn": "EVENT_NAME", "timestampColumn": "CREATED_AT",
			"contexts": map[string]any{"user": "UID", "account": "ACCOUNT_ID"}}),
	}})
	metrics := `[{"key":"refund-accounts","analysisUnits":["account"],"dataSource":{"key":"refunds"}},
		{"key":"order-total-sum","isNumeric":true,"dataSource":{"key":"checkout-events"}}]`
	f := &fakeLD{t: t, list: string(raw), metrics: metrics}
	srv := f.server()
	defer srv.Close()

	named := func(name string, edit func(map[string]any)) map[string]any {
		s := mappingSource()
		s["name"] = name
		if edit != nil {
			edit(s)
		}
		return s
	}
	e := newPhase3Engine(t, srv.URL, false)
	e.dryRun, e.ldReadable, e.updateMappings = true, true, true
	e.metricSources = []map[string]any{
		named("Checkout Events", func(s map[string]any) {
			delete(s, "customFieldMapping")
			s["idTypeMapping"] = []any{map[string]any{"statsigUnitID": "userID", "column": "uid"}, map[string]any{"statsigUnitID": "account", "column": "acct"}}
		}),
		named("Orders", nil),
		named("Refunds", func(s map[string]any) {
			s["idTypeMapping"] = []any{map[string]any{"statsigUnitID": "userID", "column": "uid"}}
		}),
		named("Page Views", nil),
	}
	l := e.dryRunDataSources("snowflake")
	want := []string{
		"Checkout Events (query): would update mappings\n        timestamp: TS → CREATED_AT\n        context user: USER_ID → UID",
		"Refunds (query): would refuse (removes context kind(s) \"account\", used by bound metrics: refund-accounts (account))",
		"Page Views (query): would create",
		"1 existing data source(s): would skip (no mapping changes)",
	}
	if strings.Join(l.lines, "\n") != strings.Join(want, "\n") || l.total != 4 {
		t.Errorf("total=%d lines:\n%s\nwant:\n%s", l.total, strings.Join(l.lines, "\n"), strings.Join(want, "\n"))
	}
	wantWarnings := []string{
		`Data source "checkout-events": its query does not return the columns the Statsig export maps for context account ("acct"), so those mappings were not taken from the export; correct them in Statsig or add them to its query, then rerun. Its value column "ORDER_TOTAL" was kept: the Statsig export maps none, but bound numeric metrics without their own value column read it (order-total-sum). Set their value column, then rerun to remove it.`,
		withoutConstantKeySummary(1),
	}
	if strings.Join(l.warnings, "\n") != strings.Join(wantWarnings, "\n") {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(l.warnings, "\n"), strings.Join(wantWarnings, "\n"))
	}
	if f.patchCount != 0 || len(f.previews) != 0 {
		t.Errorf("dry run wrote or previewed: patches=%d previews=%d", f.patchCount, len(f.previews))
	}

	e.ldReadable = false
	l = e.dryRunDataSources("snowflake")
	if l.checked || !strings.Contains(l.note, "were not checked") || len(l.lines) != 4 || l.lines[0] != "Checkout Events (query): would create" {
		t.Errorf("without credentials: checked=%v note=%q lines=%q", l.checked, l.note, l.lines)
	}
}

func TestUpdateMappings_DryRunWithOverwriteEstimatesTheChanges(t *testing.T) {
	t.Chdir(t.TempDir())
	cm := map[string]any{"keyColumn": "EVENT_KEY", "timestampColumn": "TS", "valueColumn": "ORDER_TOTAL", "contexts": map[string]any{"user": "USER_ID", "account": "account_id"}}
	f := &fakeLD{t: t, list: dsList(t, "SELECT * FROM analytics.events", cm)}
	srv := f.server()
	defer srv.Close()
	e := newPhase3Engine(t, srv.URL, false)
	e.dryRun, e.ldReadable, e.updateMappings, e.overwrite = true, true, true, true
	e.metricSources = []map[string]any{mappingSource()}
	l := e.dryRunDataSources("snowflake")
	want := "Checkout Events (query): would update\n        timestamp: TS → created_at\n        context user: USER_ID → uid"
	if len(l.lines) != 1 || l.lines[0] != want {
		t.Errorf("lines = %q\nwant %q", l.lines, want)
	}
}

func TestDiffMappings_EscapesContextKinds(t *testing.T) {
	old := map[string]any{"timestampColumn": "TS", "contexts": map[string]any{"user": "USER_ID", "a/b~c": "X"}}
	d := diffMappings(old, warehouse.ExportMappings{Contexts: map[string]string{"user": "USER_ID"}}, newColumnSet([]any{map[string]any{"name": "USER_ID"}}), false)
	if got := opLines(d.ops); got != "remove /columnMappings/contexts/a~1b~0c" || fmt.Sprint(d.removedKinds) != "[a/b~c]" {
		t.Errorf("ops = %q removed=%v", got, d.removedKinds)
	}
}

func TestMappingsFromExport_OnlyWhatTheSourceDefines(t *testing.T) {
	m := warehouse.MappingsFromExport(map[string]any{"name": "s", "sql": "SELECT 1"})
	if m.Timestamp != "" || m.Value != "" || m.Contexts != nil {
		t.Errorf("mappings = %+v, want none: the create path's defaults are not the export's", m)
	}
	m = warehouse.MappingsFromExport(mappingSource())
	if m.Timestamp != "created_at" || m.Value != "order_total" || fmt.Sprint(m.Contexts) != "map[account:account_id user:uid]" {
		t.Errorf("mappings = %+v", m)
	}
}

// The query of a wrapper edited in LaunchDarkly is kept; only its mappings change.
func TestUpdateMappings_EditedWrapperKeepsItsQuery(t *testing.T) {
	cm := map[string]any{"keyColumn": "LD_EVENT_KEY", "timestampColumn": "TS", "contexts": map[string]any{"user": "UID", "account": "ACCOUNT_ID"}, "valueColumn": "ORDER_TOTAL"}
	f := &fakeLD{t: t, list: dsList(t, wantCheckoutSQL+"\nWHERE ts > '2024-01-01'", cm)}
	e := runMappings(t, f, func(e *migrationEngine) { e.overwrite = true })
	if got := opLines(f.patches["checkout-events"]); got != "test /sqlQuery\ntest /columnMappings\nreplace /columnMappings/timestampColumn \"CREATED_AT\"" || len(f.previews) != 0 {
		t.Errorf("ops:\n%s\npreviews=%d", got, len(f.previews))
	}
	if e.report.DataSources.Updated != 1 || len(e.report.Notes) != 2 || e.report.Notes[0].Code != noteEditedConstantKey || e.report.Notes[1].Code != noteMappingsUpdated {
		t.Errorf("report %+v notes=%+v", e.report.DataSources, e.report.Notes)
	}
}
