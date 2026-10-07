package warehouse

import (
	"strings"
	"testing"
)

func TestPrepareSourceSQL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{"plain", "SELECT a FROM t", "SELECT a FROM t", ""},
		{"trailing semicolon", "SELECT a FROM t;\n", "SELECT a FROM t", ""},
		{"several semicolons", "SELECT a FROM t ; ;", "SELECT a FROM t", ""},
		{"semicolon then line comment", "SELECT a FROM t; -- done", "SELECT a FROM t", ""},
		{"semicolon then block comment", "SELECT a FROM t; /* done */", "SELECT a FROM t", ""},
		{"trailing line comment kept", "SELECT a FROM t -- last 90 days", "SELECT a FROM t -- last 90 days", ""},
		{"semicolon inside string", "SELECT 'a;b' AS x FROM t", "SELECT 'a;b' AS x FROM t", ""},
		{"semicolon inside comment", "SELECT a -- x;y\nFROM t", "SELECT a -- x;y\nFROM t", ""},
		{"escaped quote", "SELECT 'it''s;' AS x FROM t;", "SELECT 'it''s;' AS x FROM t", ""},
		{"dollar string", "SELECT $$a;b$$ AS x FROM t;", "SELECT $$a;b$$ AS x FROM t", ""},
		{"apostrophe in comment", "SELECT a FROM t -- don't count bots\n;", "SELECT a FROM t -- don't count bots", ""},
		{"double dash inside string", "SELECT '--x' AS a FROM t;", "SELECT '--x' AS a FROM t", ""},
		{"url in string with //", "SELECT 'https://example.com/a;b' AS u FROM t; // done", "SELECT 'https://example.com/a;b' AS u FROM t", ""},
		{"backslash-escaped quote in string", `SELECT 'it\'s;' AS x FROM t;`, `SELECT 'it\'s;' AS x FROM t`, ""},
		{"escaped backslash at end of string", `SELECT 'C:\\' AS p FROM t;`, `SELECT 'C:\\' AS p FROM t`, ""},
		{"quoted identifier ending in backslash", `SELECT "dir\" AS d, ts FROM t;`, `SELECT "dir\" AS d, ts FROM t`, ""},
		{"quoted identifier with doubled quote", `SELECT "a""b;" AS d FROM t;`, `SELECT "a""b;" AS d FROM t`, ""},
		{"dollar block with quote and semicolon", "SELECT $$it's; ok$$ AS x FROM t ;", "SELECT $$it's; ok$$ AS x FROM t", ""},
		{"hash is not a comment on snowflake", "SELECT \"a#b\" FROM t # not a comment", "SELECT \"a#b\" FROM t # not a comment", ""},
		{"json literal with braces", `SELECT PARSE_JSON('{"a":{"b":1}}') AS j FROM t;`, `SELECT PARSE_JSON('{"a":{"b":1}}') AS j FROM t`, ""},
		{"object constant", "SELECT {'a': 1} AS o FROM t", "SELECT {'a': 1} AS o FROM t", ""},
		{"semi-structured path and cast", "SELECT props:plan::string AS plan FROM t;", "SELECT props:plan::string AS plan FROM t", ""},
		{"qualify and order by limit", "SELECT * FROM t QUALIFY ROW_NUMBER() OVER (PARTITION BY u ORDER BY ts) = 1 ORDER BY ts LIMIT 10;", "SELECT * FROM t QUALIFY ROW_NUMBER() OVER (PARTITION BY u ORDER BY ts) = 1 ORDER BY ts LIMIT 10", ""},
		{"union", "SELECT a FROM t UNION ALL SELECT a FROM u;", "SELECT a FROM t UNION ALL SELECT a FROM u", ""},
		{"comment then semicolon then whitespace", "SELECT a FROM t /* c */ ;  \n\t", "SELECT a FROM t /* c */", ""},
		{"block comment containing quote", "SELECT a /* don't */ FROM t;", "SELECT a /* don't */ FROM t", ""},
		{"macro in a line comment", "SELECT a, ts FROM t -- was: ds >= {statsig_start_date} in Statsig", "SELECT a, ts FROM t -- was: ds >= {statsig_start_date} in Statsig", ""},
		{"macro in a block comment", "SELECT a /* {statsig_end_date} */ FROM t", "SELECT a /* {statsig_end_date} */ FROM t", ""},
		{"macro in a string literal", "SELECT '{statsig_start_date}' AS note FROM t", "SELECT '{statsig_start_date}' AS note FROM t", ""},
		{"two statements", "SET x = 1; SELECT a FROM t", "", "more than one statement"},
		{"statsig macro", "SELECT * FROM t WHERE ds >= {statsig_start_date}", "", "{statsig_start_date}"},
		{"macro in code and in a comment", "SELECT * FROM t -- {statsig_start_date}\nWHERE ds <= {statsig_end_date}", "", "[{statsig_end_date}]"},
		{"unterminated block comment", "SELECT a FROM t /* oops", "", "unterminated"},
		{"only a comment", "-- nothing", "", "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PrepareSourceSQL(tt.in, "snowflake")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// On BigQuery and Databricks "..." is a string, where a backslash escapes the quote.
func TestPrepareSourceSQL_DoubleQuotedStringsOnBigQuery(t *testing.T) {
	in := `SELECT "say \"hi\";" AS s FROM t;`
	for _, wt := range []string{"bigquery", "databricks"} {
		got, err := PrepareSourceSQL(in, wt)
		if err != nil {
			t.Fatalf("%s: %v", wt, err)
		}
		if want := `SELECT "say \"hi\";" AS s FROM t`; got != want {
			t.Errorf("%s: got %q, want %q", wt, got, want)
		}
	}
}

func TestPrepareSourceSQL_AllDocumentedMacrosRejected(t *testing.T) {
	for _, m := range []string{"{statsig_start_date}", "{statsig_end_date}", "{statsig_start_date_int}", "{statsig_end_date_int}", "{statsig_experiment_start_timestamp}", "{ statsig_start_date }"} {
		if _, err := PrepareSourceSQL("SELECT * FROM t WHERE ds >= "+m, "snowflake"); err == nil || !strings.Contains(err.Error(), m) {
			t.Errorf("%s not rejected by name: %v", m, err)
		}
	}
}

func TestPrepareSourceSQL_MultiStatement(t *testing.T) {
	for _, in := range []string{
		"SET d = '2026-01-01'; SELECT * FROM t WHERE ds >= $d",
		"SELECT 1 FROM t; /* x */ SELECT 2 FROM u",
		"USE WAREHOUSE wh; SELECT 1",
	} {
		if _, err := PrepareSourceSQL(in, "snowflake"); err == nil {
			t.Errorf("multi-statement accepted: %q", in)
		}
	}
}

func TestWrapWithConstantEventKey_Shapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"table source",
			"SELECT * FROM ANALYTICS.PROD.PAGE_VIEWS",
			"SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nSELECT * FROM ANALYTICS.PROD.PAGE_VIEWS\n) AS ld_src",
		},
		{
			"simple SQL with semicolon",
			"SELECT user_id, ts, order_id FROM events WHERE kind = 'checkout';",
			"SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nSELECT user_id, ts, order_id FROM events WHERE kind = 'checkout'\n) AS ld_src",
		},
		{
			"CTE",
			"WITH s AS (SELECT user_id, ts FROM events)\nSELECT * FROM s ORDER BY ts LIMIT 100",
			"SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nWITH s AS (SELECT user_id, ts FROM events)\nSELECT * FROM s ORDER BY ts LIMIT 100\n) AS ld_src",
		},
		{
			"trailing line comment",
			"SELECT user_id, ts FROM events -- last 90 days",
			"SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nSELECT user_id, ts FROM events -- last 90 days\n) AS ld_src",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WrapWithConstantEventKey(tt.in, "checkout-events", "LD_EVENT_KEY", "snowflake")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
			if key, ok := ParseConstantEventKey(got, "ld_event_key", "snowflake"); !ok || key != "checkout-events" {
				t.Errorf("ParseConstantEventKey = %q, %v; want checkout-events, true", key, ok)
			}
		})
	}
}

