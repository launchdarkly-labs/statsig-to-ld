package converter

import "testing"

// LaunchDarkly now supports count_distinct on simple (non-ratio) metrics, not
// just on ratios. A simple count_distinct is NUMERIC: the per-unit value is a
// distinct count, and LD forces isNumeric=true because a binary distribution
// over per-unit counts yields a negative total variance. The column being
// counted travels in unitAggregationField.
func TestConvert_WHN_SimpleCountDistinctOnColumn_IsNumericCountDistinct(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"Distinct SKUs","id":"Distinct SKUs::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count_distinct","metricSourceName":"Orders","valueColumn":"sku_id"}}`
	res := mustConvert(t, raw, Options{LDDataSource: "snowflake-ds"})

	if res.LDMetric.UnitAggregationType != "count_distinct" {
		t.Errorf("UnitAggregationType = %q, want count_distinct", res.LDMetric.UnitAggregationType)
	}
	if res.LDMetric.UnitAggregationField != "sku_id" {
		t.Errorf("UnitAggregationField = %q, want sku_id (the column being counted)", res.LDMetric.UnitAggregationField)
	}
	if res.LDMetric.IsNumeric == nil || !*res.LDMetric.IsNumeric {
		t.Error("a simple count_distinct metric must be numeric")
	}
	if res.IsLossy() {
		t.Errorf("count_distinct on a column is now expressible in LD and must not be lossy; LossyReasons=%v", res.LossyReasons)
	}
}

// LD only accepts a simple count_distinct on a warehouse-native metric: the
// create is rejected without a data source. With none bound we cannot emit one,
// so the binary approximation and its lossy flag stay.
func TestConvert_WHN_SimpleCountDistinct_NoDataSource_StaysLossyBinary(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"Distinct SKUs","id":"Distinct SKUs::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count_distinct","metricSourceName":"Orders","valueColumn":"sku_id"}}`
	res := mustConvert(t, raw, Options{})

	if res.LDMetric.UnitAggregationType != "average" {
		t.Errorf("UnitAggregationType = %q, want average (no data source, so count_distinct is not accepted)", res.LDMetric.UnitAggregationType)
	}
	if res.LDMetric.UnitAggregationField != "" {
		t.Errorf("UnitAggregationField = %q, want empty on the binary fallback", res.LDMetric.UnitAggregationField)
	}
	if res.LDMetric.IsNumeric == nil || *res.LDMetric.IsNumeric {
		t.Error("the binary fallback must be non-numeric")
	}
	if !res.IsLossy() {
		t.Errorf("count_distinct without a data source loses the distinct-value count and must stay lossy; LossyReasons=%v", res.LossyReasons)
	}
}

// A count_distinct with no column counts distinct UNITS, which an LD binary
// metric already expresses exactly. LD also requires unitAggregationField for
// count_distinct, so there is nothing to emit. Faithful, not lossy: unchanged.
func TestConvert_WHN_CountDistinctNoColumn_StaysBinaryAndClean(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"Distinct Users","id":"Distinct Users::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"count_distinct","metricSourceName":"Orders"}}`
	res := mustConvert(t, raw, Options{LDDataSource: "snowflake-ds"})

	if res.LDMetric.UnitAggregationType != "average" {
		t.Errorf("UnitAggregationType = %q, want average (distinct units is an LD binary metric)", res.LDMetric.UnitAggregationType)
	}
	if res.LDMetric.UnitAggregationField != "" {
		t.Errorf("UnitAggregationField = %q, want empty (no column to count)", res.LDMetric.UnitAggregationField)
	}
	if res.IsLossy() {
		t.Errorf("distinct-units converts exactly and must not be lossy; LossyReasons=%v", res.LossyReasons)
	}
}
