package report

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

func TestFinalize_Empty(t *testing.T) {
	r := New()
	r.Finalize(0)

	if r.StatsigMetricsTotal != 0 {
		t.Errorf("StatsigMetricsTotal = %d, want 0", r.StatsigMetricsTotal)
	}
	if r.Converted != 0 || r.SkippedExisting != 0 || r.SkippedIncompatible != 0 || r.Failed != 0 {
		t.Errorf("expected all counts 0, got converted=%d existing=%d incompat=%d failed=%d",
			r.Converted, r.SkippedExisting, r.SkippedIncompatible, r.Failed)
	}
	if r.Timestamp == "" {
		t.Error("Timestamp should be set after Finalize")
	}
}

func TestFinalize_MixedStatuses(t *testing.T) {
	r := New()
	r.AddConverted("m1", "sum", "m1::sum", "m1-sum", "proj", nil, Diagnostics{})
	r.AddConverted("m2", "mean", "m2::mean", "m2-mean", "proj", []string{"unit TODO"}, Diagnostics{})
	r.AddConverted("m3", "sum", "m3::sum", "m3-sum", "proj", []string{"warn1", "warn2"}, Diagnostics{})
	r.AddSkippedExisting("m4", "sum", "m4::sum", "m4-sum", "proj")
	r.AddSkippedIncompatible("m5", "ratio", "m5::ratio", "not supported")
	r.AddSkippedIncompatible("m6", "funnel", "m6::funnel", "needs metric group")
	r.AddFailed("m7", "sum", "m7::sum", "API error")

	r.Finalize(7)

	if r.StatsigMetricsTotal != 7 {
		t.Errorf("StatsigMetricsTotal = %d, want 7", r.StatsigMetricsTotal)
	}
	if r.Converted != 3 {
		t.Errorf("Converted = %d, want 3", r.Converted)
	}
	if r.ConvertedWithWarn != 2 {
		t.Errorf("ConvertedWithWarn = %d, want 2", r.ConvertedWithWarn)
	}
	if r.SkippedExisting != 1 {
		t.Errorf("SkippedExisting = %d, want 1", r.SkippedExisting)
	}
	if r.SkippedIncompatible != 2 {
		t.Errorf("SkippedIncompatible = %d, want 2", r.SkippedIncompatible)
	}
	if r.Failed != 1 {
		t.Errorf("Failed = %d, want 1", r.Failed)
	}
	if len(r.Metrics) != 7 {
		t.Errorf("len(Metrics) = %d, want 7", len(r.Metrics))
	}
}

func TestFinalize_SkippedLossy(t *testing.T) {
	r := New()
	r.AddSkippedLossy("dp", "event_user", "dp::event_user", []string{"daily participation rate not supported"}, Diagnostics{})
	r.Finalize(1)

	if r.SkippedLossy != 1 {
		t.Errorf("SkippedLossy = %d, want 1", r.SkippedLossy)
	}
	if len(r.Metrics) != 1 || r.Metrics[0].Status != StatusSkippedLossy {
		t.Fatalf("expected one %s metric, got %+v", StatusSkippedLossy, r.Metrics)
	}
	if len(r.Metrics[0].Warnings) != 1 {
		t.Errorf("expected the lossy reason recorded as a warning, got %v", r.Metrics[0].Warnings)
	}
	if r.Converted != 0 {
		t.Errorf("Converted = %d, want 0 (a lossy-skipped metric is not converted)", r.Converted)
	}
}

func TestFinalize_AllConverted(t *testing.T) {
	r := New()
	r.AddConverted("m1", "sum", "m1::sum", "m1-sum", "proj", nil, Diagnostics{})
	r.AddConverted("m2", "mean", "m2::mean", "m2-mean", "proj", nil, Diagnostics{})
	r.Finalize(2)

	if r.Converted != 2 || r.ConvertedWithWarn != 0 {
		t.Errorf("Converted=%d ConvertedWithWarn=%d, want 2/0", r.Converted, r.ConvertedWithWarn)
	}
}

