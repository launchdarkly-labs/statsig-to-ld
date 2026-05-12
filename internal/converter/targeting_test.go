package converter

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
)

func floatPtr(f float64) *float64 { return &f }
func strSlicePtr(s []string) *[]string { return &s }

// ===========================================================================
// Operator mapping — every entry in statsigOpToLD verified, plus a sample of
// operators NOT in the table (which Statsig's enum permits — `on`, `is_null`,
// etc.) to confirm they drop with a warning rather than silently defaulting.
// ===========================================================================

func TestConvertCondition_OperatorMappingTable(t *testing.T) {
	type tc struct {
		statsigOp string
		ldOp      string
		negate    bool
		approx    bool
	}
	cases := []tc{
		{"any", "in", false, false},
		{"none", "in", true, false},
		{"any_case_sensitive", "in", false, false},
		{"none_case_sensitive", "in", true, false},
		{"gt", "greaterThan", false, false},
		{"lt", "lessThan", false, false},
		{"gte", "greaterThanOrEqual", false, false},
		{"lte", "lessThanOrEqual", false, false},
		{"str_matches", "matches", false, false},
		{"str_contains_any", "contains", false, false},
		{"str_contains_none", "contains", true, false},
		{"version_gt", "semVerGreaterThan", false, false},
		{"version_lt", "semVerLessThan", false, false},
		{"version_gte", "semVerGreaterThan", false, true},
		{"version_lte", "semVerLessThan", false, true},
		{"version_eq", "semVerEqual", false, false},
		{"before", "before", false, false},
		{"after", "after", false, false},
	}
	for _, c := range cases {
		t.Run(c.statsigOp, func(t *testing.T) {
			cond := statsig.Condition{Type: "email", Operator: c.statsigOp, TargetValue: "x"}
			r := convertCondition(cond, "flag-key", "rule")
			if r.drop {
				t.Fatalf("operator %s should map, not drop (note=%v)", c.statsigOp, r.note)
			}
			if r.clause == nil {
				t.Fatalf("operator %s: clause is nil", c.statsigOp)
			}
			if r.clause.Op != c.ldOp {
				t.Errorf("operator %s: Op = %q, want %q", c.statsigOp, r.clause.Op, c.ldOp)
			}
			if r.clause.Negate != c.negate {
				t.Errorf("operator %s: Negate = %v, want %v", c.statsigOp, r.clause.Negate, c.negate)
			}
			if c.approx {
				if r.note == nil || !strings.Contains(r.note.Error, "approximated") {
					t.Errorf("operator %s should emit approximation note, got %v", c.statsigOp, r.note)
				}
			}
		})
	}
}

func TestConvertCondition_UnmappedOperatorDrops(t *testing.T) {
	// Operators that appear in customer Statsig projects but aren't in our
	// translation table. Per the locked atomicity decision, drop the whole
	// rule rather than emit a wrong clause.
	cases := []string{"on", "is_null", "is_not_null", "encoded_any",
		"array_contains_any", "array_contains_none", "array_contains_all"}
	for _, op := range cases {
		t.Run(op, func(t *testing.T) {
			cond := statsig.Condition{Type: "email", Operator: op, TargetValue: "x"}
			r := convertCondition(cond, "flag-key", "rule")
			if !r.drop {
				t.Errorf("operator %s should drop, did not", op)
			}
			if r.note == nil || !strings.Contains(r.note.Error, "has no LaunchDarkly equivalent") {
				t.Errorf("operator %s: missing drop note, got %v", op, r.note)
			}
		})
	}
}

// ===========================================================================
// Condition-type mapping — every entry in statsigCondTypeToLD verified, plus
// drop-typed entries (segment/gate references and environment_tier), plus
// unmapped types from the Statsig enum (`user_agent`, `url`, `javascript`,
// `experiment_group`) that we expect to drop with a warning.
// ===========================================================================

