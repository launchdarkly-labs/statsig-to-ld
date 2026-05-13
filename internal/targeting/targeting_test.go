package targeting

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// ============================================================================
// convertCondition
// ============================================================================

func TestConvertCondition_PublicMatchesEveryone(t *testing.T) {
	got := convertCondition(statsig.Condition{Type: "public"}, "f", "r")
	if !got.isPublic {
		t.Error("public condition should set isPublic")
	}
	if got.clause != nil {
		t.Errorf("public condition should not emit a clause; got %+v", got.clause)
	}
}

func TestConvertCondition_SegmentDropped(t *testing.T) {
	got := convertCondition(statsig.Condition{Type: "passes_segment"}, "f", "r")
	if !got.drop {
		t.Error("passes_segment should drop the rule")
	}
	if len(got.notes) == 0 || got.notes[0].Severity != "warning" {
		t.Errorf("expected warning note; got %+v", got.notes)
	}
}

func TestConvertCondition_GatePrereqDropped(t *testing.T) {
	got := convertCondition(statsig.Condition{Type: "passes_gate"}, "f", "r")
	if !got.drop {
		t.Error("passes_gate should drop the rule")
	}
}

func TestConvertCondition_UnknownTypeDropped(t *testing.T) {
	got := convertCondition(statsig.Condition{Type: "made_up_type"}, "f", "r")
	if !got.drop {
		t.Error("unknown condition type should drop the rule")
	}
}

func TestConvertCondition_UnknownOperatorDropped(t *testing.T) {
	got := convertCondition(statsig.Condition{Type: "email", Operator: "bogus_op"}, "f", "r")
	if !got.drop {
		t.Error("unknown operator should drop the rule")
	}
}

func TestConvertCondition_CustomFieldRequiresFieldName(t *testing.T) {
	got := convertCondition(statsig.Condition{Type: "custom_field", Operator: "any"}, "f", "r")
	if !got.drop {
		t.Error("custom_field without Field should drop the rule")
	}
}

func TestConvertCondition_CustomFieldUsesField(t *testing.T) {
	got := convertCondition(statsig.Condition{Type: "custom_field", Operator: "any", Field: "plan_tier", TargetValue: []string{"pro"}}, "f", "r")
	if got.drop || got.clause == nil {
		t.Fatalf("expected clause; got drop=%v clause=%+v", got.drop, got.clause)
	}
	if got.clause.Attribute != "plan_tier" {
		t.Errorf("Attribute = %q, want plan_tier", got.clause.Attribute)
	}
}

func TestConvertCondition_VersionGteEmitsApproximationNote(t *testing.T) {
	got := convertCondition(statsig.Condition{Type: "app_version", Operator: "version_gte", TargetValue: "5.0"}, "f", "r")
	if got.drop || got.clause == nil {
		t.Fatalf("version_gte should emit a clause; got drop=%v", got.drop)
	}
	if got.clause.Op != "semVerGreaterThan" {
		t.Errorf("Op = %q, want semVerGreaterThan", got.clause.Op)
	}
	if len(got.notes) == 0 || got.notes[0].Severity != "info" {
		t.Errorf("expected info note about approximation; got %+v", got.notes)
	}
}

func TestConvertCondition_CustomUnitIDEmitsInfoNote(t *testing.T) {
	got := convertCondition(statsig.Condition{Type: "unit_id", Operator: "any", CustomID: "companyID", TargetValue: []string{"acme"}}, "f", "r")
	if got.drop || got.clause == nil {
		t.Fatalf("unit_id custom should still produce a clause (mapped to user)")
	}
	if len(got.notes) == 0 || got.notes[0].Severity != "info" {
		t.Errorf("expected info note about unit_id mapping; got %+v", got.notes)
	}
}

// TestConvertCondition_ApproximationAndCustomUnitIDBothEmit asserts the
// regression for the Looking Glass review (PR #14): a condition that triggers
// BOTH the approximation path AND the custom-unit_id path must emit BOTH
// info notes. The prior implementation overwrote the approximation note
// with the unit_id note via a single-Note field.
func TestConvertCondition_ApproximationAndCustomUnitIDBothEmit(t *testing.T) {
	got := convertCondition(statsig.Condition{
		Type:        "unit_id",
		Operator:    "version_gte", // approximated → emits one note
		CustomID:    "companyID",   // custom unit → emits another note
		TargetValue: "5.0",
	}, "f", "r")
	if got.drop || got.clause == nil {
		t.Fatalf("expected clause; got drop=%v", got.drop)
	}
	if len(got.notes) != 2 {
		t.Fatalf("expected 2 notes (approximation + unit_id remap); got %d: %+v", len(got.notes), got.notes)
	}
	foundApprox := false
	foundUnit := false
	for _, n := range got.notes {
		if n.Severity != "info" {
			t.Errorf("expected info severity; got %q", n.Severity)
		}
		switch {
		case strings.Contains(n.Message, "approximated"):
			foundApprox = true
		case strings.Contains(n.Message, "unit ID"):
			foundUnit = true
		}
	}
	if !foundApprox || !foundUnit {
		t.Errorf("missing one of the two notes; foundApprox=%v foundUnit=%v notes=%+v", foundApprox, foundUnit, got.notes)
	}
}