func TestFinalize_ByTypeBreakdown(t *testing.T) {
	r := New()
	// Two sum metrics: one clean, one with warnings.
	r.AddConverted("s1", "sum", "s1::sum", "s1-sum", "proj", nil, Diagnostics{})
	r.AddConverted("s2", "sum", "s2::sum", "s2-sum", "proj", []string{"no data source"}, Diagnostics{})
	// A mean metric that failed.
	r.AddFailed("m1", "mean", "m1::mean", "API 500")
	// Two percentile metrics, both incompatible. The type recorded is the
	// effective Statsig type ("percentile"), which the caller resolves from a
	// warehouse-native metric's aggregation rather than the "user_warehouse"
	// wrapper — so the breakdown groups by what actually matters.
	r.AddSkippedIncompatible("p1", "percentile", "p1::user_warehouse", "not supported")
	r.AddSkippedIncompatible("p2", "percentile", "p2::user_warehouse", "not supported")

	r.Finalize(5)

	if len(r.ByType) != 3 {
		t.Fatalf("ByType should have 3 types (sum, mean, percentile), got %d: %+v", len(r.ByType), r.ByType)
	}
	if sum := r.ByType["sum"]; sum == nil || sum.Total != 2 || sum.Converted != 2 || sum.ConvertedWithWarn != 1 {
		t.Errorf("sum breakdown = %+v, want total=2 converted=2 withWarn=1", sum)
	}
	if mean := r.ByType["mean"]; mean == nil || mean.Total != 1 || mean.Failed != 1 || mean.Converted != 0 {
		t.Errorf("mean breakdown = %+v, want total=1 failed=1 converted=0", mean)
	}
	if pct := r.ByType["percentile"]; pct == nil || pct.Total != 2 || pct.SkippedIncompatible != 2 {
		t.Errorf("percentile breakdown = %+v, want total=2 skippedIncompatible=2", pct)
	}
	// Per-type counts must reconcile with the top-level totals.
	if r.Converted != 2 || r.Failed != 1 || r.SkippedIncompatible != 2 {
		t.Errorf("top-level totals drifted from per-type: converted=%d failed=%d incompat=%d",
			r.Converted, r.Failed, r.SkippedIncompatible)
	}
}

func TestWriteCSV(t *testing.T) {
	r := New()
	r.AddConverted("rev", "sum", "rev::sum", "rev-sum", "proj", []string{"warn1", "warn2"}, Diagnostics{})
	r.AddSkippedIncompatible("rate", "ratio", "rate::ratio", "not supported")
	r.Finalize(2)

	var buf bytes.Buffer
	if err := r.WriteCSV(&buf); err != nil {
		t.Fatalf("WriteCSV error: %v", err)
	}

	reader := csv.NewReader(strings.NewReader(buf.String()))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("CSV parse error: %v", err)
	}

	// Header + 2 data rows
	if len(records) != 3 {
		t.Fatalf("expected 3 CSV rows (header + 2 data), got %d", len(records))
	}

	// Check header
	if records[0][0] != "statsig_name" {
		t.Errorf("header[0] = %q, want \"statsig_name\"", records[0][0])
	}

	// Check first data row
	if records[1][0] != "rev" || records[1][3] != "converted" {
		t.Errorf("row 1: name=%q status=%q, want rev/converted", records[1][0], records[1][3])
	}
	// Warnings joined with "; "
	if records[1][6] != "warn1; warn2" {
		t.Errorf("row 1 warnings = %q, want \"warn1; warn2\"", records[1][6])
	}

	// Check second data row
	if records[2][0] != "rate" || records[2][3] != "skipped_incompatible" {
		t.Errorf("row 2: name=%q status=%q, want rate/skipped_incompatible", records[2][0], records[2][3])
	}
	if records[2][7] != "not supported" {
		t.Errorf("row 2 reason = %q, want \"not supported\"", records[2][7])
	}
}