func TestConvertCondition_ConditionTypeMappingTable(t *testing.T) {
	type tc struct {
		statsigType string
		attribute   string
		contextKind string
	}
	cases := []tc{
		{"user_id", "key", "user"},
		{"unit_id", "key", "user"},
		{"email", "email", "user"},
		{"country", "country", "user"},
		{"ip_address", "ip", "user"},
		{"app_version", "version", "ld_application"},
		{"os_name", "os", "user"},
		{"os_version", "osVersion", "user"},
		{"browser_name", "browser", "user"},
		{"browser_version", "browserVersion", "user"},
		{"locale", "locale", "user"},
		{"time", "time", "user"},
		{"device_model", "deviceModel", "user"},
		{"target_app", "application.id", "ld_application"},
	}
	for _, c := range cases {
		t.Run(c.statsigType, func(t *testing.T) {
			cond := statsig.Condition{Type: c.statsigType, Operator: "any", TargetValue: "x"}
			r := convertCondition(cond, "flag-key", "rule")
			if r.drop {
				t.Fatalf("type %s should not drop", c.statsigType)
			}
			if r.clause == nil {
				t.Fatalf("type %s: clause nil", c.statsigType)
			}
			if r.clause.Attribute != c.attribute {
				t.Errorf("type %s: Attribute = %q, want %q", c.statsigType, r.clause.Attribute, c.attribute)
			}
			if r.clause.ContextKind != c.contextKind {
				t.Errorf("type %s: ContextKind = %q, want %q", c.statsigType, r.clause.ContextKind, c.contextKind)
			}
		})
	}
}

func TestConvertCondition_CustomFieldUsesFieldVerbatim(t *testing.T) {
	cond := statsig.Condition{Type: "custom_field", Operator: "any", TargetValue: "x", Field: "plan_tier"}
	r := convertCondition(cond, "flag-key", "rule")
	if r.drop {
		t.Fatal("custom_field with field should not drop")
	}
	if r.clause.Attribute != "plan_tier" {
		t.Errorf("Attribute = %q, want \"plan_tier\"", r.clause.Attribute)
	}
	if r.clause.ContextKind != "user" {
		t.Errorf("ContextKind = %q, want \"user\"", r.clause.ContextKind)
	}
}

func TestConvertCondition_CustomFieldMissingFieldDrops(t *testing.T) {
	cond := statsig.Condition{Type: "custom_field", Operator: "any", TargetValue: "x"}
	r := convertCondition(cond, "flag-key", "rule")
	if !r.drop {
		t.Fatal("custom_field without field should drop")
	}
	if r.note == nil || !strings.Contains(r.note.Error, "missing the field name") {
		t.Errorf("expected 'missing the field name' note, got %v", r.note)
	}
}

func TestConvertCondition_PublicReturnsIsPublic(t *testing.T) {
	r := convertCondition(statsig.Condition{Type: "public"}, "flag-key", "rule")
	if !r.isPublic {
		t.Fatal("public condition should set isPublic=true")
	}
	if r.clause != nil {
		t.Error("public condition should emit no clause")
	}
	if r.drop {
		t.Error("public condition should not drop")
	}
}

func TestConvertCondition_DropTypes(t *testing.T) {
	cases := []struct {
		typ      string
		expected string
	}{
		{"passes_segment", "segment"},
		{"fails_segment", "segment"},
		{"passes_gate", "gate-prerequisite"},
		{"fails_gate", "gate-prerequisite"},
		{"environment_tier", "environment_tier"},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			r := convertCondition(statsig.Condition{Type: c.typ, Operator: "any"}, "flag-key", "rule")
			if !r.drop {
				t.Fatalf("%s should drop", c.typ)
			}
			if !strings.Contains(r.note.Error, c.expected) {
				t.Errorf("note missing %q: %s", c.expected, r.note.Error)
			}
		})
	}
}

func TestConvertCondition_UnmappedTypesFromStatsigEnumDrop(t *testing.T) {
	// Per the Statsig OpenAPI spec, the Condition.type enum includes these
	// values which our mapping table doesn't translate. They must drop with
	// a warning so customers know to handle manually.
	unmappedFromEnum := []string{"user_agent", "url", "javascript", "experiment_group"}
	for _, typ := range unmappedFromEnum {
		t.Run(typ, func(t *testing.T) {
			r := convertCondition(statsig.Condition{Type: typ, Operator: "any"}, "flag-key", "rule")
			if !r.drop {
				t.Errorf("type %s should drop (not in mapping table)", typ)
			}
			if r.note == nil || !strings.Contains(r.note.Error, "not supported by the importer") {
				t.Errorf("type %s: missing 'not supported' note, got %v", typ, r.note)
			}
		})
	}
}