// LaunchDarkly nests the query again to preview ("SELECT * FROM (%s) LIMIT %d") and
// to compute metrics ("... FROM (%s) AS data_source").
func TestWrapWithConstantEventKey_SurvivesOuterWraps(t *testing.T) {
	inner := "SELECT user_id, ts FROM events -- trailing comment ) ;"
	w, err := WrapWithConstantEventKey(inner, "k", "LD_EVENT_KEY", "snowflake")
	if err != nil {
		t.Fatal(err)
	}
	for _, outer := range []string{
		"SELECT * FROM (" + w + ") LIMIT 0",
		"SELECT x FROM (" + w + ") AS data_source WHERE y = 1",
	} {
		scan, err := scanSQL(outer, "snowflake")
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		depth := 0
		for _, p := range scan.significant {
			switch outer[p] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			t.Errorf("unbalanced parens (%d) in %q", depth, outer)
		}
	}
}

func TestConstantEventKeyColumn_PerWarehouse(t *testing.T) {
	for wt, want := range map[string]string{
		"snowflake": "LD_EVENT_KEY", "bigquery": "ld_event_key",
		"databricks": "ld_event_key", "redshift": "ld_event_key",
	} {
		if got := ConstantEventKeyColumn(wt); got != want {
			t.Errorf("%s: got %q, want %q", wt, got, want)
		}
	}
}

