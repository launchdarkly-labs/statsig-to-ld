// Statsig targeting → LD per-environment configuration. Ported from
// launchdarkly/goaltender/lambda_handlers/flag_import_worker/targeting.go
// (PR #829). Includes the operator + condition mapping tables, rule/condition
// translation, override conversion, and JSON Patch builders.
package converter

import (
	"fmt"
	"strings"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
)

const (
	// defaultContextKind is the LD context kind we emit on rule clauses,
	// rollouts, and targets. The Statsig importer is single-context-kind in v1.
	defaultContextKind = "user"
	// defaultVariantName is the variant name attached to the per-DC default
	// value during import. Used to resolve the "no variant matched" fallback
	// index and the OffVariation for DCs.
	defaultVariantName = "Default"
)

// LD per-environment data types — minimal shapes needed to construct the
// JSON Patch payloads against /api/v2/flags/{proj}/{key}.

// LDClause is one clause in an LD rule.
type LDClause struct {
	ContextKind string `json:"contextKind,omitempty"`
	Attribute   string `json:"attribute"`
	Op          string `json:"op"`
	Values      []any  `json:"values"`
	Negate      bool   `json:"negate"`
}

// LDRollout is a percentage rollout across variation indices.
type LDRollout struct {
	ContextKind string                `json:"contextKind,omitempty"`
	BucketBy    string                `json:"bucketBy,omitempty"`
	Variations  []LDWeightedVariation `json:"variations"`
}

// LDWeightedVariation pairs a variation index with a weight in [0, 100000].
type LDWeightedVariation struct {
	Variation int `json:"variation"`
	Weight    int `json:"weight"`
}

// LDRule is an LD targeting rule. Either Variation or Rollout is set, not
// both. TrackEvents stays false for importer-emitted rules.
type LDRule struct {
	Description string     `json:"description,omitempty"`
	Clauses     []LDClause `json:"clauses"`
	Variation   *int       `json:"variation,omitempty"`
	Rollout     *LDRollout `json:"rollout,omitempty"`
	TrackEvents bool       `json:"trackEvents"`
}

// LDTarget is a single (variation, []key) target for a context kind.
type LDTarget struct {
	ContextKind string   `json:"contextKind,omitempty"`
	Values      []string `json:"values"`
	Variation   int      `json:"variation"`
}

// LDFallthrough is the default variation served when no rule matches.
type LDFallthrough struct {
	Variation *int       `json:"variation,omitempty"`
	Rollout   *LDRollout `json:"rollout,omitempty"`
}

// LDEnvSettings is the per-env config payload built into JSON Patch ops.
type LDEnvSettings struct {
	On             bool
	Targets        []LDTarget
	ContextTargets []LDTarget
	Rules          []LDRule
	Fallthrough    LDFallthrough
	OffVariation   int
}

// --- Operator + condition mapping tables ---

type opMapping struct {
	op            string
	negate        bool
	approximation string // non-empty → emit an info-level note
}

// statsigOpToLD maps each Statsig operator to an LD operator. Unmapped entries
// signal the rule should be dropped with a warning.
//
// Approximations for version_gte/version_lte: LD has no semVer*OrEqual ops.
// We use the strict version and emit an info note so customers can manually
// add the equality clause.
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
	usesField   bool // when true, attribute comes from condition.Field (custom_field)
	drop        bool // when true, the rule is dropped with a warning
	dropReason  string
}

