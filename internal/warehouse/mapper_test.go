package warehouse

import (
	"strings"
	"testing"
)

// A Statsig "table" source must convert to a SELECT * query, not a bare
// tableName — LD's metric-data-source editor only supports query-backed sources.
func TestMapMetricSourceToDataSource_TableEmitsQuery(t *testing.T) {
	src := map[string]any{
		"name":            "product_activity_events",
		"sourceType":      "table",
		"tableName":       "DB.SCHEMA.EVENTS",
		"timestampColumn": "received_time",
	}
	body := MapMetricSourceToDataSource(src, "some-env", "snowflake-experimentation", "")
	if _, ok := body["tableName"]; ok {
		t.Errorf("table source should not emit a bare tableName, got %v", body["tableName"])
	}
	if got := body["sqlQuery"]; got != "SELECT * FROM DB.SCHEMA.EVENTS" {
		t.Errorf("sqlQuery = %v, want %q", got, "SELECT * FROM DB.SCHEMA.EVENTS")
	}
}

func TestMapMetricSourceToDataSource_QueryPassthrough(t *testing.T) {
	src := map[string]any{
		"name":       "transaction_events",
		"sourceType": "query",
		"sql":        "SELECT a, b FROM t",
	}
	body := MapMetricSourceToDataSource(src, "some-env", "snowflake-experimentation", "")
	if got := body["sqlQuery"]; got != "SELECT a, b FROM t" {
		t.Errorf("sqlQuery = %v, want the passthrough query", got)
	}
	if _, ok := body["tableName"]; ok {
		t.Error("query source should not set tableName")
	}
}

// A source with only a tableName (no sourceType) still converts to a query.
func TestMapMetricSourceToDataSource_TableNameOnlyEmitsQuery(t *testing.T) {
	src := map[string]any{"name": "s", "tableName": "DB.T"}
	body := MapMetricSourceToDataSource(src, "e", "k", "")
	if got := body["sqlQuery"]; got != "SELECT * FROM DB.T" {
		t.Errorf("sqlQuery = %v, want %q", got, "SELECT * FROM DB.T")
	}
}

func TestMapMetricSourceToDataSource_Maintainer(t *testing.T) {
	src := map[string]any{"name": "Checkout", "sql": "SELECT 1"}

	body := MapMetricSourceToDataSource(src, "e", "k", "6917a463c1b64809c1124c34")
	if body["maintainerId"] != "6917a463c1b64809c1124c34" {
		t.Errorf("maintainerId = %v, want the configured member ID", body["maintainerId"])
	}

	// LD rejects an empty-string maintainer, so the key must be absent.
	bare := MapMetricSourceToDataSource(src, "e", "k", "")
	if _, present := bare["maintainerId"]; present {
		t.Errorf("maintainerId should be absent when unset, got %v", bare["maintainerId"])
	}
}

func TestMapMetricSourceToDataSource_ValueColumnOnlyWhenMapped(t *testing.T) {
	src := map[string]any{"name": "Orders", "sql": "SELECT 1"}
	for _, fields := range [][]any{nil, {map[string]any{"fieldName": "value"}}} {
		src["customFieldMapping"] = fields
		cm := MapMetricSourceToDataSource(src, "e", "k", "")["columnMappings"].(map[string]any)
		if v, present := cm["valueColumn"]; present {
			t.Errorf("fields %v: valueColumn = %q, want it absent", fields, v)
		}
	}

	src["customFieldMapping"] = []any{map[string]any{"fieldName": "amount", "column": "order_total"}}
	cm := MapMetricSourceToDataSource(src, "e", "k", "")["columnMappings"].(map[string]any)
	if cm["valueColumn"] != "order_total" {
		t.Errorf("valueColumn = %v, want order_total", cm["valueColumn"])
	}
}

// Context kinds match the analysis units metrics convert gives the same unit IDs.
func TestMapMetricSourceToDataSource_ContextKindsMatchAnalysisUnits(t *testing.T) {
	src := map[string]any{"name": "s", "sql": "SELECT 1", "idTypeMapping": []any{
		map[string]any{"statsigUnitID": "userID", "column": "uid"},
		map[string]any{"statsigUnitID": "anonymousUserID", "column": "anon_id"},
		map[string]any{"statsigUnitID": "user_id", "column": "legacy_uid"},
		map[string]any{"statsigUnitID": "companyID", "column": "company_id"},
	}}
	contexts := MapMetricSourceToDataSource(src, "", "", "")["columnMappings"].(map[string]any)["contexts"].(map[string]string)
	want := map[string]string{"user": "uid", "anonymoususerid": "anon_id", "user_id": "legacy_uid", "companyid": "company_id"}
	if len(contexts) != len(want) {
		t.Fatalf("contexts = %v, want %v", contexts, want)
	}
	for kind, col := range want {
		if contexts[kind] != col {
			t.Errorf("contexts = %v, want %v", contexts, want)
		}
	}
	if c := ContextKindConflicts(src); c != nil {
		t.Errorf("conflicts = %+v, want none", c)
	}
}

func TestContextKindConflicts(t *testing.T) {
	src := map[string]any{"idTypeMapping": []any{
		map[string]any{"statsigUnitID": "userID", "column": "user_id"},
		map[string]any{"statsigUnitID": "UserId", "column": "USER_ID"},
		map[string]any{"statsigUnitID": "companyID", "column": "company_id"},
		map[string]any{"statsigUnitID": "user", "column": "uid"},
	}}
	c := ContextKindConflicts(src)
	if len(c) != 1 || c[0].Kind != "user" || strings.Join(c[0].Units, ",") != "userID,UserId,user" || strings.Join(c[0].Columns, ",") != "user_id,USER_ID,uid" {
		t.Errorf("conflicts = %+v", c)
	}
}

func TestColumnTypes(t *testing.T) {
	for _, typ := range []string{"TIMESTAMP_NTZ", "timestamp_ltz(9)", "TIMESTAMP_TZ", "TIMESTAMP", "TIMESTAMPTZ", "DATE", "DATETIME", "TIMESTAMP WITH TIME ZONE"} {
		if !IsTimestampType(typ) {
			t.Errorf("IsTimestampType(%q) = false", typ)
		}
	}
	for _, typ := range []string{"TEXT", "VARCHAR(64)", "NUMBER", "TIME", "STRING"} {
		if IsTimestampType(typ) {
			t.Errorf("IsTimestampType(%q) = true", typ)
		}
	}
	for _, typ := range []string{"NUMBER(38,0)", "fixed", "FLOAT64", "DOUBLE PRECISION", "INT8", "BIGNUMERIC", "Nullable(Float64)"} {
		if !IsNumericType(typ) {
			t.Errorf("IsNumericType(%q) = false", typ)
		}
	}
	for _, typ := range []string{"TEXT", "VARCHAR(16)", "BOOLEAN", "TIMESTAMP_NTZ"} {
		if IsNumericType(typ) {
			t.Errorf("IsNumericType(%q) = true", typ)
		}
	}
}