func TestWriteCSV_Empty(t *testing.T) {
	r := New()
	r.Finalize(0)

	var buf bytes.Buffer
	if err := r.WriteCSV(&buf); err != nil {
		t.Fatalf("WriteCSV error: %v", err)
	}

	reader := csv.NewReader(strings.NewReader(buf.String()))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("CSV parse error: %v", err)
	}

	// Header only
	if len(records) != 1 {
		t.Fatalf("expected 1 CSV row (header only), got %d", len(records))
	}
}

func TestPrintSummaryTable(t *testing.T) {
	r := New()
	r.AddConverted("m1", "sum", "m1::sum", "m1-sum", "proj", []string{"warn"}, Diagnostics{})
	r.AddSkippedIncompatible("m2", "ratio", "m2::ratio", "not supported")
	r.Finalize(2)

	var buf bytes.Buffer
	r.PrintSummaryTable(&buf)
	output := buf.String()

	if !strings.Contains(output, "Migration Summary") {
		t.Error("summary table should contain 'Migration Summary'")
	}
	if !strings.Contains(output, "2") {
		t.Error("summary table should contain total count")
	}
	if !strings.Contains(output, "with warnings") {
		t.Error("summary table should show 'with warnings' when there are warnings")
	}
}

func TestPrintSummaryTable_ByType(t *testing.T) {
	r := New()
	r.AddConverted("s1", "sum", "s1::sum", "s1-sum", "proj", nil, Diagnostics{})
	r.AddSkippedIncompatible("p1", "percentile", "p1::user_warehouse", "not supported")
	r.AddSkippedIncompatible("p2", "percentile", "p2::user_warehouse", "not supported")
	r.Finalize(3)

	var buf bytes.Buffer
	r.PrintSummaryTable(&buf)
	output := buf.String()

	if !strings.Contains(output, "By metric type") {
		t.Errorf("summary should include a per-type breakdown section; got:\n%s", output)
	}
	// Both types should be named, so the reader sees the incompatible driver.
	if !strings.Contains(output, "percentile") || !strings.Contains(output, "sum") {
		t.Errorf("per-type table should list each effective type; got:\n%s", output)
	}
}

func TestPrintSummaryTable_DryRun(t *testing.T) {
	r := New()
	r.DryRun = true
	r.AddConverted("m1", "sum", "m1::sum", "m1-sum", "proj", nil, Diagnostics{})
	r.Finalize(1)

	var buf bytes.Buffer
	r.PrintSummaryTable(&buf)
	output := buf.String()

	if !strings.Contains(output, "Would convert") {
		t.Errorf("dry-run summary should say 'Would convert', not 'Converted'; got:\n%s", output)
	}
	if !strings.Contains(strings.ToLower(output), "dry run") {
		t.Errorf("dry-run summary should be labeled as a dry run; got:\n%s", output)
	}
}

func TestPrintSummaryTable_ListsFailures(t *testing.T) {
	r := New()
	r.AddConverted("ok1", "sum", "ok1::sum", "ok1-sum", "proj", nil, Diagnostics{})
	r.AddFailed("boom", "sum", "boom::sum", "LD API returned HTTP 500")
	r.Finalize(2)

	var buf bytes.Buffer
	r.PrintSummaryTable(&buf)
	output := buf.String()

	if !strings.Contains(output, "boom") {
		t.Errorf("summary should name the failed metric 'boom'; got:\n%s", output)
	}
	if !strings.Contains(output, "LD API returned HTTP 500") {
		t.Errorf("summary should show the failure reason; got:\n%s", output)
	}
}

func TestNilWarningsVsEmpty(t *testing.T) {
	r := New()
	r.AddConverted("m1", "sum", "m1::sum", "m1-sum", "proj", nil, Diagnostics{})
	r.AddConverted("m2", "sum", "m2::sum", "m2-sum", "proj", []string{}, Diagnostics{})

	// Both nil and empty should result in 0 ConvertedWithWarn
	r.Finalize(2)
	if r.ConvertedWithWarn != 0 {
		t.Errorf("ConvertedWithWarn = %d, want 0 (nil and empty warnings should not count)", r.ConvertedWithWarn)
	}
}