func TestChooseEventKeyColumn_AvoidsCollisions(t *testing.T) {
	if got := ChooseEventKeyColumn("LD_EVENT_KEY", []string{"USER_ID", "TS"}, ""); got != "LD_EVENT_KEY" {
		t.Errorf("no collision: got %q", got)
	}
	if got := ChooseEventKeyColumn("LD_EVENT_KEY", []string{"ld_event_key", "TS"}, ""); got != "LD_EVENT_KEY_2" {
		t.Errorf("case-insensitive collision: got %q, want LD_EVENT_KEY_2", got)
	}
	if got := ChooseEventKeyColumn("LD_EVENT_KEY", []string{"ld_event_key", "LD_EVENT_KEY_2"}, ""); got != "LD_EVENT_KEY_3" {
		t.Errorf("collision: got %q, want LD_EVENT_KEY_3", got)
	}
	if got := ChooseEventKeyColumn("LD_EVENT_KEY", nil, "SELECT ld_event_key, ts FROM t"); got != "LD_EVENT_KEY_2" {
		t.Errorf("text fallback: got %q, want LD_EVENT_KEY_2", got)
	}
}

func TestApplyConstantEventKey_RewritesBody(t *testing.T) {
	src := map[string]any{
		"name":            "Checkout Events",
		"sourceType":      "table",
		"tableName":       "DB.SCHEMA.ORDERS",
		"timestampColumn": "ts",
		"idTypeMapping":   []any{map[string]any{"statsigUnitID": "userID", "column": "user_id"}},
	}
	body := MapMetricSourceToDataSource(src, "production", "snowflake-experimentation", "")
	col, err := ApplyConstantEventKey(body, "snowflake", []string{"TS", "USER_ID"})
	if err != nil {
		t.Fatal(err)
	}
	wantSQL := "SELECT *, 'checkout-events' AS LD_EVENT_KEY FROM (\nSELECT * FROM DB.SCHEMA.ORDERS\n) AS ld_src"
	if body["sqlQuery"] != wantSQL {
		t.Errorf("sqlQuery =\n%v\nwant\n%s", body["sqlQuery"], wantSQL)
	}
	cm := body["columnMappings"].(map[string]any)
	if cm["keyColumn"] != col || col != "LD_EVENT_KEY" {
		t.Errorf("keyColumn = %v (col %q), want LD_EVENT_KEY", cm["keyColumn"], col)
	}

	preview := map[string]any{"keyColumn": "EVENT_KEY"}
	real := []map[string]any{{"name": "TS", "type": "TIMESTAMP_NTZ"}, {"name": "USER_ID", "type": "TEXT"}, {"name": "EVENT_KEY", "type": "TEXT"}, {"name": "LD_EVENT_KEY", "type": "TEXT"}}
	ReconcileColumnMappings(cm, preview, real)
	PinConstantKeyColumn(cm, col, real)
	if cm["keyColumn"] != "LD_EVENT_KEY" {
		t.Errorf("after reconcile keyColumn = %v, want LD_EVENT_KEY", cm["keyColumn"])
	}
}

func TestApplyConstantEventKey_NoSQLOrTableIsAnError(t *testing.T) {
	body := MapMetricSourceToDataSource(map[string]any{"name": "Checkout Events", "timestampColumn": "ts"}, "production", "snowflake-experimentation", "")
	_, err := ApplyConstantEventKey(body, "snowflake", nil)
	if err == nil || !strings.Contains(err.Error(), "neither SQL nor a table name") {
		t.Fatalf("err = %v, want a no-query error; sqlQuery=%q", err, body["sqlQuery"])
	}
}

