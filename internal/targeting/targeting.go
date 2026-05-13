// Package targeting converts Statsig feature-gate and dynamic-config
// targeting rules + overrides into LaunchDarkly per-environment rule, target,
// and fallthrough configuration, expressed as RFC 6902 JSON Patch operations
// suitable for the LD PATCH /api/v2/flags/<proj>/<key> endpoint.
package targeting

import (
	"fmt"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

const (
	// defaultContextKind is the LD context kind emitted on rule clauses,
	// rollouts, and targets. v1 is single-context-kind.
	defaultContextKind = "user"
	// statsigDefaultVariantName is the variant name attached to the per-DC
	// default value during import.
	statsigDefaultVariantName = "Default"
)

// ============================================================================
// LD per-environment data types
// ============================================================================

// Clause is one clause in an LD rule.
type Clause struct {
	ContextKind string `json:"contextKind,omitempty"`
	Attribute   string `json:"attribute"`
	Op          string `json:"op"`
	Values      []any  `json:"values"`
	Negate      bool   `json:"negate"`
}

// Rollout is a percentage rollout across variation indices.
type Rollout struct {
	ContextKind string              `json:"contextKind,omitempty"`
	BucketBy    string              `json:"bucketBy,omitempty"`
	Variations  []WeightedVariation `json:"variations"`
}

// WeightedVariation pairs a variation index with a weight in [0, 100000].
type WeightedVariation struct {
	Variation int `json:"variation"`
	Weight    int `json:"weight"`
}

// Rule is an LD targeting rule. Either Variation or Rollout is set, not both.
type Rule struct {
	Description string   `json:"description,omitempty"`
	Clauses     []Clause `json:"clauses"`
	Variation   *int     `json:"variation,omitempty"`
	Rollout     *Rollout `json:"rollout,omitempty"`
	TrackEvents bool     `json:"trackEvents"`
}

// Target is a single (variation, []key) target for a context kind.
type Target struct {
	ContextKind string   `json:"contextKind,omitempty"`
	Values      []string `json:"values"`
	Variation   int      `json:"variation"`
}

// Fallthrough is the default variation served when no rule matches.
type Fallthrough struct {
	Variation *int     `json:"variation,omitempty"`
	Rollout   *Rollout `json:"rollout,omitempty"`
}

// EnvSettings is the per-env config payload built into JSON Patch ops.
type EnvSettings struct {
	On             bool
	Targets        []Target
	ContextTargets []Target
	Rules          []Rule
	Fallthrough    Fallthrough
	OffVariation   int
}

// Note captures an informational or warning message emitted during
// transformation. Surfaced in the targeting report alongside per-flag
// outcomes. Severity is "info" or "warning".
type Note struct {
	FlagKey  string
	Severity string
	Message  string
}

func newWarning(flagKey, format string, args ...any) Note {
	return Note{FlagKey: flagKey, Severity: "warning", Message: fmt.Sprintf(format, args...)}
}

func newInfo(flagKey, format string, args ...any) Note {
	return Note{FlagKey: flagKey, Severity: "info", Message: fmt.Sprintf(format, args...)}
}

// ============================================================================
// Operator + condition mapping tables
// ============================================================================

type opMapping struct {
	op            string
	negate        bool
	approximation string // non-empty → emit an info-level note
}

// statsigOpToLD maps each Statsig operator to an LD operator. Unmapped entries
// signal the rule should be dropped with a warning. Approximations
// (version_gte/lte) translate to the strict version with an info note.
var statsigOpToLD = map[string]opMapping{
	"any":                 {op: "in", negate: false},
	"none":                {op: "in", negate: true},
	"any_case_sensitive":  {op: "in", negate: false},
	"none_case_sensitive": {op: "in", negate: true},
	"gt":                  {op: "greaterThan", negate: false},
	"lt":                  {op: "lessThan", negate: false},
	"gte":                 {op: "greaterThanOrEqual", negate: false},
	"lte":                 {op: "lessThanOrEqual", negate: false},
	"str_matches":         {op: "matches", negate: false},
	"str_contains_any":    {op: "contains", negate: false},
	"str_contains_none":   {op: "contains", negate: true},
	"version_gt":          {op: "semVerGreaterThan", negate: false},
	"version_lt":          {op: "semVerLessThan", negate: false},
	"version_gte":         {op: "semVerGreaterThan", negate: false, approximation: "LD has no semVerGreaterThanOrEqual; approximated as semVerGreaterThan"},
	"version_lte":         {op: "semVerLessThan", negate: false, approximation: "LD has no semVerLessThanOrEqual; approximated as semVerLessThan"},
	"version_eq":          {op: "semVerEqual", negate: false},
	"before":              {op: "before", negate: false},
	"after":               {op: "after", negate: false},
}

type conditionMapping struct {
	attribute   string
	contextKind string
	usesField   bool   // attribute comes from condition.Field (custom_field)
	drop        bool   // drop the whole rule with a warning
	dropReason  string
}

// statsigCondTypeToLD maps each Statsig condition type to its LD attribute +
// context kind. Drop=true types cause the rule to be dropped.
var statsigCondTypeToLD = map[string]conditionMapping{
	"public":           {drop: false}, // special-cased in convertCondition
	"user_id":          {attribute: "key", contextKind: "user"},
	"unit_id":          {attribute: "key", contextKind: "user"}, // v1: always default to user
	"email":            {attribute: "email", contextKind: "user"},
	"country":          {attribute: "country", contextKind: "user"},
	"ip_address":       {attribute: "ip", contextKind: "user"},
	"app_version":      {attribute: "version", contextKind: "ld_application"},
	"custom_field":     {usesField: true, contextKind: "user"},
	"os_name":          {attribute: "os", contextKind: "user"},
	"os_version":       {attribute: "osVersion", contextKind: "user"},
	"browser_name":     {attribute: "browser", contextKind: "user"},
	"browser_version":  {attribute: "browserVersion", contextKind: "user"},
	"locale":           {attribute: "locale", contextKind: "user"},
	"time":             {attribute: "time", contextKind: "user"},
	"device_model":     {attribute: "deviceModel", contextKind: "user"},
	"target_app":       {attribute: "application.id", contextKind: "ld_application"},
	"passes_segment":   {drop: true, dropReason: "Statsig segment reference; segments must be re-created in LaunchDarkly manually"},
	"fails_segment":    {drop: true, dropReason: "Statsig segment reference; segments must be re-created in LaunchDarkly manually"},
	"passes_gate":      {drop: true, dropReason: "gate-prerequisite conditions are not imported; set up flag prerequisite manually"},
	"fails_gate":       {drop: true, dropReason: "gate-prerequisite conditions are not imported; set up flag prerequisite manually"},
	"environment_tier": {drop: true, dropReason: "Statsig environment_tier has no LaunchDarkly equivalent"},
}

// ============================================================================
// Condition / rule conversion
// ============================================================================

type conditionResult struct {
	clause   *Clause // nil for public conditions
	isPublic bool
	drop     bool
	// notes accumulates *all* observations attached to this condition. A
	// single condition can produce both an approximation warning AND a
	// unit_id remap notice (e.g. `version_gte` on a `unit_id` field with
	// CustomID="companyID"); both must reach the report.
	notes []Note
}

func dropWithWarning(flagKey, format string, args ...any) conditionResult {
	return conditionResult{drop: true, notes: []Note{newWarning(flagKey, format, args...)}}
}

// convertCondition maps a single Statsig condition to an LD clause.
// isPublic=true means the rule matches everyone (no clause emitted).
// drop=true with note set means the rule is unmappable.
func convertCondition(c statsig.Condition, flagKey, ruleName string) conditionResult {
	condMap, condOK := statsigCondTypeToLD[c.Type]
	if !condOK {
		return dropWithWarning(flagKey,
			"Rule %q skipped: condition type %q is not supported by the importer.", ruleName, c.Type)
	}
	if condMap.drop {
		return dropWithWarning(flagKey, "Rule %q skipped: %s.", ruleName, condMap.dropReason)
	}
	if c.Type == "public" {
		return conditionResult{isPublic: true}
	}

	opMap, opOK := statsigOpToLD[c.Operator]
	if !opOK {
		return dropWithWarning(flagKey,
			"Rule %q skipped: operator %q has no LaunchDarkly equivalent.", ruleName, c.Operator)
	}

	attribute := condMap.attribute
	if condMap.usesField {
		if c.Field == "" {
			return dropWithWarning(flagKey,
				"Rule %q skipped: custom_field condition is missing the field name.", ruleName)
		}
		attribute = c.Field
	}

	result := conditionResult{
		clause: &Clause{
			ContextKind: condMap.contextKind,
			Attribute:   attribute,
			Op:          opMap.op,
			Values:      normalizeTargetValue(c.TargetValue),
			Negate:      opMap.negate,
		},
	}

	if opMap.approximation != "" {
		result.notes = append(result.notes, newInfo(flagKey,
			"Rule %q: operator %q approximated: %s. Manual review recommended.",
			ruleName, c.Operator, opMap.approximation))
	}

	if c.Type == "unit_id" && c.CustomID != "" && c.CustomID != "userID" {
		result.notes = append(result.notes, newInfo(flagKey,
			"Rule %q used Statsig unit ID %q which was mapped to the 'user' context. Re-map in LaunchDarkly if needed.",
			ruleName, c.CustomID))
	}

	return result
}

// normalizeTargetValue coerces Statsig targetValue (scalar | array) into the
// LD clause Values slice. Null/empty inputs return empty slice.
func normalizeTargetValue(tv any) []any {
	if tv == nil {
		return []any{}
	}
	switch v := tv.(type) {
	case []any:
		return v
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out
	case []int:
		out := make([]any, len(v))
		for i, n := range v {
			out[i] = n
		}
		return out
	case []float64:
		out := make([]any, len(v))
		for i, n := range v {
			out[i] = n
		}
		return out
	default:
		return []any{v}
	}
}

// ============================================================================
// Rule conversion
// ============================================================================

type ruleConversionResult struct {
	rule               Rule
	drop               bool
	notes              []Note
	promoteFallthrough *Fallthrough
	stopProcessing     bool
}

// gateRolloutFromPP returns the (variation, rollout) pair representing a
// Statsig gate rule's pass-percentage outcome in LD terms.
func gateRolloutFromPP(pp *float64) (variation *int, rollout *Rollout) {
	p := 0.0
	if pp != nil {
		p = *pp
	}
	switch {
	case p >= 100:
		return intPtr(0), nil
	case p <= 0:
		return intPtr(1), nil
	default:
		w := rolloutWeight(p)
		return nil, &Rollout{
			ContextKind: defaultContextKind,
			Variations: []WeightedVariation{
				{Variation: 0, Weight: w},
				{Variation: 1, Weight: 100000 - w},
			},
		}
	}
}

// convertGateRule maps one gate rule to an LD rule.
func convertGateRule(rule statsig.GateRule, flagKey string) ruleConversionResult {
	clauses, hasPublic, drop, notes := buildClauses(rule.Conditions, flagKey, rule.Name)
	if drop {
		return ruleConversionResult{drop: true, notes: notes}
	}
	if len(clauses) == 0 && !hasPublic {
		return ruleConversionResult{drop: true, notes: append(notes, newWarning(flagKey,
			"Rule %q had no mappable conditions and was dropped.", rule.Name))}
	}
	if len(clauses) == 0 && hasPublic {
		ft := gateFallthroughFromPP(rule.PassPercentage)
		return ruleConversionResult{
			drop:               true,
			promoteFallthrough: &ft,
			stopProcessing:     true,
			notes: append(notes, newInfo(flagKey,
				"Rule %q targets everyone; promoted to the flag's fallthrough variation/rollout in LaunchDarkly. Any rules after this one in Statsig were unreachable and were not imported.",
				rule.Name)),
		}
	}

	out := Rule{Description: rule.Name, Clauses: clauses}
	out.Variation, out.Rollout = gateRolloutFromPP(rule.PassPercentage)
	return ruleConversionResult{rule: out, notes: notes}
}

func gateFallthroughFromPP(pp *float64) Fallthrough {
	v, r := gateRolloutFromPP(pp)
	return Fallthrough{Variation: v, Rollout: r}
}

// convertDCRule maps one DC rule to an LD rule.
func convertDCRule(rule statsig.DCRule, variantNameToIndex map[string]int, flagKey string) ruleConversionResult {
	clauses, hasPublic, drop, notes := buildClauses(rule.Conditions, flagKey, rule.Name)
	if drop {
		return ruleConversionResult{drop: true, notes: notes}
	}
	if len(clauses) == 0 && !hasPublic {
		return ruleConversionResult{drop: true, notes: append(notes, newWarning(flagKey,
			"Rule %q had no mappable conditions and was dropped.", rule.Name))}
	}

	variation, rollout, dropFromShape, shapeNotes := dcRolloutFromRule(rule, variantNameToIndex, flagKey)
	notes = append(notes, shapeNotes...)

	if len(clauses) == 0 && hasPublic {
		if dropFromShape {
			return ruleConversionResult{drop: true, stopProcessing: true, notes: notes}
		}
		return ruleConversionResult{
			drop:               true,
			promoteFallthrough: &Fallthrough{Variation: variation, Rollout: rollout},
			stopProcessing:     true,
			notes: append(notes, newInfo(flagKey,
				"Rule %q targets everyone; promoted to the flag's fallthrough variation/rollout in LaunchDarkly. Any rules after this one in Statsig were unreachable and were not imported.",
				rule.Name)),
		}
	}

	if dropFromShape {
		return ruleConversionResult{drop: true, notes: notes}
	}
	return ruleConversionResult{
		rule:  Rule{Description: rule.Name, Clauses: clauses, Variation: variation, Rollout: rollout},
		notes: notes,
	}
}

func dcRolloutFromRule(
	rule statsig.DCRule,
	variantNameToIndex map[string]int,
	flagKey string,
) (variation *int, rollout *Rollout, drop bool, notes []Note) {
	skipNote := func(format string, args ...any) (*int, *Rollout, bool, []Note) {
		return nil, nil, true, []Note{newWarning(flagKey, format, args...)}
	}

	switch pp := rule.PassPercentage; {
	case pp >= 100:
		if len(rule.Variants) == 0 {
			return skipNote("Rule %q skipped: dynamic config pass=100 rule has no variants.", rule.Name)
		}
		idx, ok := variantNameToIndex[rule.Variants[0].Name]
		if !ok {
			return skipNote("Rule %q skipped: variant %q was not imported (deduplicated or missing).", rule.Name, rule.Variants[0].Name)
		}
		return intPtr(idx), nil, false, nil

	case pp <= 0:
		idx, ok := variantNameToIndex[statsigDefaultVariantName]
		if !ok {
			return skipNote("Rule %q skipped: %q variation could not be resolved.", rule.Name, statsigDefaultVariantName)
		}
		return intPtr(idx), nil, false, nil

	default:
		weighted := make([]WeightedVariation, 0, len(rule.Variants)+1)
		usedWeight := 0
		for _, v := range rule.Variants {
			idx, ok := variantNameToIndex[v.Name]
			if !ok {
				return skipNote("Rule %q skipped: variant %q was not imported (deduplicated or missing).", rule.Name, v.Name)
			}
			w := rolloutWeight(v.PassPercentage)
			if w == 0 && v.PassPercentage > 0 {
				w = 1
			}
			weighted = append(weighted, WeightedVariation{Variation: idx, Weight: w})
			usedWeight += w
		}
		if usedWeight > 100000 {
			// Variant pass-percentages summed to more than 100% — a malformed
			// Statsig config (each variant pp is meant to be a share of total
			// traffic). Refuse rather than emit a rollout with a negative
			// remainder weight, which LD would reject anyway.
			return skipNote("Rule %q skipped: variant pass-percentages sum to %.2f%% (>100%%); refusing to emit invalid rollout.", rule.Name, float64(usedWeight)/1000)
		}
		if usedWeight < 100000 {
			defIdx, ok := variantNameToIndex[statsigDefaultVariantName]
			if !ok {
				return skipNote("Rule %q skipped: %q variation could not be resolved for rollout remainder.", rule.Name, statsigDefaultVariantName)
			}
			weighted = append(weighted, WeightedVariation{Variation: defIdx, Weight: 100000 - usedWeight})
		}
		return nil, &Rollout{ContextKind: defaultContextKind, Variations: weighted}, false, nil
	}
}

// buildClauses turns a slice of Statsig conditions into LD clauses, plus notes.
// hasPublic = at least one "public" condition. drop = ANY condition unmappable
// (atomicity decision: drop the rule, not the whole flag).
func buildClauses(conditions []statsig.Condition, flagKey, ruleName string) (clauses []Clause, hasPublic bool, drop bool, notes []Note) {
	for _, c := range conditions {
		r := convertCondition(c, flagKey, ruleName)
		notes = append(notes, r.notes...)
		if r.drop {
			return nil, false, true, notes
		}
		if r.isPublic {
			hasPublic = true
			continue
		}
		if r.clause != nil {
			clauses = append(clauses, *r.clause)
		}
	}
	return clauses, hasPublic, false, notes
}

func rolloutWeight(pct float64) int {
	if pct <= 0 {
		return 0
	}
	if pct >= 100 {
		return 100000
	}
	return int(pct*1000 + 0.5)
}

// buildVariantNameToIndex builds a name→index map from a Flag's variations
// slice. Used by DC rule conversion to resolve variant references.
func buildVariantNameToIndex(variations []launchdarkly.Variation) map[string]int {
	out := make(map[string]int, len(variations))
	for i, v := range variations {
		if _, exists := out[v.Name]; !exists {
			out[v.Name] = i
		}
	}
	return out
}

// ============================================================================
// Override conversion
// ============================================================================

// convertOverridesForEnv builds Targets/ContextTargets entries for one LD env
// from the set of overrides. Includes overrides whose environment is nil
// (applies to all envs) OR case-insensitively matches a Statsig env name
// mapped to this LD env.
func convertOverridesForEnv(
	overrides []statsig.Override,
	matchedStatsigEnvNames []string,
	flagKey string,
) (targets []Target, contextTargets []Target, notes []Note) {
	for _, o := range overrides {
		if !overrideAppliesToAnyMatched(o, matchedStatsigEnvNames) {
			continue
		}
		contextKind := "user"
		if o.UnitID != "" && o.UnitID != "userID" {
			notes = append(notes, newInfo(flagKey,
				"Override used Statsig unit ID %q which was mapped to the 'user' context. Re-map in LaunchDarkly if needed.",
				o.UnitID))
		}
		failing, conflict := dropFailingConflicts(o.PassingIDs, o.FailingIDs)
		passing := o.PassingIDs
		if len(conflict) > 0 {
			notes = append(notes, newWarning(flagKey,
				"Users %v appear in both passing and failing overrides for flag %q. Applied passing only.",
				conflict, flagKey))
		}

		appendTargets := func(values []string, variation int) {
			if len(values) == 0 {
				return
			}
			t := Target{Values: values, Variation: variation, ContextKind: contextKind}
			if contextKind == "user" {
				targets = append(targets, t)
			} else {
				contextTargets = append(contextTargets, t)
			}
		}
		appendTargets(passing, 0)
		appendTargets(failing, 1)
	}
	return targets, contextTargets, notes
}

func overrideAppliesToAnyMatched(o statsig.Override, matchedStatsigEnvNames []string) bool {
	if o.Environment == nil {
		return true
	}
	for _, name := range matchedStatsigEnvNames {
		if strings.EqualFold(*o.Environment, name) {
			return true
		}
	}
	return false
}

func dropFailingConflicts(passing, failing []string) (cleanedFailing, conflicts []string) {
	passingSet := make(map[string]struct{}, len(passing))
	for _, p := range passing {
		passingSet[p] = struct{}{}
	}
	cleanedFailing = make([]string, 0, len(failing))
	conflicts = make([]string, 0)
	for _, f := range failing {
		if _, ok := passingSet[f]; ok {
			conflicts = append(conflicts, f)
			continue
		}
		cleanedFailing = append(cleanedFailing, f)
	}
	return cleanedFailing, conflicts
}

// ============================================================================
// Rule env-fan-out
// ============================================================================

func ruleAppliesToEnv(envsPtr *[]string, reconciler *EnvReconciler, ldEnvKey string) bool {
	if envsPtr == nil {
		return true
	}
	for _, statsigEnv := range *envsPtr {
		mappedKey, ok := reconciler.LookupLDEnv(statsigEnv)
		if ok && mappedKey == ldEnvKey {
			return true
		}
	}
	return false
}

// ============================================================================
// JSON Patch builders
// ============================================================================

// BuildEnvPatchOps builds the JSON Patch ops that set the per-env config on a
// flag. All ops are "replace" because flag creation pre-populates each env
// shell with default empty values.
func BuildEnvPatchOps(ldEnvKey string, settings EnvSettings) []launchdarkly.JSONPatchOp {
	escaped := launchdarkly.EscapeJSONPointer(ldEnvKey)
	prefix := "/environments/" + escaped
	return []launchdarkly.JSONPatchOp{
		{Op: "replace", Path: prefix + "/on", Value: settings.On},
		{Op: "replace", Path: prefix + "/targets", Value: nonNilTargets(settings.Targets)},
		{Op: "replace", Path: prefix + "/contextTargets", Value: nonNilTargets(settings.ContextTargets)},
		{Op: "replace", Path: prefix + "/rules", Value: nonNilRules(settings.Rules)},
		{Op: "replace", Path: prefix + "/fallthrough", Value: settings.Fallthrough},
		{Op: "replace", Path: prefix + "/offVariation", Value: settings.OffVariation},
	}
}

func nonNilTargets(t []Target) []Target {
	if t == nil {
		return []Target{}
	}
	return t
}

func nonNilRules(r []Rule) []Rule {
	if r == nil {
		return []Rule{}
	}
	return r
}

// ============================================================================
// Per-flag env settings builder
// ============================================================================

// BuildGateEnvSettings constructs the full set of EnvSettings for one gate
// across all reachable LD envs.
func BuildGateEnvSettings(
	gate statsig.Gate,
	overrides []statsig.Override,
	reconciler *EnvReconciler,
) (map[string]EnvSettings, []Note) {
	rules := make([]ruleInput, len(gate.Rules))
	for i, rule := range gate.Rules {
		rule := rule // capture for closure
		rules[i] = ruleInput{
			envs:    rule.Environments,
			convert: func() ruleConversionResult { return convertGateRule(rule, gate.ID) },
		}
	}
	return buildEnvSettings(gate.ID, rules, overrides, 1, reconciler)
}

// BuildDCEnvSettings is the dynamic-config parallel. flag.Variations is the
// post-dedup variations slice from the flag-shell construction; we use it to
// resolve variant references and set OffVariation to the Default index.
func BuildDCEnvSettings(
	dc statsig.DynamicConfig,
	flag launchdarkly.Flag,
	overrides []statsig.Override,
	reconciler *EnvReconciler,
) (map[string]EnvSettings, []Note) {
	variantNameToIndex := buildVariantNameToIndex(flag.Variations)
	offIdx := 0
	if idx, ok := variantNameToIndex[statsigDefaultVariantName]; ok {
		offIdx = idx
	}
	rules := make([]ruleInput, len(dc.Rules))
	for i, rule := range dc.Rules {
		rule := rule
		rules[i] = ruleInput{
			envs:    rule.Environments,
			convert: func() ruleConversionResult { return convertDCRule(rule, variantNameToIndex, dc.ID) },
		}
	}
	return buildEnvSettings(dc.ID, rules, overrides, offIdx, reconciler)
}

type ruleInput struct {
	envs    *[]string
	convert func() ruleConversionResult
}

func buildEnvSettings(
	flagID string,
	rules []ruleInput,
	overrides []statsig.Override,
	offVariation int,
	reconciler *EnvReconciler,
) (map[string]EnvSettings, []Note) {
	// Conversion is env-invariant; convert once and reuse across envs so
	// each rule's notes don't get duplicated per env.
	converted := make([]ruleConversionResult, len(rules))
	for i, r := range rules {
		converted[i] = r.convert()
	}
	notes := []Note{}
	for _, r := range converted {
		notes = append(notes, r.notes...)
	}

	settingsByEnv := map[string]EnvSettings{}

	for _, ldEnvKey := range reconciler.AllReachableLDEnvKeys() {
		settings := EnvSettings{
			On:             true,
			Targets:        []Target{},
			ContextTargets: []Target{},
			Rules:          []Rule{},
			Fallthrough:    Fallthrough{Variation: intPtr(offVariation)},
			OffVariation:   offVariation,
		}

		for i, rule := range rules {
			if !ruleAppliesToEnv(rule.envs, reconciler, ldEnvKey) {
				continue
			}
			result := converted[i]
			if result.promoteFallthrough != nil {
				settings.Fallthrough = *result.promoteFallthrough
			}
			if !result.drop {
				settings.Rules = append(settings.Rules, result.rule)
			}
			if result.stopProcessing {
				droppedTrailing := 0
				for _, r := range rules[i+1:] {
					if ruleAppliesToEnv(r.envs, reconciler, ldEnvKey) {
						droppedTrailing++
					}
				}
				if droppedTrailing > 0 {
					notes = append(notes, newInfo(flagID,
						"%d rule(s) in env %q were unreachable after a public-only rule and were not imported.",
						droppedTrailing, ldEnvKey))
				}
				break
			}
		}

		matchedStatsigEnvNames := reconciler.StatsigEnvNamesForLDKey(ldEnvKey)
		targets, ctxTargets, overNotes := convertOverridesForEnv(overrides, matchedStatsigEnvNames, flagID)
		settings.Targets = append(settings.Targets, targets...)
		settings.ContextTargets = append(settings.ContextTargets, ctxTargets...)
		notes = append(notes, overNotes...)

		settingsByEnv[ldEnvKey] = settings
	}
	// Dedupe notes. Override conversion + unreachable-rule detection run
	// inside the per-env loop, so env-invariant observations (e.g. a unit_id
	// mapping note on a global override) would otherwise appear once per
	// reachable env. Env-specific notes (the "N rule(s) in env %q were
	// unreachable…" message embeds the env key) remain distinct after dedupe
	// because their text differs.
	return settingsByEnv, dedupeNotes(notes)
}

// dedupeNotes removes duplicate (severity, flagKey, message) entries while
// preserving order of first occurrence.
func dedupeNotes(in []Note) []Note {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[Note]struct{}, len(in))
	out := make([]Note, 0, len(in))
	for _, n := range in {
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func intPtr(i int) *int { return &i }
