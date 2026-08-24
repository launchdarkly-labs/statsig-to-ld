package warehouse

import "testing"

func srcWithSQL(sql string) []map[string]any {
	return []map[string]any{{"sql": sql}}
}

// The SQL keyword lists were loose enough to be actively wrong. A customer run
// reported "Databricks" because a column name contained "delta". These cases
// pin the substrings that must no longer decide a warehouse type.
func TestDetectWarehouseTypeFromSQL_IgnoresAmbiguousTokens(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"delta inside an identifier", "SELECT revenue_delta, delta_days FROM t"},
		{"double colon is a cast in several dialects", "SELECT value::int FROM t"},
		{"backticks are ordinary quoting", "SELECT `value` FROM `t`"},
		{"catalog-qualified name is not Databricks-specific", "SELECT a FROM catalog.schema.t"},
	}
	for _, tc := range cases {
		if got := DetectWarehouseType(nil, srcWithSQL(tc.sql)); got != "" {
			t.Errorf("%s: got %q, want no guess", tc.name, got)
		}
	}
}

// Unambiguous markers should still resolve.
func TestDetectWarehouseTypeFromSQL_KeepsUnambiguousMarkers(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT * FROM snowflake_sample_data.x", "snowflake"},
		{"SELECT FLATTEN(input => col) FROM t", "snowflake"},
		{"SELECT UNNEST(arr) FROM t", "bigquery"},
		{"SELECT a FROM t -- databricks workspace", "databricks"},
		{"SELECT GETDATE()", "redshift"},
	}
	for _, tc := range cases {
		if got := DetectWarehouseType(nil, srcWithSQL(tc.sql)); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.sql, got, tc.want)
		}
	}
}

// The warehouse connection config is authoritative and must beat any SQL guess.
func TestResolveWarehouseType_ConnectionBeatsGuess(t *testing.T) {
	conn := map[string]any{"accountName": "acct"}
	typ, src, err := ResolveWarehouseType("", conn, srcWithSQL("SELECT GETDATE()"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if typ != "snowflake" {
		t.Errorf("type = %q, want snowflake (from the connection config)", typ)
	}
	if src != WarehouseTypeFromConnection {
		t.Errorf("source = %v, want WarehouseTypeFromConnection", src)
	}
	if !src.IsConfident() {
		t.Error("a type read from the connection config must count as confident")
	}
}

// An explicit flag beats everything, including a conflicting connection config.
func TestResolveWarehouseType_FlagWins(t *testing.T) {
	conn := map[string]any{"host": "h", "path": "p"}
	typ, src, err := ResolveWarehouseType("snowflake", conn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if typ != "snowflake" || src != WarehouseTypeFromFlag {
		t.Errorf("got (%q, %v), want (snowflake, WarehouseTypeFromFlag)", typ, src)
	}
	if !src.IsConfident() {
		t.Error("an explicitly passed type must count as confident")
	}
}

func TestResolveWarehouseType_RejectsUnknownFlag(t *testing.T) {
	if _, _, err := ResolveWarehouseType("postgres", nil, nil); err == nil {
		t.Error("expected an error for an unsupported warehouse type")
	}
}

// A SQL-derived answer is a guess and must be labelled as one, so callers can
// refuse to create resources on it.
func TestResolveWarehouseType_SQLGuessIsNotConfident(t *testing.T) {
	typ, src, err := ResolveWarehouseType("", nil, srcWithSQL("SELECT GETDATE()"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if typ != "redshift" {
		t.Errorf("type = %q, want redshift", typ)
	}
	if src != WarehouseTypeGuessedFromSQL {
		t.Errorf("source = %v, want WarehouseTypeGuessedFromSQL", src)
	}
	if src.IsConfident() {
		t.Error("a SQL guess must NOT count as confident")
	}
}

func TestResolveWarehouseType_NothingToGoOn(t *testing.T) {
	typ, src, err := ResolveWarehouseType("", nil, srcWithSQL("SELECT a FROM t"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if typ != "" || src != WarehouseTypeUnknown {
		t.Errorf("got (%q, %v), want (\"\", WarehouseTypeUnknown)", typ, src)
	}
	if src.IsConfident() {
		t.Error("an unknown type must not count as confident")
	}
}
