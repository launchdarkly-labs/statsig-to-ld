package converter

import (
	"strings"
	"testing"
)

// Statsig's unit-count participation family (warehouse-native "daily_participation"
// and cloud "event_user"); the rollupTimeWindow selects the mode. The rate is the
// DEFAULT: it appears as an unset rollupTimeWindow (confirmed against a live
// cloud metric), "daily" (warehouse-native), or "daily_participation_rate" (cloud
// legacy). Only the explicit "max" (one-time) and "custom" (windowed) rollups are
// a per-unit binary that converts exactly. The rate is lossy in LaunchDarkly (no
// fraction-of-days aggregation).

func dpMetric(rollup string, extra string) string {
	fields := []string{`"aggregation":"daily_participation"`, `"metricSourceName":"Events"`}
	if rollup != "" {
		fields = append(fields, `"rollupTimeWindow":"`+rollup+`"`)
	}
	if extra != "" {
		fields = append(fields, extra)
	}
	return `{
	  "type":"user_warehouse","name":"DP","id":"DP::user_warehouse","directionality":"increase",
	  "unitTypes":["userID"],
	  "warehouseNative":{` + strings.Join(fields, ",") + `}}`
}

func assertBinary(t *testing.T, res *Result) {
	t.Helper()
	// A binary metric is explicitly isNumeric=false (a non-nil *bool), so LD
	// receives "isNumeric": false rather than an omitted field.
	if res.LDMetric.IsNumeric == nil {
		t.Fatal("IsNumeric should be set to false for a binary metric, got nil")
	}
	if *res.LDMetric.IsNumeric {
		t.Error("daily_participation must convert to a non-numeric (binary) metric")
	}
	if res.LDMetric.UnitAggregationType != "average" {
		t.Errorf("UnitAggregationType = %q, want average (binary)", res.LDMetric.UnitAggregationType)
	}
}

func TestConvert_DailyParticipation_Rate_IsLossy(t *testing.T) {
	res := mustConvert(t, dpMetric("daily", ""), Options{LDDataSource: "ds"})
	assertBinary(t, res)
	if !res.IsLossy() {
		t.Errorf("rollupTimeWindow=daily (participation rate) should be lossy; LossyReasons=%v", res.LossyReasons)
	}
	assertHasWarning(t, res.LossyReasons, "rate")
}

func TestConvert_DailyParticipation_OneTime_NotLossy(t *testing.T) {
	res := mustConvert(t, dpMetric("max", ""), Options{LDDataSource: "ds"})
	assertBinary(t, res)
	if res.IsLossy() {
		t.Errorf("rollupTimeWindow=max (one-time, binary) should NOT be lossy; LossyReasons=%v", res.LossyReasons)
	}
}

func TestConvert_DailyParticipation_EmptyRollup_IsLossy(t *testing.T) {
	// Unset rollupTimeWindow is the participation-rate DEFAULT, so it's lossy.
	res := mustConvert(t, dpMetric("", ""), Options{LDDataSource: "ds"})
	assertBinary(t, res)
	if !res.IsLossy() {
		t.Errorf("unset-rollup daily_participation (the rate default) should be lossy; LossyReasons=%v", res.LossyReasons)
	}
	assertHasWarning(t, res.LossyReasons, "rate")
}

// eventUserMetric builds a cloud event_user (participation) metric. An unset
// rollup is the daily-participation-rate default; "max" is one-time.
func eventUserMetric(rollup string) string {
	r := ""
	if rollup != "" {
		r = `"rollupTimeWindow":"` + rollup + `",`
	}
	return `{
	  "type":"event_user","name":"EU","id":"EU::event_user","directionality":"increase",
	  "unitTypes":["userID"],` + r + `
	  "metricEvents":[{"name":"page_view","type":"count"}]}`
}

func TestConvert_EventUser_EmptyRollup_IsLossy(t *testing.T) {
	// A cloud event_user with no rollup is the participation-rate default (live-
	// confirmed: the UI's "Daily Participation Rate" produces an unset rollup).
	res := mustConvert(t, eventUserMetric(""), Options{})
	assertBinary(t, res)
	if !res.IsLossy() {
		t.Errorf("cloud event_user with unset rollup (rate default) should be lossy; LossyReasons=%v", res.LossyReasons)
	}
	assertHasWarning(t, res.LossyReasons, "rate")
}

func TestConvert_EventUser_OneTime_NotLossy(t *testing.T) {
	// "max" = one-time event (live-confirmed): a per-unit binary, converts clean.
	res := mustConvert(t, eventUserMetric("max"), Options{})
	assertBinary(t, res)
	if res.IsLossy() {
		t.Errorf("event_user with rollupTimeWindow=max (one-time) should NOT be lossy; LossyReasons=%v", res.LossyReasons)
	}
}

func TestConvert_DailyParticipation_CustomWindow_WithDataSource_NotLossy(t *testing.T) {
	res := mustConvert(t, dpMetric("custom", `"customRollUpStart":0,"customRollUpEnd":6`), Options{LDDataSource: "ds"})
	assertBinary(t, res)
	if res.IsLossy() {
		t.Errorf("windowed (custom) daily_participation with a data source should NOT be lossy; LossyReasons=%v", res.LossyReasons)
	}
	if res.LDMetric.WindowEndOffset == nil {
		t.Error("custom window should be applied when a data source is bound")
	}
}

func TestConvert_DailyParticipation_CustomWindow_NoDataSource_LossyForWindowOnly(t *testing.T) {
	// Without a data source the window can't apply (that is legitimately lossy),
	// but the loss must be about the WINDOW, not a blanket daily-participation
	// "per-day rate" loss — this is not the rate rollup.
	res := mustConvert(t, dpMetric("custom", `"customRollUpStart":0,"customRollUpEnd":6`), Options{})
	if !res.IsLossy() {
		t.Fatalf("custom window with no data source should be lossy (window dropped)")
	}
	assertHasWarning(t, res.LossyReasons, "window")
	for _, r := range res.LossyReasons {
		if strings.Contains(strings.ToLower(r), "rate") {
			t.Errorf("windowed daily_participation should not carry a rate-loss warning: %q", r)
		}
	}
}
