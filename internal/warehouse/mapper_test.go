package warehouse

import "testing"

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
