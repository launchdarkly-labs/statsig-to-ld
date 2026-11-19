package warehouse

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

const (
	planTier    = "plan_tier > 2"
	planTierCol = "plan_tier_gt_2"
	// What #63 named the column.
	planTierHashCol = "sql_filter_6e4004268a3c8fc1"
	commentFilter   = "-- paid plans only\nplan_tier > 2 OR is_partner = TRUE"
)

var centsField = []CustomField{{Key: "cents", Formula: "price_usd*100"}}

func TestSQLFilterColumns_NamesEachFilter(t *testing.T) {
	got := SQLFilterColumns([]string{" plan_tier > 2 "})
	want := []CustomField{{Key: planTierCol, Formula: planTier}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SQLFilterColumns = %+v, want %+v", got, want)
	}
}

func TestSQLFilterColumns_NumbersNamesThatClash(t *testing.T) {
	got := SQLFilterColumns([]string{"plan_tier > 2", "plan_tier>2", "cents > 500"}, "CENTS_GT_500")
	want := []CustomField{
		{Key: "plan_tier_gt_2", Formula: "plan_tier > 2"},
		{Key: "plan_tier_gt_2_2", Formula: "plan_tier>2"},
		{Key: "cents_gt_500_2", Formula: "cents > 500"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SQLFilterColumns = %+v\nwant %+v", got, want)
	}
}

func TestMergeSQLFilters_MatchesBySQLAndNumbersNewNames(t *testing.T) {
	have := []CustomField{{Key: planTierHashCol, Formula: planTier}, {Key: "region_eq_eu", Formula: "region = 'EU'"}}
	want := SQLFilterColumns([]string{planTier, "region='EU'", "is_test = FALSE"}, "cents")
	got := MergeSQLFilters(have, want, "cents")
	wantMerged := append(slices.Clone(have),
		CustomField{Key: "region_eq_eu_2", Formula: "region='EU'"},
		CustomField{Key: "is_test_eq_false", Formula: "is_test = FALSE"})
	if !reflect.DeepEqual(got, wantMerged) {
		t.Errorf("MergeSQLFilters = %+v\nwant %+v", got, wantMerged)
	}
}

func TestBuildSourceQuery_AddsSQLFiltersOutsideCustomFields(t *testing.T) {
	commentCol := SQLFilterColumns([]string{commentFilter})[0].Key
	for _, tc := range []struct {
		name string
		c    SourceColumns
		want string
	}{
		{"neither", SourceColumns{}, "SELECT * FROM analytics.orders"},
		{
			"custom fields only",
			SourceColumns{Fields: centsField},
			"SELECT *, (price_usd*100) AS cents FROM (\nSELECT * FROM analytics.orders\n) AS statsig_src",
		},
		{
			"filters only",
			SourceColumns{Filters: SQLFilterColumns([]string{planTier})},
			"SELECT *, (plan_tier > 2) AS " + planTierCol + " FROM (\nSELECT * FROM analytics.orders\n) AS statsig_filters",
		},
		{
			// A filter can use a custom field: it reads the layer that adds them.
			"both",
			SourceColumns{Fields: centsField, Filters: SQLFilterColumns([]string{planTier, "cents > 500"})},
			"SELECT *, (plan_tier > 2) AS " + planTierCol + ", (cents > 500) AS " + SQLFilterColumns([]string{"cents > 500"})[0].Key +
				" FROM (\nSELECT *, (price_usd*100) AS cents FROM (\nSELECT * FROM analytics.orders\n) AS statsig_src\n) AS statsig_filters",
		},
		{
			"a line comment cannot swallow the alias",
			SourceColumns{Filters: SQLFilterColumns([]string{commentFilter})},
			"SELECT *, (\n" + commentFilter + "\n) AS " + commentCol + " FROM (\nSELECT * FROM analytics.orders\n) AS statsig_filters",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildSourceQuery("SELECT * FROM analytics.orders;", tc.c, "snowflake")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestBuildSourceQuery_KeepsTheConstantKeyWrapperReadable(t *testing.T) {
	inner, err := BuildSourceQuery("SELECT * FROM t", SourceColumns{Fields: centsField, Filters: SQLFilterColumns([]string{commentFilter, "x IN ('a)', 'b')"})}, "snowflake")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := WrapWithConstantEventKey(inner, "checkout", "LD_EVENT_KEY", "snowflake")
	if err != nil {
		t.Fatal(err)
	}
	w, ok := ParseConstantKeyWrapper(wrapped, "snowflake")
	if !ok || w.Inner != inner {
		t.Errorf("ParseConstantKeyWrapper = %+v, %v; want inner\n%s", w, ok, inner)
	}
}

func TestValidateSQLFilter_RefusesWhatItCannotNest(t *testing.T) {
	for _, tc := range []struct{ sql, want string }{
		{"ts > {statsig_start_date}", "Statsig macros"},
		{"a = 1; DROP TABLE t", "single expression"},
		{"name = 'unterminated", "could not read"},
		// Closing the parentheses it sits in would let it add columns or rows.
		{"TRUE) AS sql_filter_deadbeef, (FALSE", "could not read"},
		{"(plan_tier > 2", "could not read"},
	} {
		f := SQLFilterColumns([]string{tc.sql})[0]
		err := ValidateSQLFilter(f, "snowflake")
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Statsig SQL filter") {
			t.Errorf("ValidateSQLFilter(%q) = %v, want it to mention %q", tc.sql, err, tc.want)
		}
		if _, err := BuildSourceQuery("SELECT 1", SourceColumns{Filters: []CustomField{f}}, "snowflake"); err == nil {
			t.Errorf("BuildSourceQuery accepted %q", tc.sql)
		}
	}
	if err := ValidateSQLFilter(SQLFilterColumns([]string{commentFilter})[0], "snowflake"); err != nil {
		t.Errorf("a commented filter is fine: %v", err)
	}
}

func TestMissingColumns(t *testing.T) {
	const sql = "SELECT * FROM analytics.orders"
	filters := SQLFilterColumns([]string{planTier, "region = 'EU'"})
	build := func(c SourceColumns) string {
		t.Helper()
		q, err := BuildSourceQuery(sql, c, "snowflake")
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	want := SourceColumns{Fields: centsField, Filters: filters}
	stale := SQLFilterColumns([]string{"is_test = FALSE"})

	for _, tc := range []struct {
		name        string
		query       string
		wantMissing SourceColumns
		wantMerged  SourceColumns
	}{
		{"created before custom fields", sql, want, want},
		{"created by an earlier version, whose custom fields are bare", "SELECT *, price_usd*100 AS cents FROM (\n" + sql + "\n) AS statsig_src", SourceColumns{Filters: filters}, want},
		{"created with custom fields only", build(SourceColumns{Fields: centsField}), SourceColumns{Filters: filters}, want},
		{"has some filters", build(SourceColumns{Fields: centsField, Filters: filters[:1]}), SourceColumns{Filters: filters[1:]}, want},
		{"has everything", build(want), SourceColumns{}, want},
		{
			"created by #63, whose columns are named by hash",
			strings.Replace(build(want), "AS "+planTierCol, "AS "+planTierHashCol, 1),
			SourceColumns{},
			SourceColumns{Fields: centsField, Filters: append([]CustomField{{Key: planTierHashCol, Formula: planTier}}, filters[1:]...)},
		},
		{
			"a name numbered to avoid a clash",
			strings.Replace(build(want), "AS "+planTierCol, "AS "+planTierCol+"_3", 1),
			SourceColumns{},
			SourceColumns{Fields: centsField, Filters: append([]CustomField{{Key: planTierCol + "_3", Formula: planTier}}, filters[1:]...)},
		},
		// A bound metric may still filter on a column Statsig no longer has a filter for.
		{
			"keeps a filter Statsig dropped",
			build(SourceColumns{Fields: centsField, Filters: stale}),
			SourceColumns{Filters: filters},
			SourceColumns{Fields: centsField, Filters: append(append([]CustomField{}, stale...), filters...)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			missing, merged, ok := MissingColumns(tc.query, sql, want, "snowflake")
			if !ok {
				t.Fatal("not recognized as this command's query")
			}
			if !reflect.DeepEqual(missing, tc.wantMissing) {
				t.Errorf("missing = %+v, want %+v", missing, tc.wantMissing)
			}
			if !reflect.DeepEqual(merged, tc.wantMerged) {
				t.Errorf("merged = %+v, want %+v", merged, tc.wantMerged)
			}
		})
	}

	edited := strings.Replace(build(want), "(plan_tier > 2)", "(plan_tier > 3)", 1)
	for name, query := range map[string]string{
		"filter edited in LaunchDarkly": edited,
		"filter column renamed":         strings.Replace(build(want), "AS "+planTierCol, "AS tier_filter", 1),
		"Statsig SQL changed":           strings.Replace(build(want), "analytics.orders", "analytics.orders_v2", 1),
		"custom field changed":          build(SourceColumns{Fields: []CustomField{{Key: "cents", Formula: "price_usd*1000"}}}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := MissingColumns(query, sql, want, "snowflake"); ok {
				t.Error("recognized a query this command did not write from this SQL")
			}
		})
	}
}

func TestMissingColumns_ReadsBackTrickyQueries(t *testing.T) {
	const sql = "SELECT * FROM analytics.orders"
	// The alias it quotes is the one its own first characters hash to.
	decoy := "x = 'a) AS " + statsig.SQLFilterColumn("x = 'a") + "'"
	c := SourceColumns{Fields: centsField, Filters: SQLFilterColumns([]string{decoy, planTier})}
	q, err := BuildSourceQuery(sql, c, "snowflake")
	if err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{
		"a filter that quotes a column alias": q,
		"CRLF line endings":                   strings.ReplaceAll(q, "\n", "\r\n"),
	} {
		t.Run(name, func(t *testing.T) {
			missing, merged, ok := MissingColumns(query, sql, c, "snowflake")
			if !ok || !missing.Empty() || !reflect.DeepEqual(merged, c) {
				t.Errorf("MissingColumns = %+v, %+v, %v; want nothing missing", missing, merged, ok)
			}
		})
	}
}
