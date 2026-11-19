package converter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

var planTierColumn = statsig.SQLFilterColumn("COALESCE(plan_tier, 0) > 2")

const sqlFilterSum = `{
  "type":"user_warehouse","name":"Pro Rev","id":"Pro Rev::user_warehouse","directionality":"increase",
  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd",
    "criteria":[{"type":"metadata","column":"event","condition":"in","values":["purchase"]},
                {"type":"metadata","condition":"sql_filter","values":["COALESCE(plan_tier, 0) > 2"]}]}}`

func sqlFilterLeaf(column string) launchdarkly.EventFilter {
	return launchdarkly.EventFilter{Type: launchdarkly.EventFilterTypeEventProperty, Attribute: column, Op: ldOpIn, Values: []any{true}}
}

func TestConvert_WHN_SQLFilterFiltersOnItsColumn(t *testing.T) {
	res := mustConvert(t, sqlFilterSum, Options{LDDataSource: "ld-checkout"})
	if res.IsLossy() {
		t.Fatalf("a SQL filter should convert; LossyReasons=%v", res.LossyReasons)
	}
	want := &launchdarkly.EventFilter{
		Type: launchdarkly.EventFilterTypeGroup, Op: launchdarkly.EventFilterGroupOpAnd,
		Values: []any{
			launchdarkly.EventFilter{Type: launchdarkly.EventFilterTypeEventProperty, Attribute: "event", Op: ldOpIn, Values: []any{"purchase"}},
			sqlFilterLeaf(planTierColumn),
		},
	}
	if !reflect.DeepEqual(res.LDMetric.Filters, want) {
		t.Errorf("Filters = %#v\nwant %#v", res.LDMetric.Filters, want)
	}
	raw, _ := json.Marshal(res.LDMetric.Filters)
	if !strings.Contains(string(raw), `"attribute":"`+planTierColumn+`","op":"in","values":[true]`) {
		t.Errorf("wire payload = %s", raw)
	}
}

// planTierQueryFilters is how the data source's query pairs the filter with its column.
var planTierQueryFilters = []warehouse.CustomField{{Key: planTierColumn, Formula: "COALESCE(plan_tier, 0) > 2"}}

func TestConvert_WHN_SQLFilterTakesTheDataSourceColumnCase(t *testing.T) {
	upper := strings.ToUpper(planTierColumn)
	res := mustConvert(t, sqlFilterSum, Options{
		LDDataSource:         "ld-checkout",
		DataSourceColumns:    map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", upper}},
		DataSourceSQLFilters: map[string][]warehouse.CustomField{"ld-checkout": planTierQueryFilters},
	})
	if res.IsLossy() {
		t.Fatalf("LossyReasons=%v", res.LossyReasons)
	}
	leaves := res.LDMetric.Filters.Values
	if got := leaves[1].(launchdarkly.EventFilter).Attribute; got != upper {
		t.Errorf("attribute = %q, want %q", got, upper)
	}
}

func TestConvert_WHN_SQLFilterWithoutItsColumnIsLossy(t *testing.T) {
	res := mustConvert(t, sqlFilterSum, Options{
		LDDataSource:      "ld-checkout",
		DataSourceColumns: map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD"}},
	})
	if !res.IsLossy() {
		t.Fatal("a SQL filter whose column the data source lacks must keep the metric lossy")
	}
	if res.LDMetric.Filters != nil {
		t.Errorf("no filter should be emitted, got %#v", res.LDMetric.Filters)
	}
	assertHasWarning(t, res.LossyReasons, planTierColumn)
	assertHasWarning(t, res.LossyReasons, "--update-custom-fields")
	assertHasWarning(t, res.LossyReasons, "Dropped filters:")
	if len(res.FilterOutcomes) != 1 || res.FilterOutcomes[0].Applied || res.FilterOutcomes[0].BlockedBy != FilterBlockedSQLFilterColumn {
		t.Errorf("FilterOutcomes = %+v", res.FilterOutcomes)
	}
	if !slices.Contains(res.LossyCodes, WarnFilterSQLFilterColumn) {
		t.Errorf("LossyCodes = %v", res.LossyCodes)
	}
}