func TestParseConstantEventKey_ToleratesEditedSQL(t *testing.T) {
	for name, sql := range map[string]string{
		"as written":         "SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT 1\n) AS ld_src",
		"CRLF":               "SELECT *, 'k' AS LD_EVENT_KEY FROM (\r\nSELECT 1\r\n) AS ld_src",
		"re-indented":        "  SELECT  *,\n    'k' AS LD_EVENT_KEY\n  FROM (\n    SELECT 1\n  ) AS ld_src",
		"lower-case":         "select *, 'k' as ld_event_key from (select 1) as ld_src",
		"quoted alias":       "SELECT *, 'k' AS \"LD_EVENT_KEY\" FROM (\nSELECT 1\n) AS ld_src",
		"no space before (":  "SELECT *,'k' AS LD_EVENT_KEY FROM(SELECT 1) AS ld_src",
		"trailing semicolon": "SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT 1\n) AS ld_src;\n",
		"leading comment":    "-- migrated from Statsig\n/* do not edit */ SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT 1\n) AS ld_src",
		"trailing comment":   "SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT 1\n) AS \"ld_src\" ; -- done",
		"parens in strings":  "SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT ')' AS a, \"b)\" FROM t -- )\n) AS LD_SRC",
	} {
		if key, ok := ParseConstantEventKey(sql, "LD_EVENT_KEY", "snowflake"); !ok || key != "k" {
			t.Errorf("%s: got %q, %v; want k, true", name, key, ok)
		}
	}
	if key, ok := ParseConstantEventKey("SELECT *, 'it''s' AS LD_EVENT_KEY FROM (SELECT 1) AS ld_src", "LD_EVENT_KEY", "snowflake"); !ok || key != "it's" {
		t.Errorf("escaped quote: got %q, %v", key, ok)
	}
}

func TestParseConstantEventKey_RejectsOtherSQL(t *testing.T) {
	if _, ok := ParseConstantEventKey("SELECT *, 'add_to_cart' AS event_name, 1 AS event_value FROM t", "EVENT_NAME", "snowflake"); ok {
		t.Error("a hand-written constant column is not the CLI wrapper")
	}
	wrapped := "SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT 1\n) AS ld_src"
	if _, ok := ParseConstantEventKey(wrapped, "EVENT_KEY", "snowflake"); ok {
		t.Error("key column pointing elsewhere must not count as constant")
	}
	if _, ok := ParseConstantEventKey("SELECT * FROM t", "LD_EVENT_KEY", "snowflake"); ok {
		t.Error("plain SQL must not count as constant")
	}
	for name, sql := range map[string]string{
		"union of tagged subqueries": "SELECT *, 'signup' AS EVENT_NAME FROM (SELECT user_id, ts FROM signups) AS a\nUNION ALL\nSELECT *, 'purchase' AS EVENT_NAME FROM (SELECT user_id, ts FROM purchases) AS b",
		"union after ld_src":         "SELECT *, 'k' AS EVENT_NAME FROM (SELECT 1) AS ld_src UNION ALL SELECT *, 'j' AS EVENT_NAME FROM (SELECT 2) AS ld_src",
		"where after ld_src":         "SELECT *, 'k' AS EVENT_NAME FROM (SELECT 1) AS ld_src WHERE 1 = 1",
		"other alias":                "SELECT *, 'k' AS EVENT_NAME FROM (SELECT 1) AS src",
		"unclosed":                   "SELECT *, 'k' AS EVENT_NAME FROM (SELECT 1",
		"mismatched alias quotes":    "SELECT *, 'k' AS EVENT_NAME FROM (SELECT 1) AS \"ld_src",
	} {
		if key, ok := ParseConstantEventKey(sql, "EVENT_NAME", "snowflake"); ok {
			t.Errorf("%s: read as constant %q", name, key)
		}
	}
}