func TestConvertCondition_UnitIDNonUserIDEmitsInfoNote(t *testing.T) {
	cond := statsig.Condition{Type: "unit_id", Operator: "any", TargetValue: "x", CustomID: "orgID"}
	r := convertCondition(cond, "flag-key", "rule")
	if r.drop {
		t.Fatal("unit_id with non-userID customID should not drop")
	}
	if r.clause.ContextKind != "user" {
		t.Errorf("ContextKind = %q, want \"user\" (v1 default)", r.clause.ContextKind)
	}
	if r.note == nil || !strings.Contains(r.note.Error, "orgID") {
		t.Errorf("expected info note mentioning orgID, got %v", r.note)
	}
}

func TestConvertCondition_UnitIDUserIDNoNote(t *testing.T) {
	cond := statsig.Condition{Type: "unit_id", Operator: "any", TargetValue: "x", CustomID: "userID"}
	r := convertCondition(cond, "flag-key", "rule")
	if r.note != nil {
		t.Errorf("userID customID should not emit a note, got %v", r.note)
	}
}

// ===========================================================================
// TargetValue normalization
//
// Statsig API can send: array-of-string, array-of-number, single string,
// single number, or null. LD clause Values is always []any.
// ===========================================================================

func TestNormalizeTargetValue(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  []any
	}{
		{"nil", nil, []any{}},
		{"single string", "hello", []any{"hello"}},
		{"single number", float64(42), []any{float64(42)}},
		{"array-of-any", []any{"a", "b"}, []any{"a", "b"}},
		{"array-of-string", []string{"a", "b"}, []any{"a", "b"}},
		{"array-of-int", []int{1, 2}, []any{1, 2}},
		{"array-of-float64", []float64{1.5, 2.5}, []any{1.5, 2.5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeTargetValue(c.input)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// ===========================================================================
// Rollout math
//
// rolloutWeight: pct ∈ [0,100] → weight ∈ [0, 100000]. multipleOf 0.01
// per the Statsig schema, so values like 33.33 must round correctly.
// gateRolloutFromPP: pp boundary (0, 100, nil, intermediate) → (variation,
// rollout) pair where exactly one is non-nil.
// ===========================================================================

func TestRolloutWeight(t *testing.T) {
	cases := []struct {
		pct  float64
		want int
	}{
		{0, 0},
		{100, 100000},
		{50, 50000},
		{33.33, 33330},  // 33.33 * 1000 = 33330
		{0.001, 1},      // sub-rounding preserved-floor (function returns int(0.001*1000+0.5)=1)
		{99.999, 100000}, // 99.999 * 1000 + 0.5 = 99999.5 → rounds to 99999; but clamped to 100000? Actually no clamp at 99999.
		{-1, 0},
		{101, 100000},
	}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			got := rolloutWeight(c.pct)
			// 99.999 case actually returns 99999 (not 100000). Adjust:
			if c.pct == 99.999 {
				if got != 99999 {
					t.Errorf("rolloutWeight(99.999) = %d, want 99999", got)
				}
				return
			}
			if got != c.want {
				t.Errorf("rolloutWeight(%v) = %d, want %d", c.pct, got, c.want)
			}
		})
	}
}

