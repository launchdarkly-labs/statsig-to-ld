package cmd

import (
	"context"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/converter"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/report"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// lossyMetric returns a Statsig metric whose conversion is lossy — a daily
// participation rate rollup, which LaunchDarkly can't represent.
func lossyMetric() statsig.Metric {
	return statsig.Metric{
		ID:               "dp::event_user",
		Name:             "daily_participation",
		Type:             "event_user",
		Directionality:   "increase",
		UnitTypes:        []string{"userID"},
		MetricEvents:     []statsig.MetricEvent{{Name: "page_view", Type: "count"}},
		RollupTimeWindow: "daily_participation_rate",
	}
}

func TestProcessMetric_LossySkippedByDefault(t *testing.T) {
	prev := flagConvertLossy
	flagConvertLossy = false
	defer func() { flagConvertLossy = prev }()

	rpt := report.New()
	// dry-run so no LD client is needed
	processMetric(context.Background(), lossyMetric(), converter.Options{}, nil, rpt, "proj", true, 1, 1)

	if len(rpt.Metrics) != 1 {
		t.Fatalf("got %d report entries, want 1", len(rpt.Metrics))
	}
	if rpt.Metrics[0].Status != report.StatusSkippedLossy {
		t.Errorf("status = %q, want %q", rpt.Metrics[0].Status, report.StatusSkippedLossy)
	}
}

func TestProcessMetric_ConvertLossyFlagConverts(t *testing.T) {
	prev := flagConvertLossy
	flagConvertLossy = true
	defer func() { flagConvertLossy = prev }()

	rpt := report.New()
	processMetric(context.Background(), lossyMetric(), converter.Options{}, nil, rpt, "proj", true, 1, 1)

	if len(rpt.Metrics) != 1 {
		t.Fatalf("got %d report entries, want 1", len(rpt.Metrics))
	}
	if rpt.Metrics[0].Status != report.StatusConverted {
		t.Errorf("status = %q, want %q", rpt.Metrics[0].Status, report.StatusConverted)
	}
	// The lossy reasons still surface as warnings when force-converted.
	if len(rpt.Metrics[0].Warnings) == 0 {
		t.Error("expected lossy reasons to surface as warnings on the force-converted metric")
	}
}
