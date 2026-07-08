package converter

import (
	"encoding/json"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// The fixtures below use the real warehouse-native shapes confirmed against
// Statsig's own public repos (statsig-io/semantic_layer dumps + the
// terraform-provider-statsig WarehouseNativeAPIModel): the flat
// warehouseNative.{valueColumn,criteria,rollupTimeWindow,denominator*} form, and
// the metricSources[] array form. Values are synthetic; only the shape matters.

func mustConvert(t *testing.T, raw string, opts Options) *Result {
	t.Helper()
	var m statsig.Metric
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	res, err := Convert(&m, opts)
	if err != nil {
		t.Fatalf("Convert returned error: %v", err)
	}
	return res
}

func TestConvert_WHN_SumFlatForm(t *testing.T) {
	// The exact shape that errored before this fix: flat valueColumn, no
	// metricSources[] array.
	raw := `{
	  "type":"user_warehouse","name":"Purchase Revenue","id":"Purchase Revenue::user_warehouse",
	  "directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout Events","valueColumn":"price_usd",
	    "winsorizationLow":0.001,"winsorizationHigh":0.999}}`
	res := mustConvert(t, raw, Options{LDDataSource: "snowflake-ds"})

	if res.LDMetric.EventKey != "price_usd" {
		t.Errorf("EventKey = %q, want price_usd (from flat warehouseNative.valueColumn)", res.LDMetric.EventKey)
	}
	if res.LDMetric.IsNumeric == nil || !*res.LDMetric.IsNumeric {
		t.Error("sum metric should be numeric")
	}
	if res.LDMetric.UnitAggregationType != "sum" {
		t.Errorf("UnitAggregationType = %q, want sum", res.LDMetric.UnitAggregationType)
	}
	if res.LDMetric.DataSource == nil || res.LDMetric.DataSource.Key != "snowflake-ds" {
		t.Errorf("DataSource = %+v, want snowflake-ds", res.LDMetric.DataSource)
	}
	if res.LDMetric.WinsorLowerPercentile == nil || *res.LDMetric.WinsorLowerPercentile != 0.1 {
		t.Errorf("WinsorLowerPercentile = %v, want 0.1 (0.001*100)", res.LDMetric.WinsorLowerPercentile)
	}
	if res.LDMetric.WinsorUpperPercentile == nil || *res.LDMetric.WinsorUpperPercentile != 99.9 {
		t.Errorf("WinsorUpperPercentile = %v, want 99.9 (0.999*100)", res.LDMetric.WinsorUpperPercentile)
	}
	if res.IsLossy() {
		t.Errorf("clean WHN sum (no dropped features) should not be lossy; LossyReasons=%v", res.LossyReasons)
	}
}

func TestConvert_WHN_ArrayFormStillWorks(t *testing.T) {
	// The metricSources[] array form must keep working alongside the flat form.
	raw := `{
	  "type":"user_warehouse","name":"Revenue Arr","id":"Revenue Arr::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSources":[{"metricSourceName":"Checkout","valueColumn":"amount"}]}}`
	res := mustConvert(t, raw, Options{LDDataSource: "snowflake-ds"})
	if res.LDMetric.EventKey != "amount" {
		t.Errorf("EventKey = %q, want amount (from metricSources[0].valueColumn)", res.LDMetric.EventKey)
	}
	if res.LDMetric.DataSource == nil || res.LDMetric.DataSource.Key != "snowflake-ds" {
		t.Errorf("DataSource = %+v, want snowflake-ds", res.LDMetric.DataSource)
	}
}

func TestConvert_WHN_CountDistinctFlatForm(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"Distinct SKUs","id":"Distinct SKUs::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count_distinct","metricSourceName":"Orders","valueColumn":"sku_id"}}`
	res := mustConvert(t, raw, Options{LDDataSource: "snowflake-ds"})
	if res.LDMetric.UnitAggregationType != "count_distinct" {
		t.Errorf("UnitAggregationType = %q, want count_distinct", res.LDMetric.UnitAggregationType)
	}
	if res.LDMetric.UnitAggregationField != "sku_id" {
		t.Errorf("UnitAggregationField = %q, want sku_id (the value column)", res.LDMetric.UnitAggregationField)
	}
}

func TestConvert_WHN_DailyParticipation_WindowInsideWarehouseNative(t *testing.T) {
	// aggregation=daily_participation (lossy binary approximation), no value
	// column (it counts active days per unit), with a custom rollup window carried
	// INSIDE warehouseNative — matches the real user_count fixture.
	raw := `{
	  "type":"user_warehouse","name":"User Count","id":"User Count::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"daily_participation","metricSourceName":"Checkout Events",
	    "rollupTimeWindow":"custom","customRollUpStart":0,"customRollUpEnd":5}}`
	res := mustConvert(t, raw, Options{LDDataSource: "snowflake-ds"})
	if !res.IsLossy() {
		t.Errorf("daily_participation should be lossy (binary approximation); LossyReasons=%v", res.LossyReasons)
	}
	// No value column → eventKey falls back to the source name (provisional).
	if res.LDMetric.EventKey != "Checkout Events" {
		t.Errorf("EventKey = %q, want \"Checkout Events\" (source-name fallback for a value-less aggregation)", res.LDMetric.EventKey)
	}
	assertHasWarning(t, res.Warnings, "no value column")
	if res.LDMetric.WindowEndOffset == nil {
		t.Fatal("custom window inside warehouseNative should be applied when a data source is bound")
	}
	if want := int64(5 * millisPerDay); *res.LDMetric.WindowEndOffset != want {
		t.Errorf("WindowEndOffset = %d, want %d (5 days)", *res.LDMetric.WindowEndOffset, want)
	}
}

func TestConvert_WHN_Criteria_IsLossy(t *testing.T) {
	// warehouse-native filters live on warehouseNative.criteria and are dropped.
	raw := `{
	  "type":"user_warehouse","name":"Filtered Rev","id":"Filtered Rev::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd",
	    "criteria":[{"type":"metadata","column":"event","condition":"in","values":["purchase"]}]}}`
	var m statsig.Metric
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res, err := Convert(&m, Options{LDDataSource: "snowflake-ds"})
	if err != nil {
		t.Fatalf("should still convert (lossy), got error: %v", err)
	}
	if !res.IsLossy() {
		t.Errorf("dropped warehouse-native criteria should mark the conversion lossy; LossyReasons=%v", res.LossyReasons)
	}
	assertHasWarning(t, res.LossyReasons, "DATA LOSS")
}

func TestConvert_WHN_Ratio_FlatDenominatorColumn(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"Rev per Visit","id":"Rev per Visit::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"ratio",
	    "metricSourceName":"Checkout","valueColumn":"revenue","numeratorAggregation":"sum",
	    "denominatorMetricSourceName":"Visits","denominatorValueColumn":"visit_id","denominatorAggregation":"count"}}`
	res := mustConvert(t, raw, Options{SourceMapping: map[string]string{
		"Checkout": "ld-checkout",
		"Visits":   "ld-visits",
	}})
	if res.LDMetric.EventKey != "revenue" {
		t.Errorf("numerator EventKey = %q, want revenue (valueColumn)", res.LDMetric.EventKey)
	}
	if res.LDMetric.DataSource == nil || res.LDMetric.DataSource.Key != "ld-checkout" {
		t.Errorf("numerator DataSource = %+v, want ld-checkout", res.LDMetric.DataSource)
	}
	if res.LDMetric.Denominator == nil {
		t.Fatal("ratio should populate a denominator")
	}
	if res.LDMetric.Denominator.EventName != "visit_id" {
		t.Errorf("denominator EventName = %q, want visit_id (denominatorValueColumn)", res.LDMetric.Denominator.EventName)
	}
	if res.LDMetric.Denominator.DataSource == nil || res.LDMetric.Denominator.DataSource.Key != "ld-visits" {
		t.Errorf("denominator DataSource = %+v, want ld-visits (its own source)", res.LDMetric.Denominator.DataSource)
	}
}