// Only warehouse adds the column, so a data source whose columns are unknown may lack it.
func TestConvert_WHN_SQLFilterOnUnknownColumnsIsNotedUnverified(t *testing.T) {
	res := mustConvert(t, sqlFilterSum, Options{LDDataSource: "ld-checkout"})
	if res.IsLossy() || res.LDMetric.Filters == nil {
		t.Fatalf("want the filter applied; LossyReasons=%v", res.LossyReasons)
	}
	if !slices.Contains(res.NoteCodes, WarnColumnUnverified) {
		t.Errorf("NoteCodes = %v, want %s", res.NoteCodes, WarnColumnUnverified)
	}
}

// warehouse never adds a column for a filter it cannot nest, whatever the columns.
func TestConvert_WHN_SQLFilterWarehouseCannotAddIsLossy(t *testing.T) {
	raw := strings.Replace(sqlFilterSum, `"COALESCE(plan_tier, 0) > 2"`, `"ts > {statsig_start_date}"`, 1)
	for name, cols := range map[string]map[string][]string{
		"columns unknown": nil,
		"column missing":  {"ld-checkout": {"EVENT", "PRICE_USD"}},
	} {
		t.Run(name, func(t *testing.T) {
			res := mustConvert(t, raw, Options{LDDataSource: "ld-checkout", DataSourceColumns: cols})
			if !res.IsLossy() || res.LDMetric.Filters != nil {
				t.Fatalf("want lossy with no filter; Filters=%#v LossyReasons=%v", res.LDMetric.Filters, res.LossyReasons)
			}
			assertHasWarning(t, res.LossyReasons, "Statsig macro")
			assertHasWarning(t, res.LossyReasons, "Fix it in Statsig")
		})
	}
}

// The event key matches every row, so an unfiltered term would count the whole source.
func TestConvert_WHN_SQLFilterWithoutItsColumnOnAConstantKeySourceIsNotCreated(t *testing.T) {
	res := mustConvert(t, sqlFilterSum, Options{
		LDDataSource:      "ld-checkout",
		ConstantEventKeys: map[string]string{"ld-checkout": "ld-checkout"},
		DataSourceColumns: map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD"}},
	})
	if !res.IsBlocked() {
		t.Errorf("want blocked even with --convert-lossy; BlockingReasons=%v", res.BlockingReasons)
	}
}