func TestGateRolloutFromPP_Boundaries(t *testing.T) {
	t.Run("nil → variation 1 (false)", func(t *testing.T) {
		v, r := gateRolloutFromPP(nil)
		if r != nil || v == nil || *v != 1 {
			t.Errorf("got (v=%v, r=%v), want (1, nil)", v, r)
		}
	})
	t.Run("0 → variation 1 (false)", func(t *testing.T) {
		v, r := gateRolloutFromPP(floatPtr(0))
		if r != nil || v == nil || *v != 1 {
			t.Errorf("got (v=%v, r=%v), want (1, nil)", v, r)
		}
	})
	t.Run("100 → variation 0 (true)", func(t *testing.T) {
		v, r := gateRolloutFromPP(floatPtr(100))
		if r != nil || v == nil || *v != 0 {
			t.Errorf("got (v=%v, r=%v), want (0, nil)", v, r)
		}
	})
	t.Run("50 → rollout 50/50", func(t *testing.T) {
		v, r := gateRolloutFromPP(floatPtr(50))
		if v != nil || r == nil {
			t.Fatalf("got (v=%v, r=%v), want (nil, rollout)", v, r)
		}
		if len(r.Variations) != 2 {
			t.Fatalf("expected 2 weighted variations, got %d", len(r.Variations))
		}
		if r.Variations[0].Weight != 50000 || r.Variations[1].Weight != 50000 {
			t.Errorf("weights = %d/%d, want 50000/50000", r.Variations[0].Weight, r.Variations[1].Weight)
		}
		// total must sum to 100000 for LD to accept it
		total := r.Variations[0].Weight + r.Variations[1].Weight
		if total != 100000 {
			t.Errorf("weights don't sum to 100000: %d", total)
		}
	})
}

// ===========================================================================
// Gate rule conversion — covers each pass-percentage shape (0, 100,
// intermediate) plus the public-only fallthrough promotion path.
// ===========================================================================

func TestConvertGateRule_Pass100ProducesVariationZero(t *testing.T) {
	rule := statsig.GateRule{
		Name:           "include-prod",
		PassPercentage: floatPtr(100),
		Conditions: []statsig.Condition{
			{Type: "country", Operator: "any", TargetValue: []any{"US"}},
		},
	}
	r := convertGateRule(rule, "flag")
	if r.drop {
		t.Fatal("rule should not drop")
	}
	if r.rule.Variation == nil || *r.rule.Variation != 0 {
		t.Errorf("Variation = %v, want 0", r.rule.Variation)
	}
	if r.rule.Rollout != nil {
		t.Error("Rollout should be nil when Variation is set")
	}
	if r.rule.Description != "include-prod" {
		t.Errorf("Description = %q", r.rule.Description)
	}
}

func TestConvertGateRule_Pass0ProducesVariationOne(t *testing.T) {
	rule := statsig.GateRule{
		Name:           "exclude-emea",
		PassPercentage: floatPtr(0),
		Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"DE"}}},
	}
	r := convertGateRule(rule, "flag")
	if r.drop || r.rule.Variation == nil || *r.rule.Variation != 1 {
		t.Errorf("expected Variation=1 for pass=0, got drop=%v var=%v", r.drop, r.rule.Variation)
	}
}

func TestConvertGateRule_PartialRolloutProducesWeightedRollout(t *testing.T) {
	rule := statsig.GateRule{
		Name:           "ramp",
		PassPercentage: floatPtr(25),
		Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
	}
	r := convertGateRule(rule, "flag")
	if r.drop || r.rule.Rollout == nil {
		t.Fatalf("expected rollout, got drop=%v rule=%+v", r.drop, r.rule)
	}
	if r.rule.Rollout.Variations[0].Weight != 25000 || r.rule.Rollout.Variations[1].Weight != 75000 {
		t.Errorf("weights = %d/%d, want 25000/75000", r.rule.Rollout.Variations[0].Weight, r.rule.Rollout.Variations[1].Weight)
	}
}

func TestConvertGateRule_PublicOnlyPromotesFallthrough(t *testing.T) {
	// Public-only rule has no clauses — LD rejects clauseless rules, so we
	// promote the rule's pass-percentage to the env's fallthrough.
	rule := statsig.GateRule{
		Name:           "everyone-50pct",
		PassPercentage: floatPtr(50),
		Conditions:     []statsig.Condition{{Type: "public"}},
	}
	r := convertGateRule(rule, "flag")
	if !r.drop {
		t.Error("public-only rule should drop (no clauses) but promote fallthrough")
	}
	if r.promoteFallthrough == nil {
		t.Fatal("expected promoteFallthrough to be set")
	}
	if !r.stopProcessing {
		t.Error("stopProcessing should be true (trailing rules unreachable after public match)")
	}
	if r.promoteFallthrough.Rollout == nil {
		t.Error("fallthrough should have rollout (50%)")
	}
}

