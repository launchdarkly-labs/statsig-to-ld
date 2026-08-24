package converter

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

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
	if got := res.LDMetric.AnalysisUnits; len(got) != 1 || got[0] != "user" {
		t.Errorf("AnalysisUnits = %v, want [user] (from the source's userID id-type)", got)
	}
	assertHasWarning(t, res.Warnings, "metric source")
}

func TestConvert_WHN_NoUnitTypes_SourceCompanyID_WithMapping(t *testing.T) {
	res := mustConvert(t, whnNoUnitTypes, Options{
		LDDataSource:    "snowflake-ds",
		SourceUnitTypes: map[string][]string{"Checkout": {"companyID"}},
		UnitTypeMapping: map[string]string{"companyID": "company"},
	})
	if got := res.LDMetric.AnalysisUnits; len(got) != 1 || got[0] != "company" {
		t.Errorf("AnalysisUnits = %v, want [company] (source companyID via --unit-type-mapping)", got)
	}
}

func TestConvert_WHN_NoUnitTypes_NoSourceMapping_DefaultsToUser(t *testing.T) {
	// No SourceUnitTypes available: still fall back to "user" (unchanged behavior).
	res := mustConvert(t, whnNoUnitTypes, Options{LDDataSource: "snowflake-ds"})
	if got := res.LDMetric.AnalysisUnits; len(got) != 1 || got[0] != "user" {
		t.Errorf("AnalysisUnits = %v, want [user] (default)", got)
	}
	assertHasWarning(t, res.Warnings, "defaulted the LD analysis unit")
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
	if got := res.LDMetric.AnalysisUnits; len(got) != 1 || got[0] != "user" {
		t.Errorf("AnalysisUnits = %v, want [user] (metric's own unitTypes win)", got)
	}
}

const whnSumOnCheckout = `{
  "type":"user_warehouse","name":"Rev","id":"Rev::user_warehouse","directionality":"increase",
  "unitTypes":["userID"],
  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd"}}`

func checkoutSource() Options {
	return Options{
		LDDataSource:    "snowflake-ds",
		SourceUnitTypes: map[string][]string{"Checkout": {"companyID", "userID"}},
		UnitTypeMapping: map[string]string{"companyID": "company"},
	}
}

func TestAnalysisUnits_WidenFromSource(t *testing.T) {
	opts := checkoutSource()
	opts.WidenAnalysisUnits = true
	res := mustConvert(t, whnSumOnCheckout, opts)

	want := []string{"user", "company"}
	if got := res.LDMetric.AnalysisUnits; !slices.Equal(got, want) {
		t.Errorf("AnalysisUnits = %v, want %v (own unitTypes first, then the source's other id types)", got, want)
	}
	assertHasWarning(t, res.Warnings, "analysis units widened")
}

func TestAnalysisUnits_WidenOffKeepsOwnUnitTypes(t *testing.T) {
	res := mustConvert(t, whnSumOnCheckout, checkoutSource())

	if got := res.LDMetric.AnalysisUnits; !slices.Equal(got, []string{"user"}) {
		t.Errorf("AnalysisUnits = %v, want [user] (metric's own unitTypes only)", got)
	}
	assertNoWarning(t, res.Warnings, "analysis units widened")
}

func TestAnalysisUnits_ExtraUnitsAppendedAndDeduped(t *testing.T) {
	opts := checkoutSource()
	opts.WidenAnalysisUnits = true
	opts.ExtraAnalysisUnits = []string{"request", "user"}
	res := mustConvert(t, whnSumOnCheckout, opts)

	want := []string{"user", "company", "request"}
	if got := res.LDMetric.AnalysisUnits; !slices.Equal(got, want) {
		t.Errorf("AnalysisUnits = %v, want %v ('user' already present, not repeated)", got, want)
	}
}

// LaunchDarkly accepts both names but `randomizationUnits` is deprecated.
func TestPayloadUsesAnalysisUnitsField(t *testing.T) {
	res := mustConvert(t, whnSumOnCheckout, checkoutSource())
	raw, err := json.Marshal(res.LDMetric)
	if err != nil {
		t.Fatalf("marshal metric: %v", err)
	}
	if !strings.Contains(string(raw), `"analysisUnits":["user"]`) {
		t.Errorf("payload = %s, want an analysisUnits field", raw)
	}
	if strings.Contains(string(raw), "randomizationUnits") {
		t.Errorf("payload still sends the deprecated randomizationUnits: %s", raw)
	}
}

func assertNoWarning(t *testing.T, warnings []string, substr string) {
	t.Helper()
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			t.Errorf("unexpected warning containing %q: %v", substr, warnings)
		}
	}
}