func TestClassifyConstantKey(t *testing.T) {
	tests := []struct {
		name, sql, keyColumn string
		want                 ConstantKeyState
	}{
		{"unmodified", "SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT 1\n) AS ld_src", "LD_EVENT_KEY", ConstantKeyWrapped},
		{"appended WHERE", "SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT 1\n) AS ld_src WHERE ts > '2024-01-01'", "LD_EVENT_KEY", ConstantKeyEdited},
		{"appended LIMIT", "SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT 1\n) AS ld_src\nLIMIT 1000", "LD_EVENT_KEY", ConstantKeyEdited},
		{"missing derived-table AS", "SELECT *, 'k' AS LD_EVENT_KEY FROM (\nSELECT 1\n) ld_src", "LD_EVENT_KEY", ConstantKeyEdited},
		{"missing column AS", "SELECT *, 'k' LD_EVENT_KEY FROM (\nSELECT 1\n) AS ld_src", "ld_event_key", ConstantKeyEdited},
		{"leading comment", "-- edited\nselect *, 'k' as ld_event_key from (select 1) as src", "LD_EVENT_KEY", ConstantKeyEdited},
		{"literal is another key", "SELECT *, 'j' AS LD_EVENT_KEY FROM (SELECT 1) AS ld_src WHERE 1 = 1", "LD_EVENT_KEY", NoConstantKey},
		{"key column elsewhere", "SELECT *, 'k' AS LD_EVENT_KEY FROM (SELECT 1) AS ld_src WHERE 1 = 1", "EVENT_NAME", NoConstantKey},
		{"no key column", "SELECT *, 'k' AS LD_EVENT_KEY FROM (SELECT 1) AS ld_src WHERE 1 = 1", "", NoConstantKey},
		{"head inside a comment", "/* SELECT *, 'k' AS LD_EVENT_KEY FROM ( */ SELECT * FROM t", "LD_EVENT_KEY", NoConstantKey},
		{"plain SQL", "SELECT * FROM t", "LD_EVENT_KEY", NoConstantKey},
		{"union of tagged subqueries",
			"SELECT *, 'signup' AS EVENT_NAME FROM (SELECT user_id, ts FROM signups) AS a\nUNION ALL\nSELECT *, 'purchase' AS EVENT_NAME FROM (SELECT user_id, ts FROM purchases) AS b",
			"EVENT_NAME", NoConstantKey},
	}
	for _, tt := range tests {
		w, got := ClassifyConstantKey(tt.sql, tt.keyColumn, "k", "snowflake")
		if got != tt.want {
			t.Errorf("%s: state = %v, want %v", tt.name, got, tt.want)
		}
		if got != NoConstantKey && w.EventKey != "k" {
			t.Errorf("%s: event key = %q, want k", tt.name, w.EventKey)
		}
	}
	// The union is rejected for any data source key except the first branch's literal.
	union := tests[len(tests)-1].sql
	for _, key := range []string{"events", "purchase", "signups"} {
		if _, got := ClassifyConstantKey(union, "EVENT_NAME", key, "snowflake"); got != NoConstantKey {
			t.Errorf("union on data source %q read as %v", key, got)
		}
	}
	// The strict wrapper keeps its own literal even when it is not the key.
	if w, got := ClassifyConstantKey("SELECT *, 'j' AS LD_EVENT_KEY FROM (SELECT 1) AS ld_src", "LD_EVENT_KEY", "k", "snowflake"); got != ConstantKeyWrapped || w.EventKey != "j" {
		t.Errorf("strict wrapper with another literal: %+v, %v", w, got)
	}
}

func TestWrapWithConstantEventKey_CastsOnRedshift(t *testing.T) {
	got, err := WrapWithConstantEventKey("SELECT * FROM public.orders", "checkout-events", "ld_event_key", "redshift")
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT *, CAST('checkout-events' AS VARCHAR(256)) AS ld_event_key FROM (\nSELECT * FROM public.orders\n) AS ld_src"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	for _, wh := range []string{"snowflake", "bigquery", "databricks"} {
		out, _ := WrapWithConstantEventKey("SELECT 1", "k", "c", wh)
		if strings.Contains(out, "CAST(") {
			t.Errorf("%s: unexpected cast in %q", wh, out)
		}
	}

	w, ok := ParseConstantKeyWrapper(got, "redshift")
	if !ok || w.EventKey != "checkout-events" || w.Column != "ld_event_key" || w.Inner != "SELECT * FROM public.orders" {
		t.Errorf("parse: %+v, %v", w, ok)
	}
	if k, ok := ParseConstantEventKey(got, "ld_event_key", "redshift"); !ok || k != "checkout-events" {
		t.Errorf("event key: %q, %v", k, ok)
	}
	edited := got + "\nWHERE created_at > '2024-01-01'"
	if w, state := ClassifyConstantKey(edited, "ld_event_key", "checkout-events", "redshift"); state != ConstantKeyEdited || w.EventKey != "checkout-events" {
		t.Errorf("edited: %+v, %v", w, state)
	}
	if _, state := ClassifyConstantKey("SELECT *, CAST('k' AS VARCHAR) ld_event_key FROM (SELECT 1) ld_src", "ld_event_key", "k", "redshift"); state != ConstantKeyEdited {
		t.Errorf("cast without length or AS: %v", state)
	}
}

