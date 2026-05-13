package analyze

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/converter"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// Statsig condition Type values that trigger fail-closed under D8.
const (
	condPassesSegment = "passes_segment"
	condFailsSegment  = "fails_segment"
	condPassesGate    = "passes_gate"
	condFailsGate     = "fails_gate"
	condUnitID        = "unit_id"
)

// Statsig operators that LD approximates rather than implements exactly.
var approximatedOperators = map[string]bool{
	"version_gte": true,
	"version_lte": true,
}

// AnalyzeGates classifies a slice of gates into the GateSummary buckets.
func AnalyzeGates(gates []statsig.Gate) GateSummary {
	s := GateSummary{Total: len(gates)}
	for _, g := range gates {
		flags := classifyGate(g)
		if flags.hasSegment {
			s.WithSegments++
		}
		if flags.hasPrerequisite {
			s.WithPrerequisites++
		}
		if flags.hasCustomUnitID {
			s.WithCustomUnitID++
		}
		if flags.hasUnreachableRules {
			s.WithUnreachableRules++
		}
		if flags.hasApproximatedOperator {
			s.WithApproximatedOperators++
		}
		if flags.isSimple() {
			s.BooleanSimple++
		}
	}
	return s
}

// AnalyzeDynamicConfigs classifies a slice of dynamic configs by variant shape.
func AnalyzeDynamicConfigs(configs []statsig.DynamicConfig) DynamicConfigSummary {
	s := DynamicConfigSummary{Total: len(configs)}
	for _, c := range configs {
		// A DC is multi-variant if any rule has 2+ Variants. Single-variant
		// otherwise (which includes "no variants at all" — the older shape).
		multi := false
		for _, rule := range c.Rules {
			if len(rule.Variants) >= 2 {
				multi = true
				break
			}
		}
		if multi {
			s.MultiVariant++
		} else {
			s.SingleVariant++
		}
	}
	return s
}

// AnalyzeEnvironments compares Statsig and (optionally) LD environment lists
// and computes the auto-create preview.
//
// If ldEnvs is nil (caller didn't fetch them — typically because no LD key
// was provided), LDEnvsKnown is false and the LD-side fields are left empty.
func AnalyzeEnvironments(statsigEnvs []statsig.Environment, ldEnvs []launchdarkly.Environment) EnvironmentSummary {
	s := EnvironmentSummary{}
	for _, e := range statsigEnvs {
		s.StatsigEnvs = append(s.StatsigEnvs, e.Name)
	}
	sort.Strings(s.StatsigEnvs)

	if ldEnvs == nil {
		return s
	}
	s.LDEnvsKnown = true

	ldByLower := make(map[string]bool, len(ldEnvs))
	for _, e := range ldEnvs {
		s.LDEnvs = append(s.LDEnvs, e.Key)
		ldByLower[strings.ToLower(e.Key)] = true
		ldByLower[strings.ToLower(e.Name)] = true
	}
	sort.Strings(s.LDEnvs)

	for _, e := range statsigEnvs {
		if !ldByLower[strings.ToLower(e.Name)] {
			s.AutoCreateRequired = append(s.AutoCreateRequired, e.Name)
		}
	}
	sort.Strings(s.AutoCreateRequired)
	return s
}

// AnalyzeMetrics classifies metrics by whether the converter can produce an
// LD metric for each one. Convertible counts metrics for which the converter
// returned no error or only warnings; Incompatible counts those that returned
// an IncompatibleError.
func AnalyzeMetrics(metrics []statsig.Metric) MetricSummary {
	s := MetricSummary{Total: len(metrics)}
	for i := range metrics {
		_, err := converter.Convert(&metrics[i], converter.Options{})
		switch {
		case err == nil:
			s.Convertible++
		case converter.IsIncompatible(err):
			s.Incompatible++
		default:
			// Real conversion errors (rare in practice) count as
			// incompatible for the purpose of sizing — the user will
			// have to address them either way.
			s.Incompatible++
		}
	}
	return s
}