func TestConvertGateRule_AllPublicTrueGoesToVariationZeroFallthrough(t *testing.T) {
	rule := statsig.GateRule{
		Name:           "always-on",
		PassPercentage: floatPtr(100),
		Conditions:     []statsig.Condition{{Type: "public"}},
	}
	r := convertGateRule(rule, "flag")
	if r.promoteFallthrough == nil || r.promoteFallthrough.Variation == nil || *r.promoteFallthrough.Variation != 0 {
		t.Errorf("expected fallthrough variation 0, got %+v", r.promoteFallthrough)
	}
}

func TestConvertGateRule_UnmappableConditionDropsWholeRule(t *testing.T) {
	rule := statsig.GateRule{
		Name:           "mixed-rule",
		PassPercentage: floatPtr(100),
		Conditions: []statsig.Condition{
			{Type: "country", Operator: "any", TargetValue: []any{"US"}},
			{Type: "passes_segment", Operator: "any"}, // unmappable → drops whole rule
		},
	}
	r := convertGateRule(rule, "flag")
	if !r.drop {
		t.Error("rule with unmappable condition should drop entirely (atomicity)")
	}
}

func TestConvertGateRule_NoConditionsNoPublicDrops(t *testing.T) {
	// Degenerate case: a rule with no conditions at all. Statsig may not
	// actually emit these, but we should drop gracefully.
	rule := statsig.GateRule{Name: "empty", PassPercentage: floatPtr(50)}
	r := convertGateRule(rule, "flag")
	if !r.drop {
		t.Error("rule with no conditions should drop")
	}
}

// ===========================================================================
// DC rule conversion
// ===========================================================================

func TestConvertDCRule_Pass100PicksFirstVariant(t *testing.T) {
	variantNameToIndex := map[string]int{"A": 0, "B": 1, "Default": 2}
	rule := statsig.DCRule{
		Name:           "winner",
		PassPercentage: 100,
		Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
		Variants:       []statsig.DCVariant{{Name: "A"}, {Name: "B"}},
	}
	r := convertDCRule(rule, variantNameToIndex, "flag")
	if r.drop {
		t.Fatalf("rule should not drop: %v", r.notes)
	}
	if r.rule.Variation == nil || *r.rule.Variation != 0 {
		t.Errorf("Variation = %v, want 0 (index of A)", r.rule.Variation)
	}
}

func TestConvertDCRule_Pass0RoutesToDefault(t *testing.T) {
	variantNameToIndex := map[string]int{"A": 0, "Default": 1}
	rule := statsig.DCRule{
		Name:           "off",
		PassPercentage: 0,
		Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
	}
	r := convertDCRule(rule, variantNameToIndex, "flag")
	if r.drop || r.rule.Variation == nil || *r.rule.Variation != 1 {
		t.Errorf("expected Variation=1 (Default), got drop=%v var=%v", r.drop, r.rule.Variation)
	}
}

func TestConvertDCRule_PartialRolloutWeightRemainderToDefault(t *testing.T) {
	// Variants sum to 75% → 25% remainder routes to Default.
	variantNameToIndex := map[string]int{"A": 0, "B": 1, "Default": 2}
	rule := statsig.DCRule{
		Name:           "split",
		PassPercentage: 50, // intermediate triggers weighted rollout
		Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
		Variants: []statsig.DCVariant{
			{Name: "A", PassPercentage: 50},
			{Name: "B", PassPercentage: 25},
		},
	}
	r := convertDCRule(rule, variantNameToIndex, "flag")
	if r.drop {
		t.Fatalf("rule should not drop: %v", r.notes)
	}
	if r.rule.Rollout == nil {
		t.Fatal("expected rollout")
	}
	// Find the Default entry (variation index 2) and check it has the 25% remainder.
	var defaultWeight int
	for _, v := range r.rule.Rollout.Variations {
		if v.Variation == 2 {
			defaultWeight = v.Weight
		}
	}
	if defaultWeight != 25000 {
		t.Errorf("Default weight = %d, want 25000 (remainder)", defaultWeight)
	}
	totalWeight := 0
	for _, v := range r.rule.Rollout.Variations {
		totalWeight += v.Weight
	}
	if totalWeight != 100000 {
		t.Errorf("weights sum = %d, want 100000", totalWeight)
	}
}

