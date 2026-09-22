package converter

import "testing"

// Statsig's custom rollup window counts whole days inclusively, so its end day
// N covers through the END of that day. Statsig's docs: "a cohort window from
// day 1 to day 6 filters to events from 24 hours until 168 hours after
// exposure", and "a 0-6 day window collects data for 7 days because it's
// 0-indexed".
//
// LaunchDarkly's offsets are durations from first exposure, compared as
// ts >= exposure + start AND ts <= exposure + end. So Statsig's end day N is
// LaunchDarkly's (N+1) days. The start bound needs no adjustment: Statsig start
// day N already means N*24h.
func TestApplyCustomWindow_EndDayIsInclusive(t *testing.T) {
	cases := []struct {
		name               string
		start, end         float64
		wantStart, wantEnd int64
	}{
		{"0 to 6 covers seven days", 0, 6, 0, 7 * millisPerDay},
		{"0 to 0 covers the exposure day only", 0, 0, 0, 1 * millisPerDay},
		{"4 to 6 covers days four five and six", 4, 6, 4 * millisPerDay, 7 * millisPerDay},
		{"1 to 6 is 24h through 168h", 1, 6, 1 * millisPerDay, 7 * millisPerDay},
	}
	for _, tc := range cases {
		sg := baseMetric("event_user")
		sg.RollupTimeWindow = "custom"
		sg.CustomRollUpStart = float64Ptr(tc.start)
		sg.CustomRollUpEnd = float64Ptr(tc.end)

		res, err := Convert(sg, Options{LDDataSource: "snowflake-ds"})
		if err != nil {
			t.Fatalf("%s: Convert: %v", tc.name, err)
		}
		if res.LDMetric.WindowStartOffset == nil {
			t.Errorf("%s: WindowStartOffset not set", tc.name)
		} else if got := *res.LDMetric.WindowStartOffset; got != tc.wantStart {
			t.Errorf("%s: WindowStartOffset = %d (%.0f days), want %d (%.0f days)",
				tc.name, got, float64(got)/millisPerDay, tc.wantStart, float64(tc.wantStart)/millisPerDay)
		}
		if res.LDMetric.WindowEndOffset == nil {
			t.Errorf("%s: WindowEndOffset not set", tc.name)
		} else if got := *res.LDMetric.WindowEndOffset; got != tc.wantEnd {
			t.Errorf("%s: WindowEndOffset = %d (%.0f days), want %d (%.0f days)",
				tc.name, got, float64(got)/millisPerDay, tc.wantEnd, float64(tc.wantEnd)/millisPerDay)
		}
	}
}

// The warning shown when no data source is bound should describe the window the
// way Statsig does, so it can be checked against the Statsig metric.
func TestApplyCustomWindow_NoDataSourceWarningNamesStatsigDays(t *testing.T) {
	sg := baseMetric("event_user")
	sg.RollupTimeWindow = "custom"
	sg.CustomRollUpStart = float64Ptr(0)
	sg.CustomRollUpEnd = float64Ptr(6)

	res, err := Convert(sg, Options{})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	assertHasWarning(t, res.LossyReasons, "days 0-6")
}