func TestConvertRatio_SQLFilterPerTerm(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"Pro Rev per Visit","id":"Pro Rev per Visit::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"ratio",
	    "metricSourceName":"Checkout","valueColumn":"revenue","numeratorAggregation":"sum",
	    "criteria":[{"type":"metadata","condition":"sql_filter","values":["COALESCE(plan_tier, 0) > 2"]}],
	    "denominatorMetricSourceName":"Visits","denominatorValueColumn":"visit_id","denominatorAggregation":"count",
	    "denominatorCriteria":[{"type":"metadata","condition":"sql_filter","values":["NOT COALESCE(is_bot, FALSE)"]}]}}`
	res := mustConvert(t, raw, Options{
		SourceMapping: map[string]string{"Checkout": "ld-checkout", "Visits": "ld-visits"},
		DataSourceColumns: map[string][]string{
			"ld-checkout": {"REVENUE", strings.ToUpper(planTierColumn)},
			"ld-visits":   {"VISIT_ID"},
		},
		DataSourceSQLFilters: map[string][]warehouse.CustomField{"ld-checkout": planTierQueryFilters},
	})
	if got, want := res.LDMetric.Filters, sqlFilterLeaf(strings.ToUpper(planTierColumn)); got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("numerator Filters = %#v, want %#v", got, want)
	}
	if res.LDMetric.Denominator.Filters != nil {
		t.Errorf("denominator filter needs a column ld-visits lacks, got %#v", res.LDMetric.Denominator.Filters)
	}
	assertHasWarning(t, res.LossyReasons, statsig.SQLFilterColumn("NOT COALESCE(is_bot, FALSE)"))
	assertHasWarning(t, res.LossyReasons, `"ld-visits"`)
}

func TestSQLFilters_ListsEachSourcesFiltersOnce(t *testing.T) {
	metrics := mustMetrics(t,
		sqlFilterSum,
		// Same filter, other spacing: the same column.
		`{"type":"user_warehouse","name":"Pro Count","warehouseNative":{"aggregation":"count","metricSourceName":"Checkout",
		  "criteria":[{"type":"metadata","condition":"sql_filter","values":["  COALESCE(plan_tier, 0) > 2 "]}]}}`,
		`{"type":"user_warehouse","name":"Ratio","warehouseNative":{"aggregation":"ratio","metricSourceName":"Checkout","numeratorAggregation":"sum",
		  "criteria":[{"type":"metadata","condition":"sql_filter","values":["LOWER(region) = 'eu'"]}],
		  "denominatorMetricSourceName":"Visits","denominatorAggregation":"count",
		  "denominatorCriteria":[{"type":"metadata","condition":"sql_filter","values":["NOT COALESCE(is_bot, FALSE)"]}]}}`,
		// A ratio's denominator without a source of its own reads the numerator's.
		`{"type":"user_warehouse","name":"Same Source Ratio","warehouseNative":{"aggregation":"ratio","metricSourceName":"Visits","numeratorAggregation":"count",
		  "denominatorAggregation":"count",
		  "denominatorCriteria":[{"type":"metadata","condition":"sql_filter","values":["COALESCE(is_new, FALSE)"]}]}}`,
		// Written as clauses, so it needs no column.
		`{"type":"user_warehouse","name":"Non-test Count","warehouseNative":{"aggregation":"count","metricSourceName":"Checkout",
		  "criteria":[{"type":"metadata","condition":"sql_filter","values":["NOT is_test"]}]}}`,
		// Not converted, so its filter needs no column.
		`{"type":"user_warehouse","name":"P90","warehouseNative":{"aggregation":"percentile","metricSourceName":"Checkout",
		  "criteria":[{"type":"metadata","condition":"sql_filter","values":["price > 0"]}]}}`,
		`{"type":"event_count_custom","name":"Cloud","metricEvents":[{"name":"purchase",
		  "criteria":[{"type":"metadata","condition":"sql_filter","values":["x = 1"]}]}]}`,
	)
	want := map[string][]string{
		"Checkout": {"COALESCE(plan_tier, 0) > 2", "LOWER(region) = 'eu'", "NOT is_test"},
		"Visits":   {"NOT COALESCE(is_bot, FALSE)", "COALESCE(is_new, FALSE)"},
	}
	if got := SQLFilters(metrics); !reflect.DeepEqual(got, want) {
		t.Errorf("SQLFilters = %#v\nwant %#v", got, want)
	}
	if got, want := SQLFilterClauses(metrics), map[string][]string{"Checkout": {"NOT is_test"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("SQLFilterClauses = %#v\nwant %#v", got, want)
	}
}

func mustMetrics(t *testing.T, raws ...string) []statsig.Metric {
	t.Helper()
	out := make([]statsig.Metric, len(raws))
	for i, raw := range raws {
		if err := json.Unmarshal([]byte(raw), &out[i]); err != nil {
			t.Fatalf("unmarshal fixture %d: %v", i, err)
		}
	}
	return out
}

const translatableSum = `{
  "type":"user_warehouse","name":"Real Rev","id":"Real Rev::user_warehouse","directionality":"increase",
  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd",
    "criteria":[{"type":"metadata","column":"event","condition":"in","values":["purchase"]},
                {"type":"metadata","condition":"sql_filter","values":["NOT is_test AND NOT is_refund"]}]}}`

// A simple SQL filter reads as clauses in LaunchDarkly and needs no added column.
func TestConvert_WHN_SimpleSQLFilterBecomesClauses(t *testing.T) {
	res := mustConvert(t, translatableSum, Options{
		LDDataSource:      "ld-checkout",
		DataSourceColumns: map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", "IS_TEST", "IS_REFUND"}},
	})
	if res.IsLossy() {
		t.Fatalf("LossyReasons=%v", res.LossyReasons)
	}
	want := &launchdarkly.EventFilter{
		Type: launchdarkly.EventFilterTypeGroup, Op: launchdarkly.EventFilterGroupOpAnd,
		Values: []any{
			prop("EVENT", ldOpIn, false, "purchase"),
			prop("IS_TEST", ldOpIn, false, false),
			prop("IS_REFUND", ldOpIn, false, false),
		},
	}
	if !reflect.DeepEqual(res.LDMetric.Filters, want) {
		t.Errorf("Filters = %#v\nwant %#v", res.LDMetric.Filters, want)
	}
}

// Without the data source's columns, neither their names nor their types are checked.
func TestConvert_WHN_SimpleSQLFilterOnUnknownColumnsIsNotedUnverified(t *testing.T) {
	res := mustConvert(t, translatableSum, Options{LDDataSource: "ld-checkout"})
	if res.IsLossy() || res.LDMetric.Filters == nil {
		t.Fatalf("want the filter applied; LossyReasons=%v", res.LossyReasons)
	}
	if !slices.Contains(res.NoteCodes, WarnColumnUnverified) {
		t.Errorf("NoteCodes = %v, want %s", res.NoteCodes, WarnColumnUnverified)
	}
}

// LaunchDarkly compares a clause's values with the column's text, which matches SQL's
// comparison only when the column holds that kind of value.
func TestConvert_WHN_SQLFilterBecomesClausesOnlyOnColumnsOfTheRightType(t *testing.T) {
	for _, tc := range []struct {
		name, sql, colType string
		clauses            bool
	}{
		{"true/false on a boolean", "NOT is_test", "BOOLEAN", true},
		{"true/false on a number", "NOT is_test", "FIXED", false},
		{"true/false on text", "is_test = TRUE", "TEXT", false},
		{"a string on text", "is_test = 'yes'", "VARCHAR(16777216)", true},
		{"a string on a number", "is_test = '1'", "FIXED", false},
		{"a string on a timestamp", "is_test = '2024-01-01'", "TIMESTAMP_NTZ", false},
		{"LIKE on text", "is_test LIKE 'y%'", "TEXT", true},
		{"a number on a number", "is_test > 0", "FIXED", true},
		{"a number on text", "is_test > 0", "TEXT", false},
		{"an OR checks each clause", "is_test OR is_refund", "BOOLEAN", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(translatableSum, "NOT is_test AND NOT is_refund", tc.sql, 1)
			types := map[string]string{"event": "TEXT", "price_usd": "FIXED", "is_test": tc.colType}
			res := mustConvert(t, raw, Options{
				LDDataSource:          "ld-checkout",
				DataSourceColumns:     map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", "IS_TEST"}},
				DataSourceColumnTypes: map[string]map[string]string{"ld-checkout": types},
			})
			// Without clauses the filter needs a column, which this data source lacks.
			if res.IsLossy() == tc.clauses {
				t.Errorf("lossy = %v, want clauses %v; Filters=%#v", res.IsLossy(), tc.clauses, res.LDMetric.Filters)
			}
			if got := SQLFilterClauses(mustMetrics(t, raw))["Checkout"]; !reflect.DeepEqual(got, []string{tc.sql}) {
				t.Errorf("SQLFilterClauses = %q, want the filter as a candidate for clauses", got)
			}
			if got := SQLFilterFitsColumns(tc.sql, types); got != tc.clauses {
				t.Errorf("SQLFilterFitsColumns = %v, want %v", got, tc.clauses)
			}
		})
	}
}

// A readable name is not unique to its SQL, so only the data source's own pairing of
// SQL and column counts.
func TestConvert_WHN_SQLFilterNeverTakesAnotherFiltersColumn(t *testing.T) {
	const other = "LOWER(region) = 'EU'"
	raw := strings.Replace(translatableSum, "NOT is_test AND NOT is_refund", "LOWER(region) = 'eu'", 1)
	cols := map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", "LOWER_REGION_EQ_EU"}}
	for name, filters := range map[string][]warehouse.CustomField{
		"the column holds another filter": {{Key: "lower_region_eq_eu", Formula: other}},
		"the query could not be read":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			res := mustConvert(t, raw, Options{LDDataSource: "ld-checkout", DataSourceColumns: cols,
				DataSourceSQLFilters: map[string][]warehouse.CustomField{"ld-checkout": filters}})
			if !res.IsLossy() || res.LDMetric.Filters != nil {
				t.Errorf("want lossy with no filter; Filters=%#v", res.LDMetric.Filters)
			}
		})
	}
}

// warehouse keeps a column for a filter only where it could not show clauses match the
// same rows, such as with no preview, whose column types are guesses.
func TestConvert_WHN_SQLFilterUsesTheColumnWarehouseKeptForIt(t *testing.T) {
	col := statsig.SQLFilterColumn("NOT is_test")
	res := mustConvert(t, strings.Replace(translatableSum, "NOT is_test AND NOT is_refund", "NOT is_test", 1), Options{
		LDDataSource:          "ld-checkout",
		DataSourceColumns:     map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", "IS_TEST", strings.ToUpper(col)}},
		DataSourceColumnTypes: map[string]map[string]string{"ld-checkout": {"event": "TEXT", "price_usd": "FLOAT", "is_test": "BOOLEAN"}},
		DataSourceSQLFilters:  map[string][]warehouse.CustomField{"ld-checkout": {{Key: col, Formula: "NOT is_test"}}},
	})
	if res.IsLossy() {
		t.Fatalf("LossyReasons=%v", res.LossyReasons)
	}
	if got, want := res.LDMetric.Filters.Values[1], sqlFilterLeaf(strings.ToUpper(col)); !reflect.DeepEqual(got, want) {
		t.Errorf("second clause = %#v, want %#v", got, want)
	}
}

// An earlier version gave every SQL filter a column, so its column says nothing about
// whether clauses fit, and they win when they do.
func TestConvert_WHN_SQLFilterPrefersClausesToAHashNamedColumn(t *testing.T) {
	legacy := "SQL_FILTER_" + strings.ToUpper(hashPrefix("NOT is_test"))
	for _, tc := range []struct {
		isTestType, want string
	}{{"BOOLEAN", "IS_TEST"}, {"FIXED", legacy}} {
		res := mustConvert(t, strings.Replace(translatableSum, "NOT is_test AND NOT is_refund", "NOT is_test", 1), Options{
			LDDataSource:          "ld-checkout",
			DataSourceColumns:     map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", "IS_TEST", legacy}},
			DataSourceColumnTypes: map[string]map[string]string{"ld-checkout": {"event": "TEXT", "price_usd": "FIXED", "is_test": tc.isTestType}},
			DataSourceSQLFilters:  map[string][]warehouse.CustomField{"ld-checkout": {{Key: strings.ToLower(legacy), Formula: "NOT is_test"}}},
		})
		if res.IsLossy() {
			t.Fatalf("%s: LossyReasons=%v", tc.isTestType, res.LossyReasons)
		}
		if got := res.LDMetric.Filters.Values[1].(launchdarkly.EventFilter).Attribute; got != tc.want {
			t.Errorf("%s: attribute = %q, want %q", tc.isTestType, got, tc.want)
		}
	}
}

// Once the data source's query is read, a filter it has no column for has none.
func TestConvert_WHN_SQLFilterMissingFromAReadQueryIsLossy(t *testing.T) {
	res := mustConvert(t, sqlFilterSum, Options{LDDataSource: "ld-checkout",
		DataSourceSQLFilters: map[string][]warehouse.CustomField{"ld-checkout": nil}})
	if !res.IsLossy() || res.LDMetric.Filters != nil {
		t.Errorf("want lossy with no filter; Filters=%#v", res.LDMetric.Filters)
	}
}

// A column an earlier version named by hash is unique to its SQL, so its name is enough.
func TestConvert_WHN_SQLFilterTakesAHashNamedColumnByName(t *testing.T) {
	legacy := "SQL_FILTER_" + strings.ToUpper(hashPrefix("COALESCE(plan_tier, 0) > 2"))
	res := mustConvert(t, sqlFilterSum, Options{LDDataSource: "ld-checkout",
		DataSourceColumns: map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", legacy}}})
	if res.IsLossy() {
		t.Fatalf("LossyReasons=%v", res.LossyReasons)
	}
	if got := res.LDMetric.Filters.Values[1].(launchdarkly.EventFilter).Attribute; got != legacy {
		t.Errorf("attribute = %q, want %q", got, legacy)
	}
}

func hashPrefix(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:8])
}

// An OR sits inside the term's AND, within LaunchDarkly's three levels.
func TestConvert_WHN_SQLFilterWithOrBecomesAGroup(t *testing.T) {
	raw := strings.Replace(translatableSum, "NOT is_test AND NOT is_refund", "is_test OR is_refund", 1)
	res := mustConvert(t, raw, Options{LDDataSource: "ld-checkout"})
	if res.IsLossy() {
		t.Fatalf("LossyReasons=%v", res.LossyReasons)
	}
	want := &launchdarkly.EventFilter{
		Type: launchdarkly.EventFilterTypeGroup, Op: launchdarkly.EventFilterGroupOpAnd,
		Values: []any{prop("event", ldOpIn, false, "purchase"), or(prop("is_test", ldOpIn, false, true), prop("is_refund", ldOpIn, false, true))},
	}
	if !reflect.DeepEqual(res.LDMetric.Filters, want) {
		t.Errorf("Filters = %#v\nwant %#v", res.LDMetric.Filters, want)
	}
}

// Over LaunchDarkly's clause limit, the SQL filter checks its column instead.
func TestConvert_WHN_SQLFilterOverTheClauseLimitKeepsItsColumn(t *testing.T) {
	for _, sql := range []string{
		"a > 1 AND b > 1 AND c > 1 AND d > 1 AND e > 1 AND f > 1 AND g > 1 AND h > 1 AND i > 1 AND j > 1",
		"a > 1 OR b > 1 OR c > 1 OR d > 1 OR e > 1 OR f > 1 OR g > 1 OR h > 1 OR i > 1 OR j > 1",
	} {
		t.Run(sql, func(t *testing.T) { testSQLFilterKeepsItsColumn(t, sql) })
	}
}

func testSQLFilterKeepsItsColumn(t *testing.T, sql string) {
	raw := strings.Replace(translatableSum, "NOT is_test AND NOT is_refund", sql, 1)
	col := statsig.SQLFilterColumn(sql)
	res := mustConvert(t, raw, Options{LDDataSource: "ld-checkout"})
	if res.IsLossy() || res.LDMetric.Filters == nil || len(res.LDMetric.Filters.Values) != 2 {
		t.Fatalf("Filters=%#v LossyReasons=%v", res.LDMetric.Filters, res.LossyReasons)
	}
	if got := res.LDMetric.Filters.Values[1]; !reflect.DeepEqual(got, sqlFilterLeaf(col)) {
		t.Errorf("second clause = %#v, want %#v", got, sqlFilterLeaf(col))
	}
	if got := SQLFilterClauses(mustMetrics(t, raw)); len(got) != 0 {
		t.Errorf("SQLFilterClauses = %#v, want the filter to need its column", got)
	}
}

// Nothing in warehouse checks a translated filter's columns, so convert does.
func TestConvert_WHN_SimpleSQLFilterOnAMissingColumnIsLossy(t *testing.T) {
	res := mustConvert(t, translatableSum, Options{
		LDDataSource:      "ld-checkout",
		DataSourceColumns: map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", "IS_TEST"}},
	})
	if !res.IsLossy() {
		t.Fatal("want lossy: the data source has no is_refund column")
	}
	assertHasWarning(t, res.LossyReasons, `"is_refund"`)
}

// warehouse may number a filter's column, or an earlier version may have hashed it.
func TestConvert_WHN_SQLFilterUsesTheColumnItsDataSourceGivesIt(t *testing.T) {
	for _, col := range []string{planTierColumn + "_2", "sql_filter_6e4004268a3c8fc1"} {
		res := mustConvert(t, sqlFilterSum, Options{
			LDDataSource:         "ld-checkout",
			DataSourceColumns:    map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", strings.ToUpper(col)}},
			DataSourceSQLFilters: map[string][]warehouse.CustomField{"ld-checkout": {{Key: col, Formula: "COALESCE(plan_tier, 0) > 2"}}},
		})
		if res.IsLossy() {
			t.Fatalf("%s: LossyReasons=%v", col, res.LossyReasons)
		}
		if got := res.LDMetric.Filters.Values[1].(launchdarkly.EventFilter).Attribute; got != strings.ToUpper(col) {
			t.Errorf("attribute = %q, want %q", got, strings.ToUpper(col))
		}
	}
}

// A column's name only hints at its filter, so the metric says what the filter was.
func TestConvert_WHN_DescriptionQuotesTheSQLFilters(t *testing.T) {
	res := mustConvert(t, sqlFilterSum, Options{LDDataSource: "ld-checkout"})
	if want := "\nStatsig SQL filter: COALESCE(plan_tier, 0) > 2"; !strings.HasSuffix(res.LDMetric.Description, want) {
		t.Errorf("Description = %q, want it to end %q", res.LDMetric.Description, want)
	}

	ratio := mustConvert(t, `{
	  "type":"user_warehouse","name":"Rev per Visit","id":"Rev per Visit::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"ratio","metricSourceName":"Checkout","valueColumn":"revenue","numeratorAggregation":"sum",
	    "criteria":[{"type":"metadata","condition":"sql_filter","values":["NOT is_test"]}],
	    "denominatorMetricSourceName":"Visits","denominatorValueColumn":"visit_id","denominatorAggregation":"count",
	    "denominatorCriteria":[{"type":"metadata","condition":"sql_filter","values":["LOWER(region) = 'eu'"]}]}}`,
		Options{SourceMapping: map[string]string{"Checkout": "ld-checkout", "Visits": "ld-visits"}})
	if want := "\nStatsig numerator SQL filter: NOT is_test\nStatsig denominator SQL filter: LOWER(region) = 'eu'"; !strings.HasSuffix(ratio.LDMetric.Description, want) {
		t.Errorf("ratio Description = %q, want it to end %q", ratio.LDMetric.Description, want)
	}

	// LaunchDarkly takes at most 768 characters.
	long := strings.Replace(sqlFilterSum, `"name":"Pro Rev",`, `"name":"Pro Rev","description":"`+strings.Repeat("x", 740)+`",`, 1)
	res = mustConvert(t, long, Options{LDDataSource: "ld-checkout"})
	if strings.Contains(res.LDMetric.Description, "SQL filter") {
		t.Errorf("Description is %d characters with the filter", len(res.LDMetric.Description))
	}
}

// A column the query adds but the column list lacks cannot be filtered on.
func TestConvert_WHN_SQLFilterPrefersClausesToAColumnMissingFromTheList(t *testing.T) {
	col := statsig.SQLFilterColumn("NOT is_test")
	res := mustConvert(t, strings.Replace(translatableSum, "NOT is_test AND NOT is_refund", "NOT is_test", 1), Options{
		LDDataSource:          "ld-checkout",
		DataSourceColumns:     map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", "IS_TEST"}},
		DataSourceColumnTypes: map[string]map[string]string{"ld-checkout": {"event": "TEXT", "price_usd": "FIXED", "is_test": "BOOLEAN"}},
		DataSourceSQLFilters:  map[string][]warehouse.CustomField{"ld-checkout": {{Key: col, Formula: "NOT is_test"}}},
	})
	if res.IsLossy() {
		t.Fatalf("LossyReasons=%v", res.LossyReasons)
	}
	if got := res.LDMetric.Filters.Values[1].(launchdarkly.EventFilter).Attribute; got != "IS_TEST" {
		t.Errorf("attribute = %q, want the clause on IS_TEST", got)
	}
}
