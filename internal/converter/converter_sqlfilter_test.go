package converter

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

var planTierColumn = statsig.SQLFilterColumn("plan_tier > 2")

const sqlFilterSum = `{
  "type":"user_warehouse","name":"Pro Rev","id":"Pro Rev::user_warehouse","directionality":"increase",
  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd",
    "criteria":[{"type":"metadata","column":"event","condition":"in","values":["purchase"]},
                {"type":"metadata","condition":"sql_filter","values":["plan_tier > 2"]}]}}`

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

func TestConvert_WHN_SQLFilterTakesTheDataSourceColumnCase(t *testing.T) {
	upper := strings.ToUpper(planTierColumn)
	res := mustConvert(t, sqlFilterSum, Options{
		LDDataSource:      "ld-checkout",
		DataSourceColumns: map[string][]string{"ld-checkout": {"EVENT", "PRICE_USD", upper}},
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
	raw := strings.Replace(sqlFilterSum, `"plan_tier > 2"`, `"ts > {statsig_start_date}"`, 1)
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
	    "criteria":[{"type":"metadata","condition":"sql_filter","values":["plan_tier > 2"]}],
	    "denominatorMetricSourceName":"Visits","denominatorValueColumn":"visit_id","denominatorAggregation":"count",
	    "denominatorCriteria":[{"type":"metadata","condition":"sql_filter","values":["is_bot = FALSE"]}]}}`
	res := mustConvert(t, raw, Options{
		SourceMapping: map[string]string{"Checkout": "ld-checkout", "Visits": "ld-visits"},
		DataSourceColumns: map[string][]string{
			"ld-checkout": {"REVENUE", strings.ToUpper(planTierColumn)},
			"ld-visits":   {"VISIT_ID"},
		},
	})
	if got, want := res.LDMetric.Filters, sqlFilterLeaf(strings.ToUpper(planTierColumn)); got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("numerator Filters = %#v, want %#v", got, want)
	}
	if res.LDMetric.Denominator.Filters != nil {
		t.Errorf("denominator filter needs a column ld-visits lacks, got %#v", res.LDMetric.Denominator.Filters)
	}
	assertHasWarning(t, res.LossyReasons, statsig.SQLFilterColumn("is_bot = FALSE"))
	assertHasWarning(t, res.LossyReasons, `"ld-visits"`)
}

func TestSQLFilters_ListsEachSourcesFiltersOnce(t *testing.T) {
	metrics := mustMetrics(t,
		sqlFilterSum,
		// Same filter, other spacing: the same column.
		`{"type":"user_warehouse","name":"Pro Count","warehouseNative":{"aggregation":"count","metricSourceName":"Checkout",
		  "criteria":[{"type":"metadata","condition":"sql_filter","values":["  plan_tier > 2 "]}]}}`,
		`{"type":"user_warehouse","name":"Ratio","warehouseNative":{"aggregation":"ratio","metricSourceName":"Checkout","numeratorAggregation":"sum",
		  "criteria":[{"type":"metadata","condition":"sql_filter","values":["region = 'EU'"]}],
		  "denominatorMetricSourceName":"Visits","denominatorAggregation":"count",
		  "denominatorCriteria":[{"type":"metadata","condition":"sql_filter","values":["is_bot = FALSE"]}]}}`,
		// A ratio's denominator without a source of its own reads the numerator's.
		`{"type":"user_warehouse","name":"Same Source Ratio","warehouseNative":{"aggregation":"ratio","metricSourceName":"Visits","numeratorAggregation":"count",
		  "denominatorAggregation":"count",
		  "denominatorCriteria":[{"type":"metadata","condition":"sql_filter","values":["is_new = TRUE"]}]}}`,
		// Not converted, so its filter needs no column.
		`{"type":"user_warehouse","name":"P90","warehouseNative":{"aggregation":"percentile","metricSourceName":"Checkout",
		  "criteria":[{"type":"metadata","condition":"sql_filter","values":["price > 0"]}]}}`,
		`{"type":"event_count_custom","name":"Cloud","metricEvents":[{"name":"purchase",
		  "criteria":[{"type":"metadata","condition":"sql_filter","values":["x = 1"]}]}]}`,
	)
	want := map[string][]string{
		"Checkout": {"plan_tier > 2", "region = 'EU'"},
		"Visits":   {"is_bot = FALSE", "is_new = TRUE"},
	}
	if got := SQLFilters(metrics); !reflect.DeepEqual(got, want) {
		t.Errorf("SQLFilters = %#v\nwant %#v", got, want)
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