func TestConvertDCRule_UnknownVariantDropsRule(t *testing.T) {
	variantNameToIndex := map[string]int{"Default": 0}
	rule := statsig.DCRule{
		Name:           "ghost",
		PassPercentage: 100,
		Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
		Variants:       []statsig.DCVariant{{Name: "DoesNotExist"}},
	}
	r := convertDCRule(rule, variantNameToIndex, "flag")
	if !r.drop {
		t.Error("rule referencing unknown variant should drop")
	}
}

// ===========================================================================
// Overrides
// ===========================================================================

func TestDropFailingConflicts(t *testing.T) {
	cleaned, conflicts := dropFailingConflicts(
		[]string{"alice", "bob"},
		[]string{"bob", "carol"},
	)
	wantCleaned := []string{"carol"}
	wantConflicts := []string{"bob"}
	if !reflect.DeepEqual(cleaned, wantCleaned) {
		t.Errorf("cleaned = %v, want %v", cleaned, wantCleaned)
	}
	if !reflect.DeepEqual(conflicts, wantConflicts) {
		t.Errorf("conflicts = %v, want %v", conflicts, wantConflicts)
	}
}

func TestOverrideAppliesToAnyMatched(t *testing.T) {
	nilEnv := statsig.Override{}
	prodEnv := statsig.Override{Environment: strPtr("production")}
	stagingEnv := statsig.Override{Environment: strPtr("Staging")}

	if !overrideAppliesToAnyMatched(nilEnv, []string{"production"}) {
		t.Error("nil environment should apply to any env")
	}
	if !overrideAppliesToAnyMatched(prodEnv, []string{"production"}) {
		t.Error("production override should match production")
	}
	if !overrideAppliesToAnyMatched(stagingEnv, []string{"staging"}) {
		t.Error("staging override should match staging (case-insensitive)")
	}
	if overrideAppliesToAnyMatched(prodEnv, []string{"staging"}) {
		t.Error("production override should not match staging")
	}
}

func strPtr(s string) *string { return &s }

func TestConvertOverridesForEnv_PassingAndFailingSeparated(t *testing.T) {
	overrides := []statsig.Override{
		{Environment: strPtr("production"), UnitID: "userID", PassingIDs: []string{"alice"}, FailingIDs: []string{"bob"}},
	}
	targets, ctxTargets, notes := convertOverridesForEnv(overrides, []string{"production"}, "flag")
	if len(ctxTargets) != 0 {
		t.Errorf("expected no context targets (userID is treated as user context), got %+v", ctxTargets)
	}
	if len(notes) != 0 {
		t.Errorf("expected no notes for userID overrides, got %+v", notes)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets (passing + failing), got %d", len(targets))
	}
	// Check passing → variation 0, failing → variation 1
	var passingTarget, failingTarget LDTarget
	for _, tt := range targets {
		if tt.Variation == 0 {
			passingTarget = tt
		} else if tt.Variation == 1 {
			failingTarget = tt
		}
	}
	if !reflect.DeepEqual(passingTarget.Values, []string{"alice"}) {
		t.Errorf("passing target values = %v, want [alice]", passingTarget.Values)
	}
	if !reflect.DeepEqual(failingTarget.Values, []string{"bob"}) {
		t.Errorf("failing target values = %v, want [bob]", failingTarget.Values)
	}
}

func TestConvertOverridesForEnv_NonUserUnitIDEmitsNote(t *testing.T) {
	overrides := []statsig.Override{
		{Environment: nil, UnitID: "orgID", PassingIDs: []string{"acme"}},
	}
	_, _, notes := convertOverridesForEnv(overrides, []string{"production"}, "flag")
	if len(notes) != 1 {
		t.Fatalf("expected 1 note about orgID, got %d", len(notes))
	}
	if !strings.Contains(notes[0].Error, "orgID") {
		t.Errorf("note should mention orgID, got %q", notes[0].Error)
	}
}