func TestConvert_WHN_AdvancedFields_AdvisoryVsLossy(t *testing.T) {
	// CUPED + dimension columns are analysis-only → advisory (not lossy);
	// valueThreshold changes the numbers → lossy.
	raw := `{
	  "type":"user_warehouse","name":"Adv","id":"Adv::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v",
	    "cupedAttributionWindow":7,"metricDimensionColumns":["product","page"],"valueThreshold":100}}`
	res := mustConvert(t, raw, Options{LDDataSource: "snowflake-ds"})
	assertHasWarning(t, res.Warnings, "not carried over") // cuped + dimensions advisory
	if !res.IsLossy() {
		t.Error("valueThreshold should mark the conversion lossy")
	}
	assertHasWarning(t, res.LossyReasons, "value threshold")
}

func TestConvert_WHN_NoUnitTypes_DefaultsToUser(t *testing.T) {
	// Warehouse-native dumps often omit unitTypes; default the LD randomization
	// unit to "user" with an advisory warning rather than emitting an empty list.
	raw := `{
	  "type":"user_warehouse","name":"No Units","id":"No Units::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v"}}`
	res := mustConvert(t, raw, Options{LDDataSource: "snowflake-ds"})
	if got := res.LDMetric.RandomizationUnits; len(got) != 1 || got[0] != "user" {
		t.Errorf("RandomizationUnits = %v, want [user]", got)
	}
	assertHasWarning(t, res.Warnings, "defaulted the LD randomization unit")
}
