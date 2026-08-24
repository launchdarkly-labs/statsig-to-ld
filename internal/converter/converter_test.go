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

func TestConvert_EventCount_UsesLineageEvent(t *testing.T) {
	// Built-in event_count metrics carry no metricEvents; the counted event is in
	// lineage.events. The converter should use it and produce a normal count
	// metric, not fail with "no metricEvents".
	sg := &statsig.Metric{
		ID:             "purchase::event_count",
		Name:           "purchase",
		Type:           "event_count",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
		MetricEvents:   nil, // event_count has none
		Lineage:        statsig.Lineage{Events: []string{"purchase"}},
	}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("event_count should convert via lineage.events, got error: %v", err)
	}
	if result.LDMetric.EventKey != "purchase" {
		t.Errorf("EventKey = %q, want \"purchase\" (from lineage.events[0])", result.LDMetric.EventKey)
	}
	if result.LDMetric.IsNumeric == nil || *result.LDMetric.IsNumeric {
		t.Error("event_count should map to a non-numeric count metric")
	}
	if result.LDMetric.UnitAggregationType != "sum" {
		t.Errorf("UnitAggregationType = %q, want \"sum\"", result.LDMetric.UnitAggregationType)
	}
}

func TestConvert_NoEventsNoLineage_Errors(t *testing.T) {
	// With neither metricEvents nor lineage.events, there's no event key to map —
	// still a hard error (not a known-incompatible skip).
	sg := &statsig.Metric{
		ID:             "broken::event_count",
		Name:           "broken",
		Type:           "event_count",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
	}
	_, err := Convert(sg, Options{})
	if err == nil {
		t.Fatal("expected an error when there are no metric events and no lineage events")
	}
	if IsIncompatible(err) {
		t.Error("missing events should be a hard error, not an IncompatibleError skip")
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

// --- Analysis units ---

func TestConvert_AnalysisUnits(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.UnitTypes = []string{"userID", "companyID"}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	units := result.LDMetric.AnalysisUnits
	if len(units) != 2 || units[0] != "user" || units[1] != "companyid" {
		t.Errorf("AnalysisUnits = %v, want [user companyid]", units)
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

	units := result.LDMetric.AnalysisUnits
	if len(units) != 2 || units[0] != "user" || units[1] != "company" {
		t.Errorf("AnalysisUnits = %v, want [user company]", units)
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
	if got := result.LDMetric.AnalysisUnits; len(got) != 1 || got[0] != "anonymousUser" {
		t.Errorf("AnalysisUnits = %v, want [anonymousUser]", got)
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
	if got := result.LDMetric.AnalysisUnits; len(got) != 1 || got[0] != "exact-match" {
		t.Errorf("AnalysisUnits = %v, want [exact-match]", got)
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
//
// Statsig (cloud) ratio metrics carry the numerator and denominator inline as
// metricEvents[0] and metricEvents[1]. They do NOT populate
// metricComponentMetrics (that field is for composite metrics), and Statsig
// rejects a ratio defined that way (HTTP 400 "Metric event is empty"). Each
// event's Type is its aggregation ("count", "count_distinct", "value",
// "metadata").

// ratioMetric builds a Statsig cloud ratio in the order Statsig stores them:
// metricEvents[0] = denominator, metricEvents[1] = numerator. Params stay
// (numerator, denominator) for readable call sites.
func ratioMetric(numEvent, numType, denEvent, denType string) *statsig.Metric {
	return &statsig.Metric{
		ID:             "checkout_per_visit::ratio",
		Name:           "checkout_per_visit",
		Type:           "ratio",
		Description:    "Checkouts per visit",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
		MetricEvents: []statsig.MetricEvent{
			{Name: denEvent, Type: denType}, // index 0 = denominator
			{Name: numEvent, Type: numType}, // index 1 = numerator
		},
		Tags: []string{"experiment"},
	}
}

func TestConvert_Ratio_NumeratorIsSecondEvent(t *testing.T) {
	// A cloud ratio is positional, with no explicit numerator/denominator field:
	// metricEvents[0] is the DENOMINATOR and metricEvents[1] is the NUMERATOR.
	// This ratio is "checkout_completed per page_view" — numerator =
	// checkout_completed (index 1), denominator = page_view (index 0). Built
	// inline (not via the helper) so it independently pins the direction.
	sg := &statsig.Metric{
		ID:             "checkouts_per_visit::ratio",
		Name:           "checkouts_per_visit",
		Type:           "ratio",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
		MetricEvents: []statsig.MetricEvent{
			{Name: "page_view", Type: "count"},          // index 0 = denominator
			{Name: "checkout_completed", Type: "count"}, // index 1 = numerator
		},
	}

	result, err := Convert(sg, Options{LDDataSource: "snowflake"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.EventKey != "checkout_completed" {
		t.Errorf("numerator (EventKey) = %q, want checkout_completed (metricEvents[1])", result.LDMetric.EventKey)
	}
	if result.LDMetric.Denominator == nil || result.LDMetric.Denominator.EventName != "page_view" {
		t.Errorf("denominator EventName = %+v, want page_view (metricEvents[0])", result.LDMetric.Denominator)
	}
}

func TestConvert_Ratio_ConversionRate(t *testing.T) {
	// purchases / page_views — both count aggregations (a conversion rate).
	sg := ratioMetric("checkout_completed", "count", "page_view", "count")

	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.Key != "checkout-per-visit-ratio" {
		t.Errorf("Key = %q, want %q", result.LDMetric.Key, "checkout-per-visit-ratio")
	}
	if result.LDMetric.EventKey != "checkout_completed" {
		t.Errorf("EventKey = %q, want %q", result.LDMetric.EventKey, "checkout_completed")
	}
	if result.LDMetric.IsNumeric == nil || *result.LDMetric.IsNumeric {
		t.Errorf("numerator IsNumeric = %v, want pointer to false", result.LDMetric.IsNumeric)
	}
	if result.LDMetric.UnitAggregationType != "sum" {
		t.Errorf("numerator UAT = %q, want %q", result.LDMetric.UnitAggregationType, "sum")
	}
	if result.LDMetric.SuccessCriteria != "HigherThanBaseline" {
		t.Errorf("SuccessCriteria = %q, want HigherThanBaseline", result.LDMetric.SuccessCriteria)
	}
	if got := result.LDMetric.AnalysisUnits; len(got) != 1 || got[0] != "user" {
		t.Errorf("AnalysisUnits = %v, want [user]", got)
	}
	if result.LDMetric.Denominator == nil {
		t.Fatal("Denominator is nil; ratio metric should populate it")
	}
	if result.LDMetric.Denominator.EventName != "page_view" {
		t.Errorf("Denominator.EventName = %q, want %q", result.LDMetric.Denominator.EventName, "page_view")
	}
	if result.LDMetric.Denominator.IsNumeric {
		t.Error("Denominator.IsNumeric = true for count event, want false")
	}
	if result.LDMetric.Denominator.UnitAggregationType != "sum" {
		t.Errorf("Denominator.UAT = %q, want %q", result.LDMetric.Denominator.UnitAggregationType, "sum")
	}
}

func TestConvert_Ratio_NumericNumerator(t *testing.T) {
	// revenue (value) / page_views (count): numerator numeric, denominator not.
	sg := ratioMetric("purchase", "value", "page_view", "count")

	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.IsNumeric == nil || !*result.LDMetric.IsNumeric {
		t.Error("numerator IsNumeric should be true for a value aggregation")
	}
	if result.LDMetric.UnitAggregationType != "sum" {
		t.Errorf("numerator UAT = %q, want sum", result.LDMetric.UnitAggregationType)
	}
	if result.LDMetric.Unit != "units" {
		t.Errorf("Unit = %q, want default %q for numeric numerator", result.LDMetric.Unit, "units")
	}
	if result.LDMetric.Denominator.IsNumeric {
		t.Error("denominator IsNumeric should be false for a count aggregation")
	}
	if result.LDMetric.Denominator.UnitAggregationType != "sum" {
		t.Errorf("denominator UAT = %q, want sum", result.LDMetric.Denominator.UnitAggregationType)
	}
}

func TestConvert_Ratio_WrongEventCount(t *testing.T) {
	sg := ratioMetric("checkout_completed", "count", "page_view", "count")
	sg.MetricEvents = sg.MetricEvents[:1] // only a numerator

	_, err := Convert(sg, Options{})
	if err == nil {
		t.Fatal("expected error for ratio with 1 metric event")
	}
	if IsIncompatible(err) {
		t.Error("wrong event count should be a hard error, not IncompatibleError")
	}
	if !strings.Contains(err.Error(), "got 1") {
		t.Errorf("error should mention the actual count: %v", err)
	}
}

func TestConvert_Ratio_EmptyEventName(t *testing.T) {
	sg := ratioMetric("", "count", "page_view", "count") // numerator event has no name

	_, err := Convert(sg, Options{})
	if err == nil {
		t.Fatal("expected error when the numerator event has an empty name")
	}
	if !strings.Contains(err.Error(), "numerator") {
		t.Errorf("error should identify the numerator: %v", err)
	}
}

func TestConvert_Ratio_CountDistinctNumerator(t *testing.T) {
	// LD ratio terms support count(distinct <field>) natively — it maps to
	// unitAggregationType=count_distinct + unitAggregationField, non-numeric,
	// and (unlike the simple-metric path) without a data-loss warning.
	sg := ratioMetric("add_to_cart", "count_distinct", "page_view", "count")
	sg.MetricEvents[1].MetadataKey = "item_category" // numerator = metricEvents[1]

	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.UnitAggregationType != "count_distinct" {
		t.Errorf("numerator UAT = %q, want count_distinct", result.LDMetric.UnitAggregationType)
	}
	if result.LDMetric.UnitAggregationField != "item_category" {
		t.Errorf("numerator UnitAggregationField = %q, want item_category", result.LDMetric.UnitAggregationField)
	}
	if result.LDMetric.IsNumeric == nil || *result.LDMetric.IsNumeric {
		t.Error("count_distinct numerator must be non-numeric")
	}
	for _, w := range result.Warnings {
		if strings.Contains(strings.ToLower(w), "count all occurrences") {
			t.Errorf("ratio count_distinct must not warn about counting all occurrences, got: %q", w)
		}
	}
}

func TestConvert_Ratio_CountDistinctDenominator(t *testing.T) {
	sg := ratioMetric("purchase", "count", "session_start", "count_distinct")
	sg.MetricEvents[0].MetadataKey = "session_id" // denominator = metricEvents[0]

	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.Denominator.UnitAggregationType != "count_distinct" {
		t.Errorf("denominator UAT = %q, want count_distinct", result.LDMetric.Denominator.UnitAggregationType)
	}
	if result.LDMetric.Denominator.UnitAggregationField != "session_id" {
		t.Errorf("denominator UnitAggregationField = %q, want session_id", result.LDMetric.Denominator.UnitAggregationField)
	}
	if result.LDMetric.Denominator.IsNumeric {
		t.Error("count_distinct denominator must be non-numeric")
	}
}

func TestConvert_Ratio_CountDistinctNoColumnIsBinary(t *testing.T) {
	// A cloud ratio's count_distinct event carries no column — it counts distinct
	// units (users). The LaunchDarkly equivalent is a binary metric: non-numeric,
	// average aggregation (== count distinct of the analysis unit). Faithful, so
	// no warning.
	sg := ratioMetric("purchase", "count_distinct", "page_view", "count")
	// numerator MetadataKey intentionally empty, like a real cloud ratio

	result, err := Convert(sg, Options{LDDataSource: "snowflake-staging"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.UnitAggregationType != "average" {
		t.Errorf("numerator UAT = %q, want average (binary metric)", result.LDMetric.UnitAggregationType)
	}
	if result.LDMetric.UnitAggregationField != "" {
		t.Errorf("UnitAggregationField should be empty for a binary term, got %q", result.LDMetric.UnitAggregationField)
	}
	if result.LDMetric.IsNumeric == nil || *result.LDMetric.IsNumeric {
		t.Error("binary term must be non-numeric")
	}
	for _, w := range result.Warnings {
		if strings.Contains(strings.ToLower(w), "distinct") {
			t.Errorf("count-distinct-of-users maps faithfully to a binary metric; should not warn, got: %q", w)
		}
	}
}

func TestConvert_Ratio_NoDataSourceWarns(t *testing.T) {
	// LD requires a warehouse data source on ratio metrics (the API rejects a
	// ratio without one: HTTP 400 "Ratio metrics require a warehouse data
	// source"). Converting a ratio with no resolvable source should warn at
	// convert time — visible in dry-run — not silently produce a metric LD
	// rejects at create time.
	sg := ratioMetric("checkout_completed", "count", "page_view", "count")

	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHasWarning(t, result.Warnings, "data source")
	if result.LDMetric.DataSource != nil {
		t.Errorf("DataSource should be nil when none resolved, got %+v", result.LDMetric.DataSource)
	}
}

func TestConvert_Ratio_WithDataSourceNoWarning(t *testing.T) {
	// When --ld-data-source supplies a source, bind it and do not warn.
	sg := ratioMetric("checkout_completed", "count", "page_view", "count")

	result, err := Convert(sg, Options{LDDataSource: "snowflake-staging"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.DataSource == nil || result.LDMetric.DataSource.Key != "snowflake-staging" {
		t.Errorf("DataSource = %+v, want key snowflake-staging", result.LDMetric.DataSource)
	}
	for _, w := range result.Warnings {
		if strings.Contains(strings.ToLower(w), "data source") {
			t.Errorf("should not warn about data source when one is provided, got: %q", w)
		}
	}
}

// --- Lossy classification (drives the default skip; --convert-lossy overrides) ---

func TestConvert_Lossy_DailyParticipation(t *testing.T) {
	sg := baseMetric("event_user")
	sg.RollupTimeWindow = "daily_participation_rate"
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsLossy() {
		t.Error("daily participation rate should mark the conversion lossy")
	}
	assertHasWarning(t, result.LossyReasons, "daily participation")
}

func TestConvert_Lossy_Capping(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	sg.WarehouseNative = &statsig.WarehouseNative{Cap: float64Ptr(500)}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsLossy() {
		t.Error("per-unit capping should mark the conversion lossy")
	}
}

func TestConvert_Lossy_EventFilters(t *testing.T) {
	sg := baseMetric("event_count_custom")
	sg.MetricEvents[0].Criteria = []statsig.Criterion{
		{Type: "metadata", Column: "country", Condition: "=", Values: []string{"US"}},
	}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsLossy() {
		t.Error("dropped event filter criteria should mark the conversion lossy")
	}
}

func TestConvert_NotLossy_AdvisoryUnitType(t *testing.T) {
	// A non-standard unit type is an advisory warning, not a lossy conversion —
	// the metric still converts faithfully.
	sg := baseMetric("event_count_custom")
	sg.UnitTypes = []string{"userID", "companyID"}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected an advisory warning about companyID")
	}
	if result.IsLossy() {
		t.Errorf("advisory unit-type warning must not be lossy; LossyReasons = %v", result.LossyReasons)
	}
}

func TestConvert_NotLossy_Clean(t *testing.T) {
	result, err := Convert(baseMetric("event_count_custom"), Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsLossy() {
		t.Errorf("clean conversion must not be lossy; LossyReasons = %v", result.LossyReasons)
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

// --- Warehouse-native routing ---

func TestConvert_WarehouseNativeSum_RoutesByAggregation(t *testing.T) {
	// Top-level type is user_warehouse; the real shape is in warehouseNative.
	sg := &statsig.Metric{
		ID:             "revenue::user_warehouse",
		Name:           "revenue",
		Type:           "user_warehouse",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
		WarehouseNative: &statsig.WarehouseNative{
			Aggregation:   "sum",
			MetricSources: []statsig.MetricSource{{MetricSourceName: "purchase_src", ValueColumn: "price_usd"}},
		},
	}
	result, err := Convert(sg, Options{SourceMapping: map[string]string{"purchase_src": "ld-purchase"}})
	if err != nil {
		t.Fatalf("unexpected error (WN sum should route via aggregation, not error as unknown type): %v", err)
	}
	if result.LDMetric.UnitAggregationType != "sum" {
		t.Errorf("UnitAggregationType = %q, want \"sum\"", result.LDMetric.UnitAggregationType)
	}
	if result.LDMetric.EventKey != "price_usd" {
		t.Errorf("EventKey = %q, want \"price_usd\"", result.LDMetric.EventKey)
	}
	if result.LDMetric.DataSource == nil || result.LDMetric.DataSource.Key != "ld-purchase" {
		t.Errorf("DataSource = %+v, want key ld-purchase", result.LDMetric.DataSource)
	}
}

func TestConvert_WarehouseNativeRatio_PerTermSources(t *testing.T) {
	sg := &statsig.Metric{
		ID:             "rev-per-visitor::user_warehouse",
		Name:           "rev-per-visitor",
		Type:           "user_warehouse",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
		WarehouseNative: &statsig.WarehouseNative{
			Aggregation:                 "ratio",
			MetricSources:               []statsig.MetricSource{{MetricSourceName: "purchase_src", ValueColumn: "price_usd"}},
			NumeratorAggregation:        "sum",
			DenominatorMetricSourceName: "visitor_src",
			DenominatorAggregation:      "count",
		},
	}
	result, err := Convert(sg, Options{SourceMapping: map[string]string{
		"purchase_src": "ld-purchase",
		"visitor_src":  "ld-visitor",
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.DataSource == nil || result.LDMetric.DataSource.Key != "ld-purchase" {
		t.Errorf("numerator DataSource = %+v, want key ld-purchase", result.LDMetric.DataSource)
	}
	if result.LDMetric.Denominator == nil {
		t.Fatalf("Denominator is nil, want a denominator term")
	}
	if result.LDMetric.Denominator.DataSource == nil || result.LDMetric.Denominator.DataSource.Key != "ld-visitor" {
		t.Errorf("denominator DataSource = %+v, want key ld-visitor (independent of numerator)", result.LDMetric.Denominator.DataSource)
	}
}

func TestMetric_WarehouseNativeHelpers(t *testing.T) {
	wn := &statsig.Metric{Type: "user_warehouse", WarehouseNative: &statsig.WarehouseNative{
		Aggregation:   "count_distinct",
		MetricSources: []statsig.MetricSource{{MetricSourceName: "src_a"}},
	}}
	if !wn.IsWarehouseNative() {
		t.Error("IsWarehouseNative() = false, want true")
	}
	if wn.EffectiveType() != "count_distinct" {
		t.Errorf("EffectiveType() = %q, want \"count_distinct\"", wn.EffectiveType())
	}
	if wn.NumeratorSourceName() != "src_a" {
		t.Errorf("NumeratorSourceName() = %q, want \"src_a\"", wn.NumeratorSourceName())
	}

	cloud := &statsig.Metric{Type: "sum", MetricSourceName: "top_src"}
	if cloud.IsWarehouseNative() {
		t.Error("IsWarehouseNative() = true for cloud metric, want false")
	}
	if cloud.EffectiveType() != "sum" {
		t.Errorf("EffectiveType() = %q, want \"sum\"", cloud.EffectiveType())
	}
	if cloud.NumeratorSourceName() != "top_src" {
		t.Errorf("NumeratorSourceName() = %q, want \"top_src\"", cloud.NumeratorSourceName())
	}
}

func TestConvert_WarehouseNativeNoAggregation_ExplicitError(t *testing.T) {
	// type user_warehouse but no aggregation (parsing gap / incomplete metric):
	// should fail with a clear error, not a generic "unknown type", and not be
	// treated as a known-incompatible skip.
	sg := &statsig.Metric{
		ID:              "broken::user_warehouse",
		Name:            "broken",
		Type:            "user_warehouse",
		Directionality:  "increase",
		UnitTypes:       []string{"userID"},
		WarehouseNative: &statsig.WarehouseNative{}, // Aggregation == ""
	}
	_, err := Convert(sg, Options{})
	if err == nil {
		t.Fatal("expected an error for warehouse-native metric with no aggregation")
	}
	if IsIncompatible(err) {
		t.Errorf("should be a hard error, not an IncompatibleError skip: %v", err)
	}
	if !strings.Contains(err.Error(), "no aggregation") {
		t.Errorf("error should mention the missing aggregation, got: %v", err)
	}
}

func TestConvert_WarehouseNativeDailyParticipation_Lossy(t *testing.T) {
	// A warehouse-native daily-participation-RATE metric (rollupTimeWindow
	// "daily") converts as a binary approximation but is marked lossy — skipped
	// by default, --convert-lossy converts it. It must NOT be a hard
	// error/incompatible. (Other unit-count rollups are binary and not lossy.)
	sg := &statsig.Metric{
		ID:             "engagement::user_warehouse",
		Name:           "engagement",
		Type:           "user_warehouse",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
		WarehouseNative: &statsig.WarehouseNative{
			Aggregation:      "daily_participation",
			RollupTimeWindow: "daily",
			MetricSources:    []statsig.MetricSource{{MetricSourceName: "events_src", ValueColumn: "active"}},
		},
	}
	result, err := Convert(sg, Options{LDDataSource: "ds-key"})
	if err != nil {
		t.Fatalf("daily_participation should convert (lossy), not error: %v", err)
	}
	if !result.IsLossy() {
		t.Error("daily-participation-rate conversion should be marked lossy")
	}
	assertHasWarning(t, result.LossyReasons, "participation rate")
	if result.LDMetric.IsNumeric == nil || *result.LDMetric.IsNumeric {
		t.Errorf("expected non-numeric (binary), got IsNumeric=%v", result.LDMetric.IsNumeric)
	}
	if result.LDMetric.UnitAggregationType != "average" {
		t.Errorf("UnitAggregationType = %q, want \"average\"", result.LDMetric.UnitAggregationType)
	}
}

// --- Ratio winsorization + windowing ---

func ratioWithWindowAndWinsor() *statsig.Metric {
	return &statsig.Metric{
		ID:             "rev-ratio::ratio",
		Name:           "rev-ratio",
		Type:           "ratio",
		Directionality: "increase",
		UnitTypes:      []string{"userID"},
		MetricEvents: []statsig.MetricEvent{
			{Name: "add_to_cart", Type: "count"}, // [0] = denominator (per inversion fix)
			{Name: "purchase", Type: "value"},    // [1] = numerator (numeric)
		},
		RollupTimeWindow:  "custom",
		CustomRollUpStart: float64Ptr(0),
		CustomRollUpEnd:   float64Ptr(7),
		WarehouseNative:   &statsig.WarehouseNative{WinsorizationLow: float64Ptr(0.01), WinsorizationHigh: float64Ptr(0.99)},
	}
}

func TestConvertRatio_WinsorizationAndWindow(t *testing.T) {
	sg := ratioWithWindowAndWinsor()
	result, err := Convert(sg, Options{LDDataSource: "ds-key"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ld := result.LDMetric
	// Winsorization mapped to numerator (0–1 → 0–100).
	if ld.WinsorLowerPercentile == nil || *ld.WinsorLowerPercentile != 1 {
		t.Errorf("WinsorLowerPercentile = %v, want 1", ld.WinsorLowerPercentile)
	}
	if ld.WinsorUpperPercentile == nil || *ld.WinsorUpperPercentile != 99 {
		t.Errorf("WinsorUpperPercentile = %v, want 99", ld.WinsorUpperPercentile)
	}
	// Window offsets set because a data source is bound (days → ms).
	if ld.WindowStartOffset == nil || *ld.WindowStartOffset != 0 {
		t.Errorf("WindowStartOffset = %v, want 0", ld.WindowStartOffset)
	}
	if ld.WindowEndOffset == nil || *ld.WindowEndOffset != int64(7*millisPerDay) {
		t.Errorf("WindowEndOffset = %v, want %d", ld.WindowEndOffset, int64(7*millisPerDay))
	}
}

func TestConvertRatio_WindowWithoutDataSourceWarns(t *testing.T) {
	sg := ratioWithWindowAndWinsor()
	// No LDDataSource and no source mapping → no data source → window not applied.
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.WindowStartOffset != nil || result.LDMetric.WindowEndOffset != nil {
		t.Errorf("window offsets should be unset without a data source, got start=%v end=%v",
			result.LDMetric.WindowStartOffset, result.LDMetric.WindowEndOffset)
	}
	assertHasWarning(t, result.Warnings, "custom rollup window")
}