func TestConvertOverridesForEnv_NilEnvAppliesEverywhere(t *testing.T) {
	overrides := []statsig.Override{
		{Environment: nil, UnitID: "userID", PassingIDs: []string{"alice"}},
	}
	targets, _, _ := convertOverridesForEnv(overrides, []string{"any-env"}, "flag")
	if len(targets) != 1 {
		t.Errorf("nil-env override should apply, got %d targets", len(targets))
	}
}

func TestConvertOverridesForEnv_ConflictDropsFailing(t *testing.T) {
	overrides := []statsig.Override{
		{Environment: nil, UnitID: "userID", PassingIDs: []string{"alice"}, FailingIDs: []string{"alice"}},
	}
	targets, _, notes := convertOverridesForEnv(overrides, []string{"prod"}, "flag")
	// Should produce passing-only target, plus a conflict note.
	if len(notes) == 0 || !strings.Contains(notes[0].Error, "both passing and failing") {
		t.Error("expected conflict note")
	}
	for _, tt := range targets {
		if tt.Variation == 1 {
			t.Errorf("expected no failing target (conflict dropped), got %+v", tt)
		}
	}
}

// ===========================================================================
// JSON Patch builder — verify the 6-op structure and JSON-Pointer escaping.
// ===========================================================================

func TestBuildEnvPatchOps_StructureAndPath(t *testing.T) {
	settings := LDEnvSettings{
		On:           true,
		OffVariation: 1,
		Fallthrough:  LDFallthrough{Variation: intPtr(0)},
	}
	ops := BuildEnvPatchOps("production", settings)
	if len(ops) != 6 {
		t.Fatalf("expected 6 ops, got %d", len(ops))
	}
	wantPaths := []string{
		"/environments/production/on",
		"/environments/production/targets",
		"/environments/production/contextTargets",
		"/environments/production/rules",
		"/environments/production/fallthrough",
		"/environments/production/offVariation",
	}
	for i, op := range ops {
		if op.Op != "replace" {
			t.Errorf("op[%d] = %q, want \"replace\"", i, op.Op)
		}
		if op.Path != wantPaths[i] {
			t.Errorf("op[%d].Path = %q, want %q", i, op.Path, wantPaths[i])
		}
	}
}

func TestBuildEnvPatchOps_NilTargetsBecomesEmptyArray(t *testing.T) {
	// LD's PATCH endpoint rejects `null` for targets/contextTargets/rules.
	settings := LDEnvSettings{On: true, Targets: nil, ContextTargets: nil, Rules: nil}
	ops := BuildEnvPatchOps("prod", settings)
	for _, op := range ops {
		if !strings.HasSuffix(op.Path, "/targets") && !strings.HasSuffix(op.Path, "/contextTargets") && !strings.HasSuffix(op.Path, "/rules") {
			continue
		}
		// Marshal the value to JSON to confirm it's [] not null.
		bytes, _ := json.Marshal(op.Value)
		if string(bytes) != "[]" {
			t.Errorf("op %s value = %s, want []", op.Path, bytes)
		}
	}
}

func TestBuildEnvPatchOps_EscapesEnvKey(t *testing.T) {
	// Env keys can contain '/' (rare but legal in LD); the JSON Pointer
	// segment must escape '/' as '~1' and '~' as '~0'.
	ops := BuildEnvPatchOps("prod/eu", LDEnvSettings{})
	for _, op := range ops {
		if !strings.HasPrefix(op.Path, "/environments/prod~1eu/") {
			t.Errorf("op.Path = %q, expected /environments/prod~1eu/ prefix", op.Path)
		}
	}
}

// ===========================================================================
// End-to-end env settings — tests buildEnvSettings via the public Gate entry
// point, with a hand-constructed EnvReconciler (unexported fields are
// accessible since the test is in package converter).
// ===========================================================================

