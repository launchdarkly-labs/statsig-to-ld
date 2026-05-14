package analyze

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// ============================================================================
// AnalyzeGates
// ============================================================================

func TestAnalyzeGates_Buckets(t *testing.T) {
	gates := []statsig.Gate{
		// 1. Pure boolean simple gate
		{
			ID: "simple",
			Rules: []statsig.GateRule{
				{Conditions: []statsig.Condition{
					{Type: "user_id", Operator: "any", TargetValue: []string{"u1"}},
				}},
			},
		},
		// 2. With segment reference → fail-closed
		{
			ID: "uses-segment",
			Rules: []statsig.GateRule{
				{Conditions: []statsig.Condition{
					{Type: condPassesSegment, TargetValue: "beta_users"},
				}},
			},
		},
		// 3. With gate prerequisite → fail-closed
		{
			ID: "uses-prereq",
			Rules: []statsig.GateRule{
				{Conditions: []statsig.Condition{
					{Type: condFailsGate, TargetValue: "kill_switch"},
				}},
			},
		},
		// 4. With custom unit_id → fail-closed
		{
			ID: "company-targeting",
			Rules: []statsig.GateRule{
				{Conditions: []statsig.Condition{
					{Type: condUnitID, CustomID: "companyID", Operator: "any"},
				}},
			},
		},
		// 5. Custom unit_id of "userID" is NOT custom — should count as simple
		{
			ID: "explicit-userid",
			Rules: []statsig.GateRule{
				{Conditions: []statsig.Condition{
					{Type: condUnitID, CustomID: "userID", Operator: "any"},
				}},
			},
		},
		// 6. Public rule followed by another rule → unreachable trailing
		{
			ID: "public-then-rule",
			Rules: []statsig.GateRule{
				{Name: "ship to all", Conditions: nil},
				{Name: "this is dead", Conditions: []statsig.Condition{
					{Type: "user_id", Operator: "any"},
				}},
			},
		},
		// 7. Approximated operator
		{
			ID: "version-targeted",
			Rules: []statsig.GateRule{
				{Conditions: []statsig.Condition{
					{Type: "app_version", Operator: "version_gte", TargetValue: "5.0"},
				}},
			},
		},
	}

	got := AnalyzeGates(gates)

	if got.Total != 7 {
		t.Errorf("Total = %d, want 7", got.Total)
	}
	if got.WithSegments != 1 {
		t.Errorf("WithSegments = %d, want 1", got.WithSegments)
	}
	if got.WithPrerequisites != 1 {
		t.Errorf("WithPrerequisites = %d, want 1", got.WithPrerequisites)
	}
	if got.WithCustomUnitID != 1 {
		t.Errorf("WithCustomUnitID = %d, want 1 (companyID only; userID does not count)", got.WithCustomUnitID)
	}
	if got.WithUnreachableRules != 1 {
		t.Errorf("WithUnreachableRules = %d, want 1", got.WithUnreachableRules)
	}
	if got.WithApproximatedOperators != 1 {
		t.Errorf("WithApproximatedOperators = %d, want 1", got.WithApproximatedOperators)
	}
	// Simple = #1 (pure boolean) + #5 (explicit userID counts as simple).
	if got.BooleanSimple != 2 {
		t.Errorf("BooleanSimple = %d, want 2", got.BooleanSimple)
	}
}