// LossyTargetingFeatures returns the names of D8 fail-closed features
// present in this gate's targeting rules. Returns nil when the gate has
// no lossy features. Used by `flags import` to annotate per-flag entries
// in the migration report so users know what will need --accept-data-loss
// when they run `targeting import` next.
//
// The returned names match the strings used elsewhere in CLI flags and
// documentation: "segments", "prerequisites", "custom_unit_id",
// "unreachable_rules". Approximated operators (version_gte/lte) are NOT
// listed here — they import with approximation, not fail-closed.
func LossyTargetingFeatures(g statsig.Gate) []string {
	f := classifyGate(g)
	var out []string
	if f.hasSegment {
		out = append(out, "segments")
	}
	if f.hasPrerequisite {
		out = append(out, "prerequisites")
	}
	if f.hasCustomUnitID {
		out = append(out, "custom_unit_id")
	}
	if f.hasUnreachableRules {
		out = append(out, "unreachable_rules")
	}
	return out
}

// LossyDCTargetingFeatures returns the names of D8 fail-closed features
// present in this dynamic config: multi-variant override fidelity loss, plus
// the same condition-level features `LossyTargetingFeatures` flags for gates
// (DC rules also have Conditions; the earlier version of this function
// missed them, so a DC whose targeting referenced a segment slipped past D8).
func LossyDCTargetingFeatures(c statsig.DynamicConfig) []string {
	var hasMultiVariant bool
	var f gateFlags
	for _, rule := range c.Rules {
		if len(rule.Variants) >= 2 {
			hasMultiVariant = true
		}
		for _, cond := range rule.Conditions {
			switch cond.Type {
			case condPassesSegment, condFailsSegment:
				f.hasSegment = true
			case condPassesGate, condFailsGate:
				f.hasPrerequisite = true
			case condUnitID:
				if cond.CustomID != "" && cond.CustomID != "userID" {
					f.hasCustomUnitID = true
				}
			}
		}
	}
	var out []string
	if f.hasSegment {
		out = append(out, "segments")
	}
	if f.hasPrerequisite {
		out = append(out, "prerequisites")
	}
	if f.hasCustomUnitID {
		out = append(out, "custom_unit_id")
	}
	if hasMultiVariant {
		out = append(out, "multi_variant_overrides")
	}
	return out
}

// EstimateManualWork sums the fail-closed-under-D8 counters plus the
// multi-variant DC count. Rough estimate; users should treat it as a
// magnitude indicator, not a precise count.
func EstimateManualWork(gates GateSummary, dcs DynamicConfigSummary) int {
	return gates.WithSegments +
		gates.WithPrerequisites +
		gates.WithCustomUnitID +
		gates.WithUnreachableRules +
		dcs.MultiVariant
}

// Build composes a complete Report from already-fetched data. Pure function —
// callers (the CLI) do the I/O.
func Build(
	ldProject string,
	gates []statsig.Gate,
	dcs []statsig.DynamicConfig,
	statsigEnvs []statsig.Environment,
	ldEnvs []launchdarkly.Environment,
	metrics []statsig.Metric,
) Report {
	gs := AnalyzeGates(gates)
	dcSum := AnalyzeDynamicConfigs(dcs)
	return Report{
		Timestamp:           time.Now().UTC(),
		LDProject:           ldProject,
		Gates:               gs,
		DynamicConfigs:      dcSum,
		Environments:        AnalyzeEnvironments(statsigEnvs, ldEnvs),
		Metrics:             AnalyzeMetrics(metrics),
		EstimatedManualWork: EstimateManualWork(gs, dcSum),
	}
}