// statsigCondTypeToLD maps each Statsig condition type to its LD attribute +
// context kind. Drop=true types cause the whole rule to be dropped with a
// warning (segment refs, gate prerequisites, types LD has no equivalent for).
var statsigCondTypeToLD = map[string]conditionMapping{
	"public":           {drop: false}, // special-cased in convertCondition (returns no clause)
	"user_id":          {attribute: "key", contextKind: "user"},
	"unit_id":          {attribute: "key", contextKind: "user"}, // v1: always default to user
	"email":            {attribute: "email", contextKind: "user"},
	"country":          {attribute: "country", contextKind: "user"},
	"ip_address":       {attribute: "ip", contextKind: "user"},
	"app_version":      {attribute: "version", contextKind: "ld_application"}, // LD built-in app context
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

// --- Condition / rule transformation ---

type conditionResult struct {
	clause   *LDClause // nil for public conditions (rule matches everyone)
	isPublic bool
	drop     bool                     // when true, drop the whole rule
	note     *launchdarkly.FailedFlag // nil when there's nothing to surface
}

func dropWithNote(flagKey, msg string) conditionResult {
	return conditionResult{drop: true, note: &launchdarkly.FailedFlag{Name: flagKey, Error: msg}}
}

// convertCondition maps a single Statsig condition to an LD clause.
// isPublic=true means the rule matches everyone (no clause emitted). drop=true
// with note set means the rule is unmappable.
func convertCondition(c statsig.Condition, flagKey, ruleName string) conditionResult {
	condMapping, condOK := statsigCondTypeToLD[c.Type]
	if !condOK {
		return dropWithNote(flagKey,
			fmt.Sprintf("[warning] Rule %q skipped: condition type %q is not supported by the importer.", ruleName, c.Type))
	}
	if condMapping.drop {
		return dropWithNote(flagKey,
			fmt.Sprintf("[warning] Rule %q skipped: %s.", ruleName, condMapping.dropReason))
	}
	if c.Type == "public" {
		return conditionResult{isPublic: true}
	}

	opMap, opOK := statsigOpToLD[c.Operator]
	if !opOK {
		return dropWithNote(flagKey,
			fmt.Sprintf("[warning] Rule %q skipped: operator %q has no LaunchDarkly equivalent.", ruleName, c.Operator))
	}

	attribute := condMapping.attribute
	if condMapping.usesField {
		if c.Field == "" {
			return dropWithNote(flagKey,
				fmt.Sprintf("[warning] Rule %q skipped: custom_field condition is missing the field name.", ruleName))
		}
		attribute = c.Field
	}

	result := conditionResult{
		clause: &LDClause{
			ContextKind: condMapping.contextKind,
			Attribute:   attribute,
			Op:          opMap.op,
			Values:      normalizeTargetValue(c.TargetValue),
			Negate:      opMap.negate,
		},
	}

	if opMap.approximation != "" {
		result.note = &launchdarkly.FailedFlag{
			Name:  flagKey,
			Error: fmt.Sprintf("[info] Rule %q: operator %q approximated: %s. Manual review recommended.", ruleName, c.Operator, opMap.approximation),
		}
	}

	if c.Type == "unit_id" && c.CustomID != "" && c.CustomID != "userID" {
		result.note = &launchdarkly.FailedFlag{
			Name:  flagKey,
			Error: fmt.Sprintf("[info] Rule %q used Statsig unit ID %q which was mapped to the 'user' context. Re-map in LaunchDarkly if needed.", ruleName, c.CustomID),
		}
	}

	return result
}

// normalizeTargetValue coerces Statsig targetValue (scalar | array) into the
// LD clause Values slice. Null/empty inputs return empty slice (LD evaluates
// empty-Values clauses as false-matching, which is the safe default).
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

// ruleConversionResult is the output of converting one Statsig rule.
//   - drop: skip emitting this rule (notes may still surface).
//   - promoteFallthrough: overwrite the env's fallthrough with this value.
//   - stopProcessing: halt rule iteration for the env after this result.
type ruleConversionResult struct {
	rule               LDRule
	drop               bool
	notes              []launchdarkly.FailedFlag
	promoteFallthrough *LDFallthrough
	stopProcessing     bool
}

// gateRolloutFromPP returns the (variation, rollout) pair representing a
// Statsig gate rule's pass-percentage outcome in LD terms. Exactly one of
// the returned pointers is non-nil.
func gateRolloutFromPP(pp *float64) (variation *int, rollout *LDRollout) {
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
		return nil, &LDRollout{
			ContextKind: defaultContextKind,
			Variations: []LDWeightedVariation{
				{Variation: 0, Weight: w},
				{Variation: 1, Weight: 100000 - w},
			},
		}
	}
}

// convertGateRule maps one gate rule to an LD rule.
//
// passPercentage:
//   - nil or 0 → Variation = 1 (false)
//   - 100 → Variation = 0 (true)
//   - in between → Rollout weighted across variations 0,1
func convertGateRule(rule statsig.GateRule, flagKey string) ruleConversionResult {
	clauses, hasPublic, drop, notes := buildClauses(rule.Conditions, flagKey, rule.Name)
	if drop {
		return ruleConversionResult{drop: true, notes: notes}
	}
	if len(clauses) == 0 && !hasPublic {
		return ruleConversionResult{drop: true, notes: append(notes, launchdarkly.FailedFlag{
			Name:  flagKey,
			Error: fmt.Sprintf("[warning] Rule %q had no mappable conditions and was dropped.", rule.Name),
		})}
	}
	// Public-only rules ("matches everyone") have no clauses. LD rejects
	// rules with zero clauses, so we promote the rule's pass-percentage
	// rollout to the flag's per-env fallthrough variation/rollout instead.
	// The caller stops rule iteration once it sees a promoteFallthrough
	// (subsequent Statsig rules are unreachable: first-match-wins).
	if len(clauses) == 0 && hasPublic {
		ft := gateFallthroughFromPP(rule.PassPercentage)
		return ruleConversionResult{
			drop:               true,
			promoteFallthrough: &ft,
			stopProcessing:     true,
			notes: append(notes, launchdarkly.FailedFlag{
				Name:  flagKey,
				Error: fmt.Sprintf("[info] Rule %q targets everyone; promoted to the flag's fallthrough variation/rollout in LaunchDarkly. Any rules after this one in Statsig were unreachable and were not imported.", rule.Name),
			}),
		}
	}

	out := LDRule{Description: rule.Name, Clauses: clauses}
	out.Variation, out.Rollout = gateRolloutFromPP(rule.PassPercentage)
	return ruleConversionResult{rule: out, notes: notes}
}