func TestIsPublicRule(t *testing.T) {
	cases := []struct {
		name string
		rule statsig.GateRule
		want bool
	}{
		{"no conditions", statsig.GateRule{}, true},
		{"single public condition", statsig.GateRule{Conditions: []statsig.Condition{{Type: "public"}}}, true},
		{"single user_id condition", statsig.GateRule{Conditions: []statsig.Condition{{Type: "user_id"}}}, false},
		{"multiple conditions including public", statsig.GateRule{Conditions: []statsig.Condition{{Type: "public"}, {Type: "user_id"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPublicRule(tc.rule); got != tc.want {
				t.Errorf("isPublicRule = %v, want %v", got, tc.want)
			}
		})
	}
}

// ============================================================================
// AnalyzeDynamicConfigs
// ============================================================================

func TestAnalyzeDynamicConfigs(t *testing.T) {
	dcs := []statsig.DynamicConfig{
		// No rules at all → single-variant
		{ID: "empty"},
		// Rules with 0 or 1 variant → single-variant
		{ID: "single-variant", Rules: []statsig.DCRule{
			{ID: "r1", Variants: []statsig.DCVariant{{Name: "only"}}},
		}},
		// At least one rule with 2+ variants → multi-variant
		{ID: "ab-test", Rules: []statsig.DCRule{
			{ID: "r1", Variants: []statsig.DCVariant{{Name: "a"}, {Name: "b"}}},
		}},
		// Multiple rules, last one is multi → still multi
		{ID: "mixed", Rules: []statsig.DCRule{
			{ID: "r1"},
			{ID: "r2", Variants: []statsig.DCVariant{{Name: "x"}, {Name: "y"}, {Name: "z"}}},
		}},
	}

	got := AnalyzeDynamicConfigs(dcs)
	if got.Total != 4 {
		t.Errorf("Total = %d, want 4", got.Total)
	}
	if got.SingleVariant != 2 {
		t.Errorf("SingleVariant = %d, want 2", got.SingleVariant)
	}
	if got.MultiVariant != 2 {
		t.Errorf("MultiVariant = %d, want 2", got.MultiVariant)
	}
}

// ============================================================================
// AnalyzeEnvironments
// ============================================================================

func TestAnalyzeEnvironments_NoLD(t *testing.T) {
	statsigEnvs := []statsig.Environment{
		{Name: "production"},
		{Name: "development"},
	}

	got := AnalyzeEnvironments(statsigEnvs, nil)

	if got.LDEnvsKnown {
		t.Error("LDEnvsKnown should be false when ldEnvs is nil")
	}
	if !reflect.DeepEqual(got.StatsigEnvs, []string{"development", "production"}) {
		t.Errorf("StatsigEnvs = %v, want sorted [development production]", got.StatsigEnvs)
	}
	if len(got.AutoCreateRequired) != 0 {
		t.Errorf("AutoCreateRequired should be empty when LD unknown; got %v", got.AutoCreateRequired)
	}
}

func TestAnalyzeEnvironments_WithLD_AutoCreateNeeded(t *testing.T) {
	statsigEnvs := []statsig.Environment{
		{Name: "production"},
		{Name: "development"},
		{Name: "staging"},
	}
	ldEnvs := []launchdarkly.Environment{
		{Key: "production", Name: "Production"},
		// "development" missing — should appear in AutoCreateRequired
		// "staging" missing — should appear in AutoCreateRequired
	}

	got := AnalyzeEnvironments(statsigEnvs, ldEnvs)

	if !got.LDEnvsKnown {
		t.Error("LDEnvsKnown should be true")
	}
	if !reflect.DeepEqual(got.AutoCreateRequired, []string{"development", "staging"}) {
		t.Errorf("AutoCreateRequired = %v, want sorted [development staging]", got.AutoCreateRequired)
	}
}

func TestAnalyzeEnvironments_CaseInsensitiveMatch(t *testing.T) {
	statsigEnvs := []statsig.Environment{{Name: "Production"}}
	ldEnvs := []launchdarkly.Environment{{Key: "production", Name: "Production"}}

	got := AnalyzeEnvironments(statsigEnvs, ldEnvs)
	if len(got.AutoCreateRequired) != 0 {
		t.Errorf("expected case-insensitive match; got AutoCreateRequired=%v", got.AutoCreateRequired)
	}
}

// ============================================================================
// AnalyzeMetrics
// ============================================================================

func TestAnalyzeMetrics_ConvertibleVsIncompatible(t *testing.T) {
	metrics := []statsig.Metric{
		// Convertible: event_count_custom is supported
		{ID: "purchase::event_count_custom", Name: "purchase", Type: "event_count_custom", UnitTypes: []string{"userID"}, MetricEvents: []statsig.MetricEvent{{Name: "purchase"}}},
		// Convertible: sum is supported
		{ID: "revenue::sum", Name: "revenue", Type: "sum", UnitTypes: []string{"userID"}, MetricEvents: []statsig.MetricEvent{{Name: "purchase"}}},
		// Incompatible: ratio is not yet supported in LD
		{ID: "conv-rate::ratio", Name: "conv-rate", Type: "ratio"},
		// Incompatible: funnel
		{ID: "signup::funnel", Name: "signup", Type: "funnel"},
	}

	got := AnalyzeMetrics(metrics)

	if got.Total != 4 {
		t.Errorf("Total = %d, want 4", got.Total)
	}
	if got.Convertible < 1 {
		t.Errorf("Convertible = %d, want at least 1", got.Convertible)
	}
	if got.Incompatible < 2 {
		t.Errorf("Incompatible = %d, want at least 2", got.Incompatible)
	}
	if got.Convertible+got.Incompatible != got.Total {
		t.Errorf("Convertible (%d) + Incompatible (%d) != Total (%d)", got.Convertible, got.Incompatible, got.Total)
	}
}

// ============================================================================
// EstimateManualWork
// ============================================================================

func TestEstimateManualWork(t *testing.T) {
	gates := GateSummary{
		WithSegments:         3,
		WithPrerequisites:    2,
		WithCustomUnitID:     1,
		WithUnreachableRules: 4,
		// approximated operators DON'T count — they import (with warnings)
		WithApproximatedOperators: 99,
	}
	dcs := DynamicConfigSummary{MultiVariant: 5}

	got := EstimateManualWork(gates, dcs)
	want := 3 + 2 + 1 + 4 + 5 // 15
	if got != want {
		t.Errorf("EstimateManualWork = %d, want %d", got, want)
	}
}

// ============================================================================
// PrintTable smoke test
// ============================================================================

func TestReportPrintTable_ContainsKeySections(t *testing.T) {
	r := Report{
		LDProject: "my-project",
		Gates: GateSummary{
			Total:         5,
			BooleanSimple: 2,
			WithSegments:  3,
		},
		DynamicConfigs: DynamicConfigSummary{Total: 1, SingleVariant: 1},
		Environments: EnvironmentSummary{
			StatsigEnvs: []string{"dev", "prod"},
			LDEnvsKnown: true,
			LDEnvs:      []string{"prod"},
			AutoCreateRequired: []string{"dev"},
		},
		Metrics:             MetricSummary{Total: 10, Convertible: 7, Incompatible: 3},
		EstimatedManualWork: 3,
	}

	var buf bytes.Buffer
	r.PrintTable(&buf)
	out := buf.String()

	for _, want := range []string{
		"my-project",
		"Gates:",
		"With segments",
		"fail-closed",
		"Will auto-create in LD",
		"dev",
		"Metrics:",
		"Estimated manual work",
		"~3 items",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("PrintTable output missing %q\n---\n%s", want, out)
		}
	}
}

func TestReportPrintTable_NoLD(t *testing.T) {
	r := Report{
		Gates:        GateSummary{Total: 1, BooleanSimple: 1},
		Environments: EnvironmentSummary{StatsigEnvs: []string{"prod"}, LDEnvsKnown: false},
	}
	var buf bytes.Buffer
	r.PrintTable(&buf)
	out := buf.String()
	if !strings.Contains(out, "LD env preview skipped") {
		t.Errorf("expected hint about missing LD key; got:\n%s", out)
	}
}