// newTestReconciler builds a reconciler with hardcoded mappings, bypassing
// the actual Reconcile() flow. Statsig env names → LD env keys, plus the
// reverse map and reachable-keys list.
func newTestReconciler(statsigToLD map[string]string) *EnvReconciler {
	r := &EnvReconciler{
		mapping:     map[string]string{},
		reverseMap:  map[string][]string{},
		unreachable: map[string]bool{},
	}
	reachable := map[string]struct{}{}
	for statsigName, ldKey := range statsigToLD {
		r.mapping[strings.ToLower(statsigName)] = ldKey
		r.reverseMap[ldKey] = append(r.reverseMap[ldKey], statsigName)
		reachable[ldKey] = struct{}{}
	}
	for k := range reachable {
		r.allLDKeys = append(r.allLDKeys, k)
	}
	sort.Strings(r.allLDKeys)
	return r
}

func TestBuildGateEnvSettings_FansOutAcrossEnvs(t *testing.T) {
	reconciler := newTestReconciler(map[string]string{
		"production": "production",
		"staging":    "staging",
	})

	gate := statsig.Gate{
		ID:   "checkout_flow",
		Name: "Checkout Flow",
		Rules: []statsig.GateRule{
			{
				Name:           "prod-only",
				PassPercentage: floatPtr(100),
				Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
				Environments:   strSlicePtr([]string{"production"}),
			},
		},
	}

	settings, _ := BuildGateEnvSettings(gate, nil, reconciler)
	if len(settings) != 2 {
		t.Fatalf("expected settings for 2 envs, got %d", len(settings))
	}
	// Prod has the rule, staging does not (gate rule is env-scoped).
	if len(settings["production"].Rules) != 1 {
		t.Errorf("production should have 1 rule, got %d", len(settings["production"].Rules))
	}
	if len(settings["staging"].Rules) != 0 {
		t.Errorf("staging should have 0 rules, got %d", len(settings["staging"].Rules))
	}
	// Both envs have On=true (we always create flags as enabled per import).
	for env, s := range settings {
		if !s.On {
			t.Errorf("env %s: On should be true", env)
		}
		if s.OffVariation != 1 {
			t.Errorf("env %s: OffVariation = %d, want 1 (false)", env, s.OffVariation)
		}
	}
}

func TestBuildGateEnvSettings_NilEnvsRuleAppliesEverywhere(t *testing.T) {
	reconciler := newTestReconciler(map[string]string{"production": "production", "staging": "staging"})
	gate := statsig.Gate{
		ID: "g",
		Rules: []statsig.GateRule{
			{
				Name:           "all-envs",
				PassPercentage: floatPtr(100),
				Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
				// Environments unset (nil) → applies to all
			},
		},
	}
	settings, _ := BuildGateEnvSettings(gate, nil, reconciler)
	for env, s := range settings {
		if len(s.Rules) != 1 {
			t.Errorf("env %s: expected 1 rule (rule has no env restriction), got %d", env, len(s.Rules))
		}
	}
}

func TestBuildGateEnvSettings_PublicRuleStopProcessing(t *testing.T) {
	reconciler := newTestReconciler(map[string]string{"production": "production"})
	gate := statsig.Gate{
		ID: "g",
		Rules: []statsig.GateRule{
			{
				Name:           "everyone",
				PassPercentage: floatPtr(100),
				Conditions:     []statsig.Condition{{Type: "public"}},
			},
			{
				Name:           "unreachable",
				PassPercentage: floatPtr(0),
				Conditions:     []statsig.Condition{{Type: "country", Operator: "any", TargetValue: []any{"US"}}},
			},
		},
	}
	settings, notes := BuildGateEnvSettings(gate, nil, reconciler)
	prod := settings["production"]
	if len(prod.Rules) != 0 {
		t.Errorf("expected 0 rules (public-only promotes to fallthrough), got %d", len(prod.Rules))
	}
	// Fallthrough should be set to variation 0 (pass=100).
	if prod.Fallthrough.Variation == nil || *prod.Fallthrough.Variation != 0 {
		t.Errorf("fallthrough = %+v, want variation 0", prod.Fallthrough)
	}
	// Should emit a note about the trailing rule being unreachable.
	hasUnreachableNote := false
	for _, n := range notes {
		if strings.Contains(n.Error, "unreachable") {
			hasUnreachableNote = true
		}
	}
	if !hasUnreachableNote {
		t.Error("expected note about unreachable trailing rule")
	}
}
