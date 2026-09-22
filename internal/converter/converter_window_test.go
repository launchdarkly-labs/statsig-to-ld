package converter

import "testing"

// Statsig window days are inclusive, so end day N is N+1 days in LD.
func TestApplyCustomWindow_EndDayIsInclusive(t *testing.T) {
	cases := []struct {
		name               string
		start, end         float64
		wantStart, wantEnd int64
	}{
		{"0-6 is 7 days", 0, 6, 0, 7 * millisPerDay},
		{"0-0 is 1 day", 0, 0, 0, 1 * millisPerDay},
		{"4-6 is 3 days", 4, 6, 4 * millisPerDay, 7 * millisPerDay},
		{"1-6 is 24h to 168h", 1, 6, 1 * millisPerDay, 7 * millisPerDay},
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
