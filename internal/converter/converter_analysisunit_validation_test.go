package converter

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

const whnSumWithUnits = `{
  "type":"user_warehouse","name":"Rev","id":"Rev::user_warehouse","directionality":"increase",
  "unitTypes":["userID","companyID"],
  "warehouseNative":{"aggregation":"sum","metricSourceName":"Checkout","valueColumn":"price_usd"}}`

// LaunchDarkly rejects a metric whose analysis units are not registered as
// randomization units on the project. Dropping the unregistered ones creates a
// usable metric; keeping them means no metric at all.
func TestAnalysisUnits_DropsUnregisteredUnits(t *testing.T) {
	res := mustConvert(t, whnSumWithUnits, Options{
		LDDataSource:            "ds",
		RegisteredAnalysisUnits: map[string]bool{"user": true},
	})

	if got := res.LDMetric.AnalysisUnits; !slices.Equal(got, []string{"user"}) {
		t.Errorf("AnalysisUnits = %v, want [user] (companyid is not registered)", got)
	}
	assertHasWarning(t, res.Warnings, "not registered")
	if !slices.Contains(res.WarningCodes, WarnAnalysisUnitNotRegistered) {
		t.Errorf("WarningCodes = %v, want %s", res.WarningCodes, WarnAnalysisUnitNotRegistered)
	}
	// Dropping units must not skip the metric.
	if res.IsLossy() {
		t.Errorf("dropping an unregistered unit should be advisory, not lossy: %v", res.LossyReasons)
	}
}

// An unknown registered set (no LD key, or the lookup failed) must not filter
// anything: silently narrowing on missing information is worse than a 400.
func TestAnalysisUnits_UnknownRegisteredSetFiltersNothing(t *testing.T) {
	res := mustConvert(t, whnSumWithUnits, Options{LDDataSource: "ds"})
	if got := res.LDMetric.AnalysisUnits; !slices.Equal(got, []string{"user", "companyid"}) {
		t.Errorf("AnalysisUnits = %v, want [user companyid] unfiltered", got)
	}
	assertNoWarning(t, res.Warnings, "not registered")
}

// Extras were appended after the empty-list check, so passing them silently
// replaced the "user" fallback and suppressed its warning.
func TestAnalysisUnits_ExtrasDoNotSuppressTheUserDefault(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"NoUnits","id":"NoUnits::user_warehouse","directionality":"increase",
	  "warehouseNative":{"aggregation":"sum","metricSourceName":"S","valueColumn":"v"}}`
	res := mustConvert(t, raw, Options{LDDataSource: "ds", ExtraAnalysisUnits: []string{"request"}})

	if got := res.LDMetric.AnalysisUnits; !slices.Contains(got, "user") {
		t.Errorf("AnalysisUnits = %v, want the \"user\" fallback kept alongside the extra", got)
	}
	assertHasWarning(t, res.Warnings, "defaulted the LD analysis unit")
}

// Widening is documented as inert for cloud metrics. The only guard was in the
// command layer, so a cloud metric carrying a top-level metricSourceName was
// widened anyway on a mixed run.
func TestAnalysisUnits_CloudMetricIsNeverWidened(t *testing.T) {
	raw := `{
	  "type":"event_count_custom","name":"CloudCount","id":"CloudCount::event_count_custom",
	  "directionality":"increase","unitTypes":["userID"],
	  "metricSourceName":"Checkout",
	  "metricEvents":[{"name":"purchase"}]}`
	res := mustConvert(t, raw, Options{
		WidenAnalysisUnits: true,
		SourceUnitTypes:    map[string][]string{"Checkout": {"userID", "companyID"}},
	})

	if got := res.LDMetric.AnalysisUnits; !slices.Equal(got, []string{"user"}) {
		t.Errorf("AnalysisUnits = %v, want [user]: a cloud metric has no warehouse source to widen from", got)
	}
}

// A ratio can only be analysed by a unit both of its sources can identify.
func TestAnalysisUnits_RatioWideningIntersectsBothSources(t *testing.T) {
	raw := `{
	  "type":"user_warehouse","name":"Conv","id":"Conv::user_warehouse","directionality":"increase",
	  "unitTypes":["userID"],
	  "warehouseNative":{"aggregation":"ratio","metricSourceName":"Numer","valueColumn":"a",
	    "denominatorMetricSourceName":"Denom","denominatorValueColumn":"b","denominatorAggregation":"sum"}}`

	var m statsig.Metric
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res, err := Convert(&m, Options{
		LDDataSource:       "ds",
		WidenAnalysisUnits: true,
		SourceUnitTypes: map[string][]string{
			"Numer": {"userID", "companyID"},
			"Denom": {"userID"},
		},
	})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	if got := res.LDMetric.AnalysisUnits; slices.Contains(got, "companyid") {
		t.Errorf("AnalysisUnits = %v: companyid is absent from the denominator source and must not be added", got)
	}
	if got := res.LDMetric.AnalysisUnits; !slices.Contains(got, "user") {
		t.Errorf("AnalysisUnits = %v, want user retained", got)
	}
}
