package converter

import (
	"reflect"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
)

func prop(attr, op string, negate bool, values ...any) launchdarkly.EventFilter {
	return launchdarkly.EventFilter{Type: launchdarkly.EventFilterTypeEventProperty, Attribute: attr, Op: op, Negate: negate, Values: values}
}

func TestTranslateSQLFilter_WritesSimpleFiltersAsClauses(t *testing.T) {
	notTest := prop("is_test", ldOpIn, false, false)
	eu := prop("region", ldOpIn, false, "EU")
	notEU := prop("region", ldOpIn, true, "EU")
	for _, tc := range []struct {
		sql  string
		want []launchdarkly.EventFilter
	}{
		{"is_test", []launchdarkly.EventFilter{prop("is_test", ldOpIn, false, true)}},
		{"NOT is_test AND NOT is_refund", []launchdarkly.EventFilter{notTest, prop("is_refund", ldOpIn, false, false)}},
		{"not is_test and\n\tnot is_refund ", []launchdarkly.EventFilter{notTest, prop("is_refund", ldOpIn, false, false)}},
		{"NOT NOT is_test", []launchdarkly.EventFilter{prop("is_test", ldOpIn, false, true)}},
		{"is_test IS FALSE", []launchdarkly.EventFilter{notTest}},
		{"is_test IS TRUE", []launchdarkly.EventFilter{prop("is_test", ldOpIn, false, true)}},
		{"is_test = FALSE", []launchdarkly.EventFilter{notTest}},
		{"is_test <> TRUE", []launchdarkly.EventFilter{notTest}},
		{"NOT (is_test = TRUE)", []launchdarkly.EventFilter{notTest}},
		{"region = 'EU'", []launchdarkly.EventFilter{eu}},
		{"region <> 'EU'", []launchdarkly.EventFilter{notEU}},
		{"region != 'EU'", []launchdarkly.EventFilter{notEU}},
		{"NOT region = 'EU'", []launchdarkly.EventFilter{notEU}},
		{"name = 'O''Brien'", []launchdarkly.EventFilter{prop("name", ldOpIn, false, "O'Brien")}},
		{"region IN ('EU', 'UK')", []launchdarkly.EventFilter{prop("region", ldOpIn, false, "EU", "UK")}},
		{"region NOT IN ('EU')", []launchdarkly.EventFilter{notEU}},
		{"NOT (region IN ('EU'))", []launchdarkly.EventFilter{notEU}},
		{"plan LIKE 'pro%'", []launchdarkly.EventFilter{prop("plan", ldOpStartsWith, false, "pro")}},
		{"plan LIKE '%pro'", []launchdarkly.EventFilter{prop("plan", ldOpEndsWith, false, "pro")}},
		{"plan LIKE '%pro%'", []launchdarkly.EventFilter{prop("plan", ldOpContains, false, "pro")}},
		{"plan NOT LIKE '%pro%'", []launchdarkly.EventFilter{prop("plan", ldOpContains, true, "pro")}},
		{"plan LIKE 'pro'", []launchdarkly.EventFilter{prop("plan", ldOpIn, false, "pro")}},
		{"amount > 10", []launchdarkly.EventFilter{prop("amount", ldOpGreaterThan, false, 10.0)}},
		{"amount >= 10.5", []launchdarkly.EventFilter{prop("amount", ldOpGreaterThanOrEqual, false, 10.5)}},
		{"amount < -1", []launchdarkly.EventFilter{prop("amount", ldOpLessThan, false, -1.0)}},
		{"amount <= 0", []launchdarkly.EventFilter{prop("amount", ldOpLessThanOrEqual, false, 0.0)}},
		{"NOT amount > 10", []launchdarkly.EventFilter{prop("amount", ldOpLessThanOrEqual, false, 10.0)}},
		{"NOT amount < 10", []launchdarkly.EventFilter{prop("amount", ldOpGreaterThanOrEqual, false, 10.0)}},
		{"(NOT is_test) AND (amount > 0 AND region = 'EU')", []launchdarkly.EventFilter{
			notTest, prop("amount", ldOpGreaterThan, false, 0.0), eu}},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got, ok := translateSQLFilter(tc.sql)
			if !ok {
				t.Fatalf("translateSQLFilter(%q) did not translate", tc.sql)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

// Each of these means something LaunchDarkly's clauses cannot say exactly, or
// cannot show in its filter editor, so the filter keeps its column.
func TestTranslateSQLFilter_LeavesTheRestToItsColumn(t *testing.T) {
	for _, sql := range []string{
		"",
		"is_test OR is_refund",
		"amount = 10",              // a decimal column's text form is 10.00, so "in" would miss it
		"region IN ('EU', 10)",     // same
		"region = ''",              // whether empty means null is the warehouse's call
		"region IS NULL",           // the filter editor cannot show "exists"
		"region IS NOT NULL",       // same
		"is_test IS NOT TRUE",      // also matches null
		"NOT (is_test IS TRUE)",    // same
		"NOT (is_test AND is_new)", // no single clause
		"amount > '10'",
		"LOWER(region) = 'eu'",
		"e.region = 'EU'",
		`"Region" = 'EU'`,
		"region = 'EU' -- note",
		"region = 'E\\U'",
		"region = $$EU$$",
		"region LIKE 'E_'",
		"region LIKE '%E%U%'",
		"region LIKE '%'",
		"region ILIKE 'eu'",
		"amount BETWEEN 1 AND 2",
		"amount > 1e3",
		"amount > 1.123456789012345",
		"amount > 9007199254740993",
		"ts > {statsig_start_date}",
		"region = 'EU';",
		"TRUE",
		"NOT NULL",
		"region = 'EU' AND",
		"(region = 'EU'",
		"region = 'EU')",
		"amount > -",
		"region = 'EU' AND region IS NULL",
	} {
		if got, ok := translateSQLFilter(sql); ok {
			t.Errorf("translateSQLFilter(%q) = %#v, want no translation", sql, got)
		}
	}
}
