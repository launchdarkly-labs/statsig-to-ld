package converter

import "testing"

// Warehouse-native metrics usually omit unitTypes on the metric itself; the
// analysis unit comes from the metric source's id-type mapping. Options.
// SourceUnitTypes carries that mapping (source name -> Statsig unit IDs), and
// the converter should prefer it over the bare "user" default.

const whnNoUnitTypes = `{
  "type":"user_warehouse","name":"Rev","id":"Rev::user_warehouse","directionality":"increase",
  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd"}}`

func TestConvert_WHN_NoUnitTypes_UsesSourceIDType(t *testing.T) {
	res := mustConvert(t, whnNoUnitTypes, Options{
		LDDataSource:    "snowflake-ds",
		SourceUnitTypes: map[string][]string{"Checkout": {"userID"}},
	})
	if got := res.LDMetric.RandomizationUnits; len(got) != 1 || got[0] != "user" {
		t.Errorf("RandomizationUnits = %v, want [user] (from the source's userID id-type)", got)
	}
	assertHasWarning(t, res.Warnings, "metric source")
}

func TestConvert_WHN_NoUnitTypes_SourceCompanyID_WithMapping(t *testing.T) {
	res := mustConvert(t, whnNoUnitTypes, Options{
		LDDataSource:    "snowflake-ds",
		SourceUnitTypes: map[string][]string{"Checkout": {"companyID"}},
		UnitTypeMapping: map[string]string{"companyID": "company"},
	})
	if got := res.LDMetric.RandomizationUnits; len(got) != 1 || got[0] != "company" {
		t.Errorf("RandomizationUnits = %v, want [company] (source companyID via --unit-type-mapping)", got)
	}
}

func TestConvert_WHN_NoUnitTypes_NoSourceMapping_DefaultsToUser(t *testing.T) {
	// No SourceUnitTypes available: still fall back to "user" (unchanged behavior).
	res := mustConvert(t, whnNoUnitTypes, Options{LDDataSource: "snowflake-ds"})
	if got := res.LDMetric.RandomizationUnits; len(got) != 1 || got[0] != "user" {
		t.Errorf("RandomizationUnits = %v, want [user] (default)", got)
	}
	assertHasWarning(t, res.Warnings, "defaulted the LD randomization unit")
}

func TestConvert_WHN_ExplicitUnitTypes_IgnoreSource(t *testing.T) {
	// When the metric declares unitTypes, they win over the source mapping.
	raw := `{
	  "type":"user_warehouse","name":"Rev","id":"Rev::user_warehouse","directionality":"increase",
	  "unitTypes":["userID"],
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd"}}`
	res := mustConvert(t, raw, Options{
		LDDataSource:    "snowflake-ds",
		SourceUnitTypes: map[string][]string{"Checkout": {"companyID"}},
	})
	if got := res.LDMetric.RandomizationUnits; len(got) != 1 || got[0] != "user" {
		t.Errorf("RandomizationUnits = %v, want [user] (metric's own unitTypes win)", got)
	}
}