// gateFallthroughFromPP wraps gateRolloutFromPP into an LDFallthrough for the
// public-only promotion path.
func gateFallthroughFromPP(pp *float64) LDFallthrough {
	v, r := gateRolloutFromPP(pp)
	return LDFallthrough{Variation: v, Rollout: r}
}

// convertDCRule maps one DC rule to an LD rule. variantNameToIndex resolves
// variant names → post-dedup variation index.
func convertDCRule(rule statsig.DCRule, variantNameToIndex map[string]int, flagKey string) ruleConversionResult {
	clauses, hasPublic, drop, notes := buildClauses(rule.Conditions, flagKey, rule.Name)
	if drop {
		return ruleConversionResult{drop: true, notes: notes}
	}
	if len(clauses) == 0 && !hasPublic {
		return ruleConversionResult{drop: true, notes: append(notes, launchdarkly.FailedFlag{
			Name:  flagKey,
			Error: fmt.Sprintf("[warning] Rule %q had no mappable conditions and was dropped.", rule.Name),
		})}
	}

	variation, rollout, dropFromShape, shapeNotes := dcRolloutFromRule(rule, variantNameToIndex, flagKey)
	notes = append(notes, shapeNotes...)

	if len(clauses) == 0 && hasPublic {
		if dropFromShape {
			// Variant resolution failed. Don't promote — but DO stop iteration:
			// trailing rules in Statsig are unreachable after this public match.
			return ruleConversionResult{drop: true, stopProcessing: true, notes: notes}
		}
		return ruleConversionResult{
			drop:               true,
			promoteFallthrough: &LDFallthrough{Variation: variation, Rollout: rollout},
			stopProcessing:     true,
			notes: append(notes, launchdarkly.FailedFlag{
				Name:  flagKey,
				Error: fmt.Sprintf("[info] Rule %q targets everyone; promoted to the flag's fallthrough variation/rollout in LaunchDarkly. Any rules after this one in Statsig were unreachable and were not imported.", rule.Name),
			}),
		}
	}

	if dropFromShape {
		return ruleConversionResult{drop: true, notes: notes}
	}
	return ruleConversionResult{
		rule:  LDRule{Description: rule.Name, Clauses: clauses, Variation: variation, Rollout: rollout},
		notes: notes,
	}
}

// dcRolloutFromRule returns the (variation, rollout) pair representing a
// Statsig DC rule's pass-percentage outcome. drop is true when variant
// resolution fails.
func dcRolloutFromRule(
	rule statsig.DCRule,
	variantNameToIndex map[string]int,
	flagKey string,
) (variation *int, rollout *LDRollout, drop bool, notes []launchdarkly.FailedFlag) {
	skipNote := func(format string, args ...any) (*int, *LDRollout, bool, []launchdarkly.FailedFlag) {
		return nil, nil, true, []launchdarkly.FailedFlag{{
			Name:  flagKey,
			Error: fmt.Sprintf(format, args...),
		}}
	}

	switch pp := rule.PassPercentage; {
	case pp >= 100:
		if len(rule.Variants) == 0 {
			return skipNote("[warning] Rule %q skipped: dynamic config pass=100 rule has no variants.", rule.Name)
		}
		idx, ok := variantNameToIndex[rule.Variants[0].Name]
		if !ok {
			return skipNote("[warning] Rule %q skipped: variant %q was not imported (deduplicated or missing).", rule.Name, rule.Variants[0].Name)
		}
		return intPtr(idx), nil, false, nil

	case pp <= 0:
		idx, ok := variantNameToIndex[defaultVariantName]
		if !ok {
			return skipNote("[warning] Rule %q skipped: %q variation could not be resolved.", rule.Name, defaultVariantName)
		}
		return intPtr(idx), nil, false, nil

	default:
		weighted := make([]LDWeightedVariation, 0, len(rule.Variants)+1)
		usedWeight := 0
		for _, v := range rule.Variants {
			idx, ok := variantNameToIndex[v.Name]
			if !ok {
				return skipNote("[warning] Rule %q skipped: variant %q was not imported (deduplicated or missing).", rule.Name, v.Name)
			}
			w := rolloutWeight(v.PassPercentage)
			if w == 0 && v.PassPercentage > 0 {
				// passPercentage like 0.0005 rounds to 0 weight; preserve at least 1.
				w = 1
			}
			weighted = append(weighted, LDWeightedVariation{Variation: idx, Weight: w})
			usedWeight += w
		}
		if usedWeight < 100000 {
			defIdx, ok := variantNameToIndex[defaultVariantName]
			if !ok {
				return skipNote("[warning] Rule %q skipped: %q variation could not be resolved for rollout remainder.", rule.Name, defaultVariantName)
			}
			weighted = append(weighted, LDWeightedVariation{Variation: defIdx, Weight: 100000 - usedWeight})
		}
		return nil, &LDRollout{ContextKind: defaultContextKind, Variations: weighted}, false, nil
	}
}