// PrintTable writes a human-readable summary of the report to w.
func (r Report) PrintTable(w io.Writer) {
	fmt.Fprintln(w, "─────────────────────────────────────────────────────")
	fmt.Fprintln(w, "  Statsig → LaunchDarkly migration analysis")
	fmt.Fprintln(w, "─────────────────────────────────────────────────────")
	if r.LDProject != "" {
		fmt.Fprintf(w, "  LD project:                %s\n", r.LDProject)
	}
	fmt.Fprintf(w, "  Generated:                 %s\n", r.Timestamp.Format(time.RFC3339))
	fmt.Fprintln(w, "─────────────────────────────────────────────────────")

	fmt.Fprintf(w, "  Gates:                     %d\n", r.Gates.Total)
	fmt.Fprintf(w, "    Boolean / simple:        %d\n", r.Gates.BooleanSimple)
	if r.Gates.WithSegments > 0 {
		fmt.Fprintf(w, "    With segments:           %d   ← fail-closed (see D8)\n", r.Gates.WithSegments)
	}
	if r.Gates.WithPrerequisites > 0 {
		fmt.Fprintf(w, "    With prerequisites:      %d   ← fail-closed (see D8)\n", r.Gates.WithPrerequisites)
	}
	if r.Gates.WithCustomUnitID > 0 {
		fmt.Fprintf(w, "    With custom unit_id:     %d   ← fail-closed (see D8)\n", r.Gates.WithCustomUnitID)
	}
	if r.Gates.WithUnreachableRules > 0 {
		fmt.Fprintf(w, "    With unreachable rules:  %d   ← dropped silently\n", r.Gates.WithUnreachableRules)
	}
	if r.Gates.WithApproximatedOperators > 0 {
		fmt.Fprintf(w, "    With approx. operators:  %d   ← imported with approximation\n", r.Gates.WithApproximatedOperators)
	}

	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "  Dynamic configs:           %d\n", r.DynamicConfigs.Total)
	fmt.Fprintf(w, "    Single-variant:          %d\n", r.DynamicConfigs.SingleVariant)
	if r.DynamicConfigs.MultiVariant > 0 {
		fmt.Fprintf(w, "    Multi-variant:           %d   ← override fidelity loss\n", r.DynamicConfigs.MultiVariant)
	}

	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "  Environments (Statsig):    %d\n", len(r.Environments.StatsigEnvs))
	if r.Environments.LDEnvsKnown {
		fmt.Fprintf(w, "  Environments (LD):         %d\n", len(r.Environments.LDEnvs))
		if len(r.Environments.AutoCreateRequired) > 0 {
			fmt.Fprintf(w, "    Will auto-create in LD:  %d (%s)\n",
				len(r.Environments.AutoCreateRequired),
				strings.Join(r.Environments.AutoCreateRequired, ", "))
		} else {
			fmt.Fprintln(w, "    Will auto-create in LD:  0")
		}
	} else {
		fmt.Fprintln(w, "  (LD env preview skipped — pass --ld-key + --ld-project to enable)")
	}

	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "  Metrics:                   %d\n", r.Metrics.Total)
	fmt.Fprintf(w, "    Convertible:             %d\n", r.Metrics.Convertible)
	if r.Metrics.Incompatible > 0 {
		fmt.Fprintf(w, "    Incompatible:            %d   ← skipped on convert\n", r.Metrics.Incompatible)
	}

	fmt.Fprintln(w, "─────────────────────────────────────────────────────")
	fmt.Fprintf(w, "  Estimated manual work:     ~%d items\n", r.EstimatedManualWork)
	fmt.Fprintln(w, "─────────────────────────────────────────────────────")
}

// ============================================================================
// Internal classification
// ============================================================================

type gateFlags struct {
	hasSegment              bool
	hasPrerequisite         bool
	hasCustomUnitID         bool
	hasUnreachableRules     bool
	hasApproximatedOperator bool
	hasUnknownOperator      bool
}

// isSimple reports whether the gate has no fail-closed or approximation
// flags set — i.e., it can be imported faithfully with default flags.
func (f gateFlags) isSimple() bool {
	return !f.hasSegment &&
		!f.hasPrerequisite &&
		!f.hasCustomUnitID &&
		!f.hasUnreachableRules &&
		!f.hasApproximatedOperator &&
		!f.hasUnknownOperator
}

func classifyGate(g statsig.Gate) gateFlags {
	var f gateFlags
	seenPublic := false
	for _, rule := range g.Rules {
		if seenPublic {
			// Anything after a "public" (match-everyone, no conditions)
			// rule is unreachable.
			f.hasUnreachableRules = true
		}
		if isPublicRule(rule) {
			seenPublic = true
		}
		for _, cond := range rule.Conditions {
			switch cond.Type {
			case condPassesSegment, condFailsSegment:
				f.hasSegment = true
			case condPassesGate, condFailsGate:
				f.hasPrerequisite = true
			case condUnitID:
				if cond.CustomID != "" && cond.CustomID != "userID" {
					f.hasCustomUnitID = true
				}
			}
			if approximatedOperators[cond.Operator] {
				f.hasApproximatedOperator = true
			}
		}
	}
	return f
}

// isPublicRule returns true if the rule matches everyone — no conditions, or
// a single "public" condition. This is how Statsig models "ship to all users."
func isPublicRule(r statsig.GateRule) bool {
	if len(r.Conditions) == 0 {
		return true
	}
	if len(r.Conditions) == 1 && r.Conditions[0].Type == "public" {
		return true
	}
	return false
}
