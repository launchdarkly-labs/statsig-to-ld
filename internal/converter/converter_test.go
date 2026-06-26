package converter

import (
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

func TestSanitizeKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"purchase_revenue::sum", "purchase-revenue-sum"},
		{"My Metric::event_count_custom", "my-metric-event-count-custom"},
		{"a--b__c::d", "a-b-c-d"},
		{"UPPER_CASE", "upper-case"},
		{"::leading_colons", "leading-colons"},
		{"trailing__", "trailing"},
	}
	for _, tt := range tests {
		got := SanitizeKey(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func boolPtr(b bool) *bool          { return &b }
func float64Ptr(f float64) *float64 { return &f }

func baseMetric(typ string) *statsig.Metric {
	return &statsig.Metric{
		ID:             "test_metric::" + typ,
		Name:           "test_metric",
		Type:           typ,
		Description:    "A test metric",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
		MetricEvents: []statsig.MetricEvent{
			{Name: "page_view", Type: "count", Criteria: []statsig.Criterion{}},
		},
	}
}

// --- Type mappings ---

func TestConvert_EventCountCustom(t *testing.T) {
	sg := baseMetric("event_count_custom")
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ld := result.LDMetric
	if ld.Kind != "custom" {
		t.Errorf("Kind = %q, want \"custom\"", ld.Kind)
	}
	if *ld.IsNumeric != false {
		t.Errorf("IsNumeric = %v, want false", *ld.IsNumeric)
	}
	if ld.UnitAggregationType != "sum" {
		t.Errorf("UnitAggregationType = %q, want \"sum\"", ld.UnitAggregationType)
	}
	if ld.AnalysisType != "mean" {
		t.Errorf("AnalysisType = %q, want \"mean\"", ld.AnalysisType)
	}
	if ld.EventKey != "page_view" {
		t.Errorf("EventKey = %q, want \"page_view\"", ld.EventKey)
	}
	if ld.SuccessCriteria != "HigherThanBaseline" {
		t.Errorf("SuccessCriteria = %q, want \"HigherThanBaseline\"", ld.SuccessCriteria)
	}
	if ld.Key != "test-metric-event-count-custom" {
		t.Errorf("Key = %q, want \"test-metric-event-count-custom\"", ld.Key)
	}
	if ld.EventDefault != nil {
		t.Errorf("EventDefault should be nil for non-numeric, got %+v", ld.EventDefault)
	}
}

func TestConvert_Sum(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ld := result.LDMetric
	if *ld.IsNumeric != true {
		t.Errorf("IsNumeric = %v, want true", *ld.IsNumeric)
	}
	if ld.UnitAggregationType != "sum" {
		t.Errorf("UnitAggregationType = %q, want \"sum\"", ld.UnitAggregationType)
	}
	if ld.Unit != "units" {
		t.Errorf("Unit = %q, want \"units\"", ld.Unit)
	}
	if ld.EventDefault == nil || ld.EventDefault.Value != 0 {
		t.Errorf("EventDefault should be {Disabled:false, Value:0}, got %+v", ld.EventDefault)
	}
}

func TestConvert_Mean(t *testing.T) {
	sg := baseMetric("mean")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "api_response_time", Type: "value"}
	sg.Directionality = "decrease"
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ld := result.LDMetric
	if *ld.IsNumeric != true {
		t.Errorf("IsNumeric = %v, want true", *ld.IsNumeric)
	}
	if ld.UnitAggregationType != "average" {
		t.Errorf("UnitAggregationType = %q, want \"average\"", ld.UnitAggregationType)
	}
	if ld.SuccessCriteria != "LowerThanBaseline" {
		t.Errorf("SuccessCriteria = %q, want \"LowerThanBaseline\"", ld.SuccessCriteria)
	}
	if ld.EventDefault == nil || !ld.EventDefault.Disabled {
		t.Errorf("EventDefault should be {Disabled:true} for mean metrics, got %+v", ld.EventDefault)
	}
}

func TestConvert_EventUser(t *testing.T) {
	sg := baseMetric("event_user")
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ld := result.LDMetric
	if *ld.IsNumeric != false {
		t.Errorf("IsNumeric = %v, want false", *ld.IsNumeric)
	}
	if ld.UnitAggregationType != "average" {
		t.Errorf("UnitAggregationType = %q, want \"average\"", ld.UnitAggregationType)
	}
}

func TestConvert_EventUserWindow(t *testing.T) {
	sg := baseMetric("event_user_window")
	_, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Incompatible types ---

func TestConvert_IncompatibleTypes(t *testing.T) {
	// "ratio" was removed: see TestConvert_Ratio_* for the real branch.
	incompatible := []string{"funnel", "composite", "composite_sum", "percentile", "user"}
	for _, typ := range incompatible {
		sg := baseMetric(typ)
		_, err := Convert(sg, Options{})
		if err == nil {
			t.Errorf("expected error for type %q, got nil", typ)
			continue
		}
		if !IsIncompatible(err) {
			t.Errorf("expected IncompatibleError for type %q, got %T: %v", typ, err, err)
		}
	}
}

func TestConvert_UnknownType(t *testing.T) {
	sg := baseMetric("totally_made_up")
	_, err := Convert(sg, Options{})
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
	if IsIncompatible(err) {
		t.Error("unknown type should not be IncompatibleError (it's an unexpected input, not a known skip)")
	}
}

// --- Key validation ---

func TestConvert_EmptyKeyAfterSanitization(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.ID = ":::" // all punctuation → empty after sanitization
	_, err := Convert(sg, Options{})
	if err == nil {
		t.Fatal("expected error for metric with empty key after sanitization")
	}
	if IsIncompatible(err) {
		t.Error("empty key should not be IncompatibleError")
	}
}

func TestConvert_LongKeyTruncated(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.ID = strings.Repeat("a", 300) + "::event_count_custom"
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.LDMetric.Key) > maxLDKeyLength {
		t.Errorf("key length = %d, want <= %d", len(result.LDMetric.Key), maxLDKeyLength)
	}
	assertHasWarning(t, result.Warnings, "truncated")
}

// --- Randomization units ---

func TestConvert_RandomizationUnits(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.UnitTypes = []string{"userID", "companyID"}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	units := result.LDMetric.RandomizationUnits
	if len(units) != 2 || units[0] != "user" || units[1] != "companyid" {
		t.Errorf("RandomizationUnits = %v, want [user companyid]", units)
	}

	hasWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "companyID") && strings.Contains(w, "context kind") {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Error("expected warning about non-standard unitType companyID")
	}
}

