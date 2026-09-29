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

	// Empty means opted out, and LD rejects an empty-string maintainer, so the
	// key must be absent rather than present-and-blank.
	bare := MapMetricSourceToDataSource(src, "e", "k", "")
	if _, present := bare["maintainerId"]; present {
		t.Errorf("maintainerId should be absent when unset, got %v", bare["maintainerId"])
	}
}
