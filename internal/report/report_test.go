package report

import (
	"bytes"
	"encoding/csv"
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
	r.AddConverted("m1", "sum", "m1::sum", "m1-sum", "proj", nil)
	r.AddConverted("m2", "mean", "m2::mean", "m2-mean", "proj", []string{"unit TODO"})
	r.AddConverted("m3", "sum", "m3::sum", "m3-sum", "proj", []string{"warn1", "warn2"})
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

func TestFinalize_AllConverted(t *testing.T) {
	r := New()
	r.AddConverted("m1", "sum", "m1::sum", "m1-sum", "proj", nil)
	r.AddConverted("m2", "mean", "m2::mean", "m2-mean", "proj", nil)
	r.Finalize(2)

	if r.Converted != 2 || r.ConvertedWithWarn != 0 {
		t.Errorf("Converted=%d ConvertedWithWarn=%d, want 2/0", r.Converted, r.ConvertedWithWarn)
	}
}

func TestWriteCSV(t *testing.T) {
	r := New()
	r.AddConverted("rev", "sum", "rev::sum", "rev-sum", "proj", []string{"warn1", "warn2"})
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
	r.AddConverted("m1", "sum", "m1::sum", "m1-sum", "proj", []string{"warn"})
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

func TestNilWarningsVsEmpty(t *testing.T) {
	r := New()
	r.AddConverted("m1", "sum", "m1::sum", "m1-sum", "proj", nil)
	r.AddConverted("m2", "sum", "m2::sum", "m2-sum", "proj", []string{})

	// Both nil and empty should result in 0 ConvertedWithWarn
	r.Finalize(2)
	if r.ConvertedWithWarn != 0 {
		t.Errorf("ConvertedWithWarn = %d, want 0 (nil and empty warnings should not count)", r.ConvertedWithWarn)
	}
}