func TestParseConstantKeyWrapper_InnerAndWarehouseComments(t *testing.T) {
	w, ok := ParseConstantKeyWrapper("SELECT *, 'orders' AS LD_EVENT_KEY FROM (\n  SELECT * FROM t WHERE (a) = 1 -- (\n) AS ld_src", "snowflake")
	if !ok || w.EventKey != "orders" || w.Column != "LD_EVENT_KEY" || w.Inner != "SELECT * FROM t WHERE (a) = 1 -- (" {
		t.Errorf("got %+v, %v", w, ok)
	}
	// "#" starts a comment only on BigQuery.
	sql := "# note\nSELECT *, 'k' AS ld_event_key FROM (SELECT 1) AS ld_src"
	if _, ok := ParseConstantKeyWrapper(sql, "bigquery"); !ok {
		t.Error("bigquery: leading # comment not skipped")
	}
	if _, ok := ParseConstantKeyWrapper(sql, "snowflake"); ok {
		t.Error("snowflake: # is not a comment")
	}
}

func TestReconcileColumnMappings_StatsigTimestampWins(t *testing.T) {
	cm := map[string]any{"timestampColumn": "event_ts", "contexts": map[string]string{"user": "user_id"}}
	preview := map[string]any{"timestampColumn": "CREATED_AT"}
	real := []map[string]any{{"name": "CREATED_AT", "type": "TIMESTAMP_NTZ"}, {"name": "EVENT_TS", "type": "TIMESTAMP_NTZ"}, {"name": "USER_ID", "type": "TEXT"}, {"name": "LD_EVENT_KEY", "type": "TEXT"}}
	ReconcileColumnMappings(cm, preview, real)
	if cm["timestampColumn"] != "EVENT_TS" {
		t.Errorf("timestampColumn = %v, want EVENT_TS (Statsig's configured column, in the warehouse's case)", cm["timestampColumn"])
	}
}

func TestReconcileColumnMappings_PreviewTimestampWhenStatsigColumnMissing(t *testing.T) {
	cm := map[string]any{"timestampColumn": "timestamp", "contexts": map[string]string{"user": "user_id"}}
	preview := map[string]any{"timestampColumn": "CREATED_AT"}
	real := []map[string]any{{"name": "CREATED_AT", "type": "TIMESTAMP_NTZ"}, {"name": "USER_ID", "type": "TEXT"}}
	ReconcileColumnMappings(cm, preview, real)
	if cm["timestampColumn"] != "CREATED_AT" {
		t.Errorf("timestampColumn = %v, want the preview's CREATED_AT", cm["timestampColumn"])
	}
}

func TestReconcileColumnMappings_StatsigValueColumnWins(t *testing.T) {
	real := []map[string]any{{"name": "QUANTITY", "type": "NUMBER"}, {"name": "ORDER_TOTAL", "type": "NUMBER"}, {"name": "TS", "type": "TIMESTAMP_NTZ"}}
	cm := map[string]any{"timestampColumn": "ts", "valueColumn": "order_total"}
	ReconcileColumnMappings(cm, map[string]any{"valueColumn": "QUANTITY"}, real)
	if cm["valueColumn"] != "ORDER_TOTAL" {
		t.Errorf("valueColumn = %v, want ORDER_TOTAL", cm["valueColumn"])
	}
	cm = map[string]any{"timestampColumn": "ts", "valueColumn": "amount"}
	ReconcileColumnMappings(cm, map[string]any{"valueColumn": "QUANTITY"}, real)
	if cm["valueColumn"] != "QUANTITY" {
		t.Errorf("valueColumn = %v, want the preview's QUANTITY when Statsig's column is missing", cm["valueColumn"])
	}
}