// buildClauses turns a slice of Statsig conditions into LD clauses + notes.
// hasPublic is true when at least one "public" condition was present.
// drop is true when ANY condition was unmappable (atomicity rule).
func buildClauses(conditions []statsig.Condition, flagKey, ruleName string) (clauses []LDClause, hasPublic bool, drop bool, notes []launchdarkly.FailedFlag) {
	for _, c := range conditions {
		r := convertCondition(c, flagKey, ruleName)
		if r.note != nil {
			notes = append(notes, *r.note)
		}
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

// rolloutWeight converts a percentage in [0, 100] to an LD weight in [0, 100000].
func rolloutWeight(pct float64) int {
	if pct <= 0 {
		return 0
	}
	if pct >= 100 {
		return 100000
	}
	return int(pct*1000 + 0.5)
}

// --- Override conversion ---

// convertOverridesForEnv builds Targets/ContextTargets entries for one LD env
// from the full set of overrides. Includes overrides whose environment is nil
// (applies to all LD envs) OR whose environment case-insensitively matches
// any of the Statsig env names mapped to this LD env.
func convertOverridesForEnv(
	overrides []statsig.Override,
	matchedStatsigEnvNames []string,
	flagKey string,
) (targets []LDTarget, contextTargets []LDTarget, notes []launchdarkly.FailedFlag) {
	for _, o := range overrides {
		if !overrideAppliesToAnyMatched(o, matchedStatsigEnvNames) {
			continue
		}
		contextKind := "user"
		if o.UnitID != "" && o.UnitID != "userID" {
			notes = append(notes, launchdarkly.FailedFlag{
				Name:  flagKey,
				Error: fmt.Sprintf("[info] Override used Statsig unit ID %q which was mapped to the 'user' context. Re-map in LaunchDarkly if needed.", o.UnitID),
			})
		}
		failing, conflict := dropFailingConflicts(o.PassingIDs, o.FailingIDs)
		passing := o.PassingIDs
		if len(conflict) > 0 {
			notes = append(notes, launchdarkly.FailedFlag{
				Name:  flagKey,
				Error: fmt.Sprintf("[warning] Users %v appear in both passing and failing overrides for flag %q. Applied passing only.", conflict, flagKey),
			})
		}

		appendTargets := func(values []string, variation int) {
			if len(values) == 0 {
				return
			}
			t := LDTarget{Values: values, Variation: variation, ContextKind: contextKind}
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

// overrideAppliesToAnyMatched returns true when an override should be applied
// to the LD env represented by the given matched Statsig env names.
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

// dropFailingConflicts removes from failing any IDs that also appear in
// passing (passing wins), returning the deconflicted failing slice and the
// list of conflicting IDs for the warning.
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

// --- Rule fan-out across environments ---

// ruleAppliesToEnv decides whether a Statsig rule should be applied to a given
// LD env after env reconciliation. The rule's Environments pointer is nil OR
// contains a Statsig env name whose mapped LD env key matches ldEnvKey.
func ruleAppliesToEnv(envsPtr *[]string, reconciler *EnvReconciler, ldEnvKey string) bool {
	if envsPtr == nil {
		return true // null means "all envs"
	}
	for _, statsigEnv := range *envsPtr {
		mappedKey, ok := reconciler.LookupLDEnv(statsigEnv)
		if ok && mappedKey == ldEnvKey {
			return true
		}
	}
	return false
}

// --- JSON Patch builders ---

// BuildEnvPatchOps builds the JSON Patch ops that set the per-env config on a
// flag. All ops are "replace" because flag creation pre-populates each env
// shell with default empty values. The env key segment of the path is
// JSON-Pointer escaped.
func BuildEnvPatchOps(ldEnvKey string, settings LDEnvSettings) []launchdarkly.JSONPatchOp {
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

// nonNilTargets returns an empty slice when input is nil so the resulting
// JSON is `[]` instead of `null`. LD's PATCH endpoint rejects null for these
// fields.
func nonNilTargets(t []LDTarget) []LDTarget {
	if t == nil {
		return []LDTarget{}
	}
	return t
}

func nonNilRules(r []LDRule) []LDRule {
	if r == nil {
		return []LDRule{}
	}
	return r
}

// --- Per-flag settings builder ---

// BuildGateEnvSettings constructs the full set of LDEnvSettings for one gate
// flag across all reachable LD envs.
//
// Behavior:
//   - For each LD env, applies rules whose Statsig environments include the
//     mapped Statsig env (or null = all envs).
//   - Applies overrides whose environment matches OR is null.
//   - Sets fallthrough/offVariation to the false variation (variation 1).
func BuildGateEnvSettings(
	gate statsig.Gate,
	overrides []statsig.Override,
	reconciler *EnvReconciler,
) (map[string]LDEnvSettings, []launchdarkly.FailedFlag) {
	rules := make([]ruleInput, len(gate.Rules))
	for i := range gate.Rules {
		rule := gate.Rules[i]
		rules[i] = ruleInput{
			envs:    rule.Environments,
			convert: func() ruleConversionResult { return convertGateRule(rule, gate.ID) },
		}
	}
	return buildEnvSettings(gate.ID, rules, overrides, 1, reconciler)
}

// BuildDCEnvSettings is the DC parallel. variations is the post-dedup
// variations slice from variationsFromDynamicConfig; we use it to resolve
// variant references and to set OffVariation to the Default index.
func BuildDCEnvSettings(
	dc statsig.DynamicConfig,
	variations []launchdarkly.Variation,
	overrides []statsig.Override,
	reconciler *EnvReconciler,
) (map[string]LDEnvSettings, []launchdarkly.FailedFlag) {
	variantNameToIndex := buildVariantNameToIndex(variations)
	offIdx := 0
	if idx, ok := variantNameToIndex[defaultVariantName]; ok {
		offIdx = idx
	}
	rules := make([]ruleInput, len(dc.Rules))
	for i := range dc.Rules {
		rule := dc.Rules[i]
		rules[i] = ruleInput{
			envs:    rule.Environments,
			convert: func() ruleConversionResult { return convertDCRule(rule, variantNameToIndex, dc.ID) },
		}
	}
	return buildEnvSettings(dc.ID, rules, overrides, offIdx, reconciler)
}

// ruleInput is the shared rule representation buildEnvSettings consumes for
// both gates and DCs.
type ruleInput struct {
	envs    *[]string
	convert func() ruleConversionResult
}

// buildEnvSettings constructs the per-env LDEnvSettings map for one flag's
// rules + overrides. Shared between gates and DCs.
func buildEnvSettings(
	flagID string,
	rules []ruleInput,
	overrides []statsig.Override,
	offVariation int,
	reconciler *EnvReconciler,
) (map[string]LDEnvSettings, []launchdarkly.FailedFlag) {
	// Conversion is env-invariant; convert once and reuse across envs so
	// rule notes aren't duplicated per env they apply to.
	converted := make([]ruleConversionResult, len(rules))
	for i, r := range rules {
		converted[i] = r.convert()
	}
	notes := []launchdarkly.FailedFlag{}
	for _, r := range converted {
		notes = append(notes, r.notes...)
	}

	settingsByEnv := map[string]LDEnvSettings{}

	for _, ldEnvKey := range reconciler.AllReachableLDEnvKeys() {
		settings := LDEnvSettings{
			On:             true,
			Targets:        []LDTarget{},
			ContextTargets: []LDTarget{},
			Rules:          []LDRule{},
			Fallthrough:    LDFallthrough{Variation: intPtr(offVariation)},
			OffVariation:   offVariation,
		}

		// stopProcessing honors Statsig's first-match-wins: trailing rules
		// after a public-only match are dead code in Statsig and must not
		// become live LD rules.
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
					notes = append(notes, launchdarkly.FailedFlag{
						Name:  flagID,
						Error: fmt.Sprintf("[info] %d rule(s) in env %q were unreachable after a public-only rule and were not imported.", droppedTrailing, ldEnvKey),
					})
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
	return settingsByEnv, notes
}

func intPtr(i int) *int {
	return &i
}