// --- Unit type mapping ---

func TestConvert_UnitTypeMapping(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.UnitTypes = []string{"userID", "companyID"}
	opts := Options{
		UnitTypeMapping: map[string]string{
			"companyID": "company",
		},
	}
	result, err := Convert(sg, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	units := result.LDMetric.RandomizationUnits
	if len(units) != 2 || units[0] != "user" || units[1] != "company" {
		t.Errorf("RandomizationUnits = %v, want [user company]", units)
	}

	// Should NOT warn about companyID when it's in the mapping
	for _, w := range result.Warnings {
		if strings.Contains(w, "companyID") {
			t.Errorf("should not warn about mapped unitType, got warning: %s", w)
		}
	}
}

func TestConvert_UnitTypeMapping_CaseInsensitive(t *testing.T) {
	// Statsig returns "stableID" (camelCase). User wrote "stableid" (lowercase)
	// in their mapping file. Lookup should succeed and the LD value casing
	// should be preserved.
	sg := baseMetric("event_count_custom")
	sg.UnitTypes = []string{"stableID"}
	opts := Options{
		UnitTypeMapping: map[string]string{
			"stableid": "anonymousUser",
		},
	}
	result, err := Convert(sg, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.LDMetric.RandomizationUnits; len(got) != 1 || got[0] != "anonymousUser" {
		t.Errorf("RandomizationUnits = %v, want [anonymousUser]", got)
	}
	for _, w := range result.Warnings {
		if strings.Contains(w, "stableID") {
			t.Errorf("should not warn about mapped unitType, got warning: %s", w)
		}
	}
}

func TestConvert_UnitTypeMapping_ExactMatchPriority(t *testing.T) {
	// When both an exact and a case-folded match exist, exact wins. This
	// lets users disambiguate intentionally if they need to.
	sg := baseMetric("event_count_custom")
	sg.UnitTypes = []string{"FOO"}
	opts := Options{
		UnitTypeMapping: map[string]string{
			"FOO": "exact-match",
			"foo": "lower-match",
		},
	}
	result, err := Convert(sg, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := result.LDMetric.RandomizationUnits; len(got) != 1 || got[0] != "exact-match" {
		t.Errorf("RandomizationUnits = %v, want [exact-match]", got)
	}
}

// --- Multi-event warning ---

func TestConvert_MultipleMetricEvents(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.MetricEvents = append(sg.MetricEvents, statsig.MetricEvent{
		Name: "checkout", Type: "count",
	})
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHasWarning(t, result.Warnings, "2 metric events")
	assertHasWarning(t, result.Warnings, "1 additional events are ignored")
	// Should still use the first event
	if result.LDMetric.EventKey != "page_view" {
		t.Errorf("EventKey = %q, want \"page_view\"", result.LDMetric.EventKey)
	}
}

// --- Tags ---

func TestConvert_TagsMerged(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.Tags = []string{"revenue", "p0"}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tags := result.LDMetric.Tags
	if len(tags) != 3 {
		t.Fatalf("Tags = %v, want 3 tags", tags)
	}
	if tags[0] != "statsig-import" {
		t.Errorf("tags[0] = %q, want \"statsig-import\"", tags[0])
	}
	// Statsig tags should be sanitized and appended
	found := map[string]bool{}
	for _, tag := range tags {
		found[tag] = true
	}
	if !found["revenue"] || !found["p0"] {
		t.Errorf("expected tags to include revenue and p0, got %v", tags)
	}
}

func TestConvert_TagsNoDuplicate(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.Tags = []string{"statsig-import"} // same as the auto-added tag
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	count := 0
	for _, tag := range result.LDMetric.Tags {
		if tag == "statsig-import" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("\"statsig-import\" appeared %d times, want 1", count)
	}
}

// --- Default unit ---

func TestConvert_DefaultUnit(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	result, err := Convert(sg, Options{DefaultUnit: "$"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.Unit != "$" {
		t.Errorf("Unit = %q, want \"$\"", result.LDMetric.Unit)
	}
	// Should NOT have TODO warning when DefaultUnit is set
	for _, w := range result.Warnings {
		if strings.Contains(w, "TODO") {
			t.Error("should not have TODO warning when DefaultUnit is set")
		}
	}
}

func TestConvert_DefaultUnitFallback(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.Unit != "units" {
		t.Errorf("Unit = %q, want \"units\"", result.LDMetric.Unit)
	}
	for _, w := range result.Warnings {
		if strings.Contains(w, "unit") || strings.Contains(w, "TODO") {
			t.Errorf("should not warn about default unit fallback, got: %q", w)
		}
	}
}

// --- Feature warnings ---

func TestConvert_WarningCountDistinct(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.MetricEvents[0].Type = "count_distinct"
	sg.MetricEvents[0].MetadataKey = "item_category"
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHasWarning(t, result.Warnings, "distinct")
}

func TestConvert_WarningMetadata(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0].Type = "metadata"
	sg.MetricEvents[0].MetadataKey = "results_count"
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHasWarning(t, result.Warnings, "metadata")
}

func TestConvert_WarningCriteria_DataLoss(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.MetricEvents[0].Criteria = []statsig.Criterion{
		{Type: "metadata", Column: "item_category", Condition: "=", Values: []string{"electronics"}},
	}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHasWarning(t, result.Warnings, "DATA LOSS")
	// Should include the actual criteria details
	assertHasWarning(t, result.Warnings, "item_category")
	assertHasWarning(t, result.Warnings, "electronics")
}

func TestConvert_Winsorization(t *testing.T) {
	// Statsig winsorization bounds are fractions (0–1); LaunchDarkly expects
	// percentiles (0–100). 0.05/0.95 → 5/95. No warning when it maps.
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	sg.WarehouseNative = &statsig.WarehouseNative{
		WinsorizationLow:  float64Ptr(0.05),
		WinsorizationHigh: float64Ptr(0.95),
	}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.WinsorLowerPercentile == nil || *result.LDMetric.WinsorLowerPercentile != 5 {
		t.Errorf("WinsorLowerPercentile = %v, want 5", result.LDMetric.WinsorLowerPercentile)
	}
	if result.LDMetric.WinsorUpperPercentile == nil || *result.LDMetric.WinsorUpperPercentile != 95 {
		t.Errorf("WinsorUpperPercentile = %v, want 95", result.LDMetric.WinsorUpperPercentile)
	}
	for _, w := range result.Warnings {
		if strings.Contains(strings.ToLower(w), "winsor") {
			t.Errorf("should not warn about winsorization when it maps, got: %q", w)
		}
	}
}

func TestConvert_WinsorizationOnlyLowerBound(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	sg.WarehouseNative = &statsig.WarehouseNative{WinsorizationLow: float64Ptr(0.01)}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.WinsorLowerPercentile == nil || *result.LDMetric.WinsorLowerPercentile != 1 {
		t.Errorf("WinsorLowerPercentile = %v, want 1", result.LDMetric.WinsorLowerPercentile)
	}
	if result.LDMetric.WinsorUpperPercentile != nil {
		t.Errorf("WinsorUpperPercentile should be nil, got %v", *result.LDMetric.WinsorUpperPercentile)
	}
}

func TestConvert_WinsorizationOnOccurrenceMetricWarns(t *testing.T) {
	// LD rejects winsorization on occurrence metrics (non-numeric average, e.g.
	// event_user). Skip it and warn rather than emit a metric LD will reject.
	sg := baseMetric("event_user")
	sg.WarehouseNative = &statsig.WarehouseNative{
		WinsorizationLow:  float64Ptr(0.05),
		WinsorizationHigh: float64Ptr(0.95),
	}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.WinsorLowerPercentile != nil || result.LDMetric.WinsorUpperPercentile != nil {
		t.Error("winsorization must not be set on an occurrence metric")
	}
	assertHasWarning(t, result.Warnings, "winsor")
}

func TestConvert_WarningCapping(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	sg.WarehouseNative = &statsig.WarehouseNative{Cap: float64Ptr(500)}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHasWarning(t, result.Warnings, "capping")
}

func TestConvert_WarningLogTransform(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	sg.WarehouseNative = &statsig.WarehouseNative{UseLogTransform: boolPtr(true)}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHasWarning(t, result.Warnings, "log transform")
}

func TestConvert_WindowWithDataSource(t *testing.T) {
	// A custom rollup window (days) maps to LD window offsets (milliseconds)
	// when a data source is bound — LD requires a snowflake source for windows.
	// 0–3 days → 0 .. 259_200_000 ms.
	sg := baseMetric("event_user")
	sg.RollupTimeWindow = "custom"
	sg.CustomRollUpStart = float64Ptr(0)
	sg.CustomRollUpEnd = float64Ptr(3)
	result, err := Convert(sg, Options{LDDataSource: "snowflake-staging"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.WindowStartOffset == nil || *result.LDMetric.WindowStartOffset != 0 {
		t.Errorf("WindowStartOffset = %v, want 0", result.LDMetric.WindowStartOffset)
	}
	if result.LDMetric.WindowEndOffset == nil || *result.LDMetric.WindowEndOffset != 259_200_000 {
		t.Errorf("WindowEndOffset = %v, want 259200000 (3 days in ms)", result.LDMetric.WindowEndOffset)
	}
	for _, w := range result.Warnings {
		if strings.Contains(strings.ToLower(w), "window") {
			t.Errorf("should not warn about the window when it maps, got: %q", w)
		}
	}
}

func TestConvert_WindowWithoutDataSourceWarns(t *testing.T) {
	// Without a data source LD rejects window offsets, so warn and don't set them.
	sg := baseMetric("event_user")
	sg.RollupTimeWindow = "custom"
	sg.CustomRollUpStart = float64Ptr(0)
	sg.CustomRollUpEnd = float64Ptr(3)
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.WindowStartOffset != nil || result.LDMetric.WindowEndOffset != nil {
		t.Error("window offsets must not be set without a data source")
	}
	assertHasWarning(t, result.Warnings, "data source")
}

func TestConvert_WarningDailyParticipation(t *testing.T) {
	sg := baseMetric("event_user")
	sg.RollupTimeWindow = "daily_participation_rate"
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHasWarning(t, result.Warnings, "daily participation rate")
}

// --- Warehouse Native data source ---

func TestConvert_WHNativeGlobalDataSource(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	result, err := Convert(sg, Options{LDDataSource: "snowflake-ds"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.DataSource == nil || result.LDMetric.DataSource.Key != "snowflake-ds" {
		t.Errorf("DataSource = %+v, want {Key: snowflake-ds}", result.LDMetric.DataSource)
	}
}

func TestConvert_WHNativeSourceMapping(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	sg.MetricSourceName = "purchases_table"
	opts := Options{
		LDDataSource: "default-ds",
		SourceMapping: map[string]string{
			"purchases_table": "snowflake-purchases-ds",
		},
	}
	result, err := Convert(sg, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.DataSource == nil || result.LDMetric.DataSource.Key != "snowflake-purchases-ds" {
		t.Errorf("DataSource = %+v, want {Key: snowflake-purchases-ds}", result.LDMetric.DataSource)
	}
}

func TestConvert_WHNativeSourceMappingFallback(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	sg.MetricSourceName = "unknown_table"
	opts := Options{
		LDDataSource: "default-ds",
		SourceMapping: map[string]string{
			"other_table": "other-ds",
		},
	}
	result, err := Convert(sg, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.DataSource == nil || result.LDMetric.DataSource.Key != "default-ds" {
		t.Errorf("DataSource = %+v, want {Key: default-ds}", result.LDMetric.DataSource)
	}
}

func TestConvert_WHNativeNoDataSource(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	sg.MetricSourceName = "purchases_table"
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.DataSource != nil {
		t.Errorf("DataSource = %+v, want nil", result.LDMetric.DataSource)
	}
	assertHasWarning(t, result.Warnings, "no LD data source")
}

// --- No event key ---

func TestConvert_NoMetricEvents(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.MetricEvents = nil
	_, err := Convert(sg, Options{})
	if err == nil {
		t.Fatal("expected error for metric with no events")
	}
	if IsIncompatible(err) {
		t.Error("missing events should not be IncompatibleError")
	}
}

// --- Ratio metrics ---

// ratioBaseMetric returns a Statsig ratio referencing two component names.
// Tests supply the actual components via MetricsByName.
func ratioBaseMetric(numName, denName string) *statsig.Metric {
	return &statsig.Metric{
		ID:             "checkout_per_visit::ratio",
		Name:           "checkout_per_visit",
		Type:           "ratio",
		Description:    "Checkouts per visit",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
		MetricComponentMetrics: []statsig.ComponentMetric{
			{Name: numName, Type: "event_count"},
			{Name: denName, Type: "event_count"},
		},
		Tags: []string{"experiment"},
	}
}

func componentMetric(name, typ, eventName string) *statsig.Metric {
	return &statsig.Metric{
		ID:           name + "::" + typ,
		Name:         name,
		Type:         typ,
		UnitTypes:    []string{"userID"},
		MetricEvents: []statsig.MetricEvent{{Name: eventName, Type: "count"}},
	}
}

func TestConvert_Ratio_HappyPath(t *testing.T) {
	num := componentMetric("checkouts", "event_count", "checkout_completed")
	den := componentMetric("visits", "event_count", "page_view")
	sg := ratioBaseMetric("checkouts", "visits")

	result, err := Convert(sg, Options{
		MetricsByName: map[string]*statsig.Metric{
			"checkouts": num,
			"visits":    den,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.EventKey != "checkout_completed" {
		t.Errorf("EventKey = %q, want %q", result.LDMetric.EventKey, "checkout_completed")
	}
	if result.LDMetric.UnitAggregationType != "sum" {
		t.Errorf("numerator UAT = %q, want %q", result.LDMetric.UnitAggregationType, "sum")
	}
	if result.LDMetric.IsNumeric == nil || *result.LDMetric.IsNumeric {
		t.Errorf("IsNumeric = %v, want pointer to false", result.LDMetric.IsNumeric)
	}
	if result.LDMetric.Denominator == nil {
		t.Fatal("Denominator is nil; ratio metric should populate it")
	}
	if result.LDMetric.Denominator.EventName != "page_view" {
		t.Errorf("Denominator.EventName = %q, want %q", result.LDMetric.Denominator.EventName, "page_view")
	}
	if result.LDMetric.Denominator.UnitAggregationType != "sum" {
		t.Errorf("Denominator.UAT = %q, want %q", result.LDMetric.Denominator.UnitAggregationType, "sum")
	}
	if result.LDMetric.Denominator.IsNumeric {
		t.Error("Denominator.IsNumeric = true for event_count component, want false")
	}
}

func TestConvert_Ratio_MixedTermShapes(t *testing.T) {
	// Numerator is a numeric sum metric; denominator is a count-style event.
	num := componentMetric("revenue", "sum", "purchase")
	den := componentMetric("visits", "event_count", "page_view")
	sg := ratioBaseMetric("revenue", "visits")

	result, err := Convert(sg, Options{
		MetricsByName: map[string]*statsig.Metric{
			"revenue": num,
			"visits":  den,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.IsNumeric == nil || !*result.LDMetric.IsNumeric {
		t.Error("numerator IsNumeric should be true for sum component")
	}
	if result.LDMetric.UnitAggregationType != "sum" {
		t.Errorf("numerator UAT = %q, want sum", result.LDMetric.UnitAggregationType)
	}
	if result.LDMetric.Denominator.IsNumeric {
		t.Error("denominator IsNumeric should be false for event_count component")
	}
	if result.LDMetric.Denominator.UnitAggregationType != "sum" {
		t.Errorf("denominator UAT = %q, want sum", result.LDMetric.Denominator.UnitAggregationType)
	}
}

func TestConvert_Ratio_WrongComponentCount(t *testing.T) {
	sg := ratioBaseMetric("a", "b")
	sg.MetricComponentMetrics = []statsig.ComponentMetric{{Name: "only_one", Type: "event_count"}}

	_, err := Convert(sg, Options{
		MetricsByName: map[string]*statsig.Metric{
			"only_one": componentMetric("only_one", "event_count", "ev"),
		},
	})
	if err == nil {
		t.Fatal("expected error for ratio with 1 component")
	}
	if IsIncompatible(err) {
		t.Error("wrong component count should be a hard error, not IncompatibleError")
	}
	if !strings.Contains(err.Error(), "got 1") {
		t.Errorf("error should mention the actual count: %v", err)
	}
}

func TestConvert_Ratio_MissingComponent(t *testing.T) {
	sg := ratioBaseMetric("checkouts", "missing_metric")

	_, err := Convert(sg, Options{
		MetricsByName: map[string]*statsig.Metric{
			"checkouts": componentMetric("checkouts", "event_count", "checkout_completed"),
			// "missing_metric" intentionally absent
		},
	})
	if err == nil {
		t.Fatal("expected error when a component is missing from the lookup")
	}
	if !strings.Contains(err.Error(), "missing_metric") {
		t.Errorf("error should name the missing component: %v", err)
	}
}

func TestConvert_Ratio_NoLookup(t *testing.T) {
	sg := ratioBaseMetric("a", "b")
	_, err := Convert(sg, Options{}) // MetricsByName is nil
	if err == nil {
		t.Fatal("expected error when MetricsByName is nil for a ratio metric")
	}
	if !strings.Contains(err.Error(), "MetricsByName") {
		t.Errorf("error should hint at the missing option: %v", err)
	}
}

func TestConvert_Ratio_OfRatios(t *testing.T) {
	// A ratio component is itself an exotic type for v1.
	num := &statsig.Metric{Name: "inner_ratio", Type: "ratio"}
	den := componentMetric("visits", "event_count", "page_view")
	sg := ratioBaseMetric("inner_ratio", "visits")

	_, err := Convert(sg, Options{
		MetricsByName: map[string]*statsig.Metric{
			"inner_ratio": num,
			"visits":      den,
		},
	})
	if err == nil {
		t.Fatal("expected error for ratio-of-ratios")
	}
	if !IsIncompatible(err) {
		t.Errorf("ratio-of-ratios should surface as IncompatibleError, got %T: %v", err, err)
	}
}

func TestConvert_Ratio_FunnelComponent(t *testing.T) {
	num := componentMetric("checkouts", "event_count", "checkout_completed")
	den := &statsig.Metric{Name: "user_funnel", Type: "funnel"}
	sg := ratioBaseMetric("checkouts", "user_funnel")

	_, err := Convert(sg, Options{
		MetricsByName: map[string]*statsig.Metric{
			"checkouts":   num,
			"user_funnel": den,
		},
	})
	if err == nil {
		t.Fatal("expected error when a ratio component is funnel-typed")
	}
	if !IsIncompatible(err) {
		t.Errorf("funnel component should surface as IncompatibleError, got %T: %v", err, err)
	}
}

// --- Helpers ---

func assertHasWarning(t *testing.T, warnings []string, substr string) {
	t.Helper()
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return
		}
	}
	t.Errorf("expected warning containing %q, got: %v", substr, warnings)
}