// ============================================================================
// normalizeTargetValue
// ============================================================================

func TestNormalizeTargetValue(t *testing.T) {
	cases := []struct {
		in   any
		want []any
	}{
		{nil, []any{}},
		{"hello", []any{"hello"}},
		{42, []any{42}},
		{[]any{"a", "b"}, []any{"a", "b"}},
		{[]string{"a", "b"}, []any{"a", "b"}},
		{[]int{1, 2}, []any{1, 2}},
	}
	for _, tc := range cases {
		got := normalizeTargetValue(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("normalizeTargetValue(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ============================================================================
// rolloutWeight
// ============================================================================

func TestRolloutWeight(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{0, 0},
		{-1, 0},
		{100, 100000},
		{150, 100000},
		{50, 50000},
		{12.5, 12500},
	}
	for _, tc := range cases {
		if got := rolloutWeight(tc.in); got != tc.want {
			t.Errorf("rolloutWeight(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ============================================================================
// gateRolloutFromPP
// ============================================================================

func TestGateRolloutFromPP_FullPass(t *testing.T) {
	p := 100.0
	v, r := gateRolloutFromPP(&p)
	if v == nil || *v != 0 {
		t.Errorf("pass=100 should give variation 0; got v=%v r=%v", v, r)
	}
	if r != nil {
		t.Errorf("pass=100 should not produce a rollout; got %+v", r)
	}
}

func TestGateRolloutFromPP_ZeroPass(t *testing.T) {
	p := 0.0
	v, r := gateRolloutFromPP(&p)
	if v == nil || *v != 1 {
		t.Errorf("pass=0 should give variation 1; got v=%v r=%v", v, r)
	}
	if r != nil {
		t.Errorf("pass=0 should not produce a rollout; got %+v", r)
	}
}

func TestGateRolloutFromPP_PartialEmitsRollout(t *testing.T) {
	p := 25.0
	v, r := gateRolloutFromPP(&p)
	if v != nil {
		t.Errorf("partial pass should produce a rollout, not variation; got v=%v", v)
	}
	if r == nil || len(r.Variations) != 2 {
		t.Fatalf("expected 2-variation rollout; got %+v", r)
	}
	if r.Variations[0].Weight+r.Variations[1].Weight != 100000 {
		t.Errorf("rollout weights should sum to 100000; got %d + %d", r.Variations[0].Weight, r.Variations[1].Weight)
	}
}

// ============================================================================
// convertGateRule
// ============================================================================

func TestConvertGateRule_HappyPath(t *testing.T) {
	pp := 50.0
	rule := statsig.GateRule{
		Name:           "ramp",
		PassPercentage: &pp,
		Conditions: []statsig.Condition{
			{Type: "email", Operator: "str_contains_any", TargetValue: []string{"@acme.com"}},
		},
	}
	got := convertGateRule(rule, "show_banner")
	if got.drop {
		t.Fatalf("rule should not drop; notes=%v", got.notes)
	}
	if got.rule.Description != "ramp" {
		t.Errorf("Description = %q", got.rule.Description)
	}
	if len(got.rule.Clauses) != 1 {
		t.Errorf("expected 1 clause; got %d", len(got.rule.Clauses))
	}
	if got.rule.Rollout == nil {
		t.Errorf("partial pp should produce a rollout")
	}
}

func TestConvertGateRule_PublicPromotesFallthrough(t *testing.T) {
	pp := 100.0
	rule := statsig.GateRule{
		Name:           "ship-to-all",
		PassPercentage: &pp,
		Conditions:     []statsig.Condition{{Type: "public"}},
	}
	got := convertGateRule(rule, "show_banner")
	if !got.drop {
		t.Errorf("public rule should be dropped (promoted to fallthrough)")
	}
	if got.promoteFallthrough == nil {
		t.Fatalf("expected promoteFallthrough; got nil")
	}
	if !got.stopProcessing {
		t.Errorf("public rule should stop processing of trailing rules")
	}
	if got.promoteFallthrough.Variation == nil || *got.promoteFallthrough.Variation != 0 {
		t.Errorf("fallthrough variation = %v, want 0", got.promoteFallthrough.Variation)
	}
}

func TestConvertGateRule_SegmentRefDrops(t *testing.T) {
	rule := statsig.GateRule{
		Name: "beta-cohort",
		Conditions: []statsig.Condition{
			{Type: "passes_segment", TargetValue: "beta_users"},
		},
	}
	got := convertGateRule(rule, "show_banner")
	if !got.drop {
		t.Error("rule with segment ref should be dropped")
	}
}

// ============================================================================
// convertOverridesForEnv
// ============================================================================

func TestConvertOverridesForEnv_NilEnvAppliesToAll(t *testing.T) {
	overrides := []statsig.Override{
		{Environment: nil, UnitID: "userID", PassingIDs: []string{"u1", "u2"}, FailingIDs: []string{"u3"}},
	}
	targets, ctxTargets, notes := convertOverridesForEnv(overrides, []string{"production"}, "f")
	_ = notes
	if len(targets) != 2 {
		t.Errorf("expected 2 targets (passing + failing); got %d", len(targets))
	}
	if len(ctxTargets) != 0 {
		t.Errorf("user-context targets should go to Targets not ContextTargets; got %d ctx", len(ctxTargets))
	}
	if targets[0].Variation != 0 {
		t.Errorf("passing target should be variation 0; got %d", targets[0].Variation)
	}
}

func TestConvertOverridesForEnv_FilteredByEnv(t *testing.T) {
	prod := "production"
	staging := "staging"
	overrides := []statsig.Override{
		{Environment: &prod, UnitID: "userID", PassingIDs: []string{"p1"}},
		{Environment: &staging, UnitID: "userID", PassingIDs: []string{"s1"}},
	}
	targets, _, _ := convertOverridesForEnv(overrides, []string{"production"}, "f")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target (production only); got %d", len(targets))
	}
	if !reflect.DeepEqual(targets[0].Values, []string{"p1"}) {
		t.Errorf("wrong env's override matched; got %+v", targets[0].Values)
	}
}

func TestConvertOverridesForEnv_PassingWinsOnConflict(t *testing.T) {
	overrides := []statsig.Override{
		{Environment: nil, UnitID: "userID", PassingIDs: []string{"u1"}, FailingIDs: []string{"u1", "u2"}},
	}
	targets, _, notes := convertOverridesForEnv(overrides, []string{"production"}, "f")
	if len(targets) != 2 {
		t.Fatalf("expected 2 target entries (passing+failing); got %d", len(targets))
	}
	// Failing should NOT include u1; should still include u2.
	for _, tg := range targets {
		if tg.Variation == 1 {
			if reflect.DeepEqual(tg.Values, []string{"u1"}) {
				t.Errorf("u1 should have been dropped from failing; got %+v", tg.Values)
			}
		}
	}
	// Should have a warning about the conflict.
	foundConflict := false
	for _, n := range notes {
		if n.Severity == "warning" {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Errorf("expected a conflict warning; got notes=%v", notes)
	}
}

// ============================================================================
// JSON Patch builder
// ============================================================================

func TestBuildEnvPatchOps_EnvKeyEscaped(t *testing.T) {
	settings := EnvSettings{On: true, OffVariation: 1}
	ops := BuildEnvPatchOps("prod/west", settings)
	wantPrefix := "/environments/prod~1west/"
	for _, op := range ops {
		if len(op.Path) < len(wantPrefix) || op.Path[:len(wantPrefix)] != wantPrefix {
			t.Errorf("env key with '/' should be JSON-Pointer escaped; want prefix %q got %q", wantPrefix, op.Path)
			break
		}
	}
}

func TestBuildEnvPatchOps_HasAllFields(t *testing.T) {
	ops := BuildEnvPatchOps("production", EnvSettings{})
	wantSuffixes := []string{"/on", "/targets", "/contextTargets", "/rules", "/fallthrough", "/offVariation"}
	if len(ops) != len(wantSuffixes) {
		t.Fatalf("expected %d ops; got %d", len(wantSuffixes), len(ops))
	}
	for i, want := range wantSuffixes {
		if !endsWith(ops[i].Path, want) {
			t.Errorf("op[%d].Path = %q, want suffix %q", i, ops[i].Path, want)
		}
	}
}

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// ============================================================================
// Integration: BuildGateEnvSettings
// ============================================================================

// fakeReconciler implements the minimal EnvReconciler surface (we use the real
// EnvReconciler since it's plain data after Reconcile).
func newFakeReconciler(envMap map[string]string) *EnvReconciler {
	r := &EnvReconciler{
		mapping:     map[string]string{},
		reverseMap:  map[string][]string{},
		unreachable: map[string]bool{},
	}
	for statsigName, ldKey := range envMap {
		r.mapping[statsigName] = ldKey
		r.reverseMap[ldKey] = append(r.reverseMap[ldKey], statsigName)
	}
	keys := map[string]bool{}
	for _, k := range r.mapping {
		keys[k] = true
	}
	for k := range keys {
		r.allLDKeys = append(r.allLDKeys, k)
	}
	// Production Reconcile.Reconcile sorts allLDKeys; mirror that here so the
	// fake matches the deterministic env-iteration contract callers (and
	// tests) depend on.
	sort.Strings(r.allLDKeys)
	return r
}

func TestBuildGateEnvSettings_FanOutAcrossEnvs(t *testing.T) {
	r := newFakeReconciler(map[string]string{
		"production": "production",
		"staging":    "staging",
	})
	pp := 100.0
	gate := statsig.Gate{
		ID: "show_banner",
		Rules: []statsig.GateRule{
			{
				Name:           "ramp",
				PassPercentage: &pp,
				Conditions: []statsig.Condition{
					{Type: "email", Operator: "str_contains_any", TargetValue: []string{"@acme.com"}},
				},
			},
		},
	}

	settingsByEnv, _ := BuildGateEnvSettings(gate, nil, r)
	if len(settingsByEnv) != 2 {
		t.Fatalf("expected settings for 2 envs; got %d", len(settingsByEnv))
	}
	for envKey, s := range settingsByEnv {
		if !s.On {
			t.Errorf("env %q On should be true", envKey)
		}
		if len(s.Rules) != 1 {
			t.Errorf("env %q expected 1 rule; got %d", envKey, len(s.Rules))
		}
		if s.OffVariation != 1 {
			t.Errorf("env %q OffVariation = %d, want 1 (boolean off)", envKey, s.OffVariation)
		}
	}
}

// ============================================================================
// Integration: BuildDCEnvSettings
// ============================================================================

func TestBuildDCEnvSettings_VariantResolutionAndOffVariation(t *testing.T) {
	r := newFakeReconciler(map[string]string{"production": "production"})
	dc := statsig.DynamicConfig{
		ID: "ab_test",
		Rules: []statsig.DCRule{{
			Name:           "exposure",
			PassPercentage: 100,
			Variants:       []statsig.DCVariant{{Name: "A"}},
			Conditions: []statsig.Condition{
				{Type: "email", Operator: "str_contains_any", TargetValue: []string{"@acme.com"}},
			},
		}},
	}
	flag := launchdarkly.Flag{
		Variations: []launchdarkly.Variation{
			{Name: "A", Value: "a-value"},
			{Name: "B", Value: "b-value"},
			{Name: "Default", Value: "default-value"},
		},
	}
	settingsByEnv, _ := BuildDCEnvSettings(dc, flag, nil, r)
	s, ok := settingsByEnv["production"]
	if !ok {
		t.Fatalf("expected production env settings")
	}
	if s.OffVariation != 2 {
		t.Errorf("OffVariation = %d, want 2 (Default index)", s.OffVariation)
	}
	if len(s.Rules) != 1 {
		t.Fatalf("expected 1 rule; got %d", len(s.Rules))
	}
	if s.Rules[0].Variation == nil || *s.Rules[0].Variation != 0 {
		t.Errorf("pass=100 should resolve variant A → variation 0; got %v", s.Rules[0].Variation)
	}
}

// ============================================================================
// Integration: trailing rules dropped after public
// ============================================================================

func TestBuildGateEnvSettings_TrailingRulesDroppedAfterPublic(t *testing.T) {
	r := newFakeReconciler(map[string]string{"production": "production"})
	pp50 := 50.0
	pp100 := 100.0
	gate := statsig.Gate{
		ID: "g",
		Rules: []statsig.GateRule{
			{Name: "ship to all", PassPercentage: &pp100, Conditions: []statsig.Condition{{Type: "public"}}},
			{Name: "trailing", PassPercentage: &pp50, Conditions: []statsig.Condition{{Type: "email", Operator: "str_contains_any", TargetValue: []string{"@acme.com"}}}},
		},
	}
	settingsByEnv, notes := BuildGateEnvSettings(gate, nil, r)
	s := settingsByEnv["production"]
	if len(s.Rules) != 0 {
		t.Errorf("trailing rule should be dropped; got %d rules", len(s.Rules))
	}
	if s.Fallthrough.Variation == nil || *s.Fallthrough.Variation != 0 {
		t.Errorf("fallthrough should be promoted to variation 0; got %v", s.Fallthrough.Variation)
	}
	foundUnreachable := false
	for _, n := range notes {
		if n.Severity == "info" {
			foundUnreachable = true
		}
	}
	if !foundUnreachable {
		t.Errorf("expected info note about unreachable trailing rules; got notes=%v", notes)
	}
}
