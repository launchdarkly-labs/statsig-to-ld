package converter

import (
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
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
	if ld.Unit != "TODO" {
		t.Errorf("Unit = %q, want \"TODO\"", ld.Unit)
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
	incompatible := []string{"ratio", "funnel", "composite", "composite_sum", "percentile", "user"}
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

func TestConvert_DefaultUnitTODO(t *testing.T) {
	sg := baseMetric("sum")
	sg.MetricEvents[0] = statsig.MetricEvent{Name: "purchase", Type: "value"}
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LDMetric.Unit != "TODO" {
		t.Errorf("Unit = %q, want \"TODO\"", result.LDMetric.Unit)
	}
	assertHasWarning(t, result.Warnings, "TODO")
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

func TestConvert_WarningWinsorization(t *testing.T) {
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
	assertHasWarning(t, result.Warnings, "winsorization")
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

func TestConvert_WarningCustomWindow(t *testing.T) {
	sg := baseMetric("event_user")
	sg.RollupTimeWindow = "custom"
	sg.CustomRollUpStart = float64Ptr(0)
	sg.CustomRollUpEnd = float64Ptr(3)
	result, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertHasWarning(t, result.Warnings, "custom rollup window")
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