// A skipped-lossy entry used to store only its lossy reasons, which threw away
// every advisory warning on the metric. Those are exactly the metrics most likely
// to need triage, so the full list has to survive alongside the lossy subset.
func TestAddSkippedLossy_KeepsFullWarningsAndLossySubset(t *testing.T) {
	r := New()
	r.AddSkippedLossy("dp", "daily_participation", "dp::user_warehouse",
		[]string{"lossy: rate approximated", "advisory: unit defaulted to user"},
		Diagnostics{
			LossyReasons: []string{"lossy: rate approximated"},
			LossyCodes:   []string{"daily_participation_rate_approximated"},
			WarningCodes: []string{"daily_participation_rate_approximated", "analysis_unit_defaulted"},
			LDDataSource: "warehouse-ds",
		})
	if len(r.Metrics) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(r.Metrics))
	}
	m := r.Metrics[0]
	if len(m.Warnings) != 2 {
		t.Errorf("Warnings = %v, want both the lossy reason and the advisory warning", m.Warnings)
	}
	if len(m.LossyReasons) != 1 || m.LossyReasons[0] != "lossy: rate approximated" {
		t.Errorf("LossyReasons = %v, want just the lossy subset", m.LossyReasons)
	}
	if len(m.WarningCodes) != len(m.Warnings) {
		t.Errorf("WarningCodes (%d) should be parallel to Warnings (%d)", len(m.WarningCodes), len(m.Warnings))
	}
	if m.LDDataSource != "warehouse-ds" {
		t.Errorf("LDDataSource = %q, want warehouse-ds", m.LDDataSource)
	}
}

// The diagnostics have to reach the JSON, since that is what customers share back.
func TestJSON_IncludesDiagnostics(t *testing.T) {
	r := New()
	r.AddConverted("rev", "sum", "rev::sum", "rev-sum", "proj", []string{"converted 2 filter criteria"},
		Diagnostics{
			WarningCodes:            []string{"filter_applied"},
			LDDataSource:            "warehouse-ds",
			AnalysisUnits:           []string{"user"},
			StatsigRollupTimeWindow: "max",
			StatsigSourceName:       "Checkout",
			Filters: []FilterOutcome{
				{Term: "warehouse-native", Criteria: 2, Applied: true},
				{Term: "denominator", Criteria: 1, BlockedBy: "unsupported_condition", BlockedCondition: "sql_filter"},
			},
		})
	r.Finalize(1)

	// The CLI marshals the Report struct directly, so that is what to exercise.
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Metrics []struct {
			WarningCodes            []string `json:"warning_codes"`
			LDDataSource            string   `json:"ld_data_source"`
			AnalysisUnits           []string `json:"analysis_units"`
			StatsigRollupTimeWindow string   `json:"statsig_rollup_time_window"`
			StatsigSourceName       string   `json:"statsig_source_name"`
			Filters                 []struct {
				Term             string `json:"term"`
				Criteria         int    `json:"criteria"`
				Applied          bool   `json:"applied"`
				BlockedBy        string `json:"blocked_by"`
				BlockedCondition string `json:"blocked_condition"`
			} `json:"filters"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	if len(got.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(got.Metrics))
	}
	m := got.Metrics[0]
	if m.LDDataSource != "warehouse-ds" || m.StatsigRollupTimeWindow != "max" || m.StatsigSourceName != "Checkout" {
		t.Errorf("flat diagnostics missing from JSON: %+v", m)
	}
	if len(m.AnalysisUnits) != 1 || m.AnalysisUnits[0] != "user" {
		t.Errorf("analysis_units = %v", m.AnalysisUnits)
	}
	if len(m.Filters) != 2 {
		t.Fatalf("filters = %+v, want 2 terms", m.Filters)
	}
	if !m.Filters[0].Applied || m.Filters[0].Criteria != 2 {
		t.Errorf("first filter term = %+v", m.Filters[0])
	}
	if m.Filters[1].Applied || m.Filters[1].BlockedCondition != "sql_filter" {
		t.Errorf("second filter term = %+v, want blocked on sql_filter", m.Filters[1])
	}
}
