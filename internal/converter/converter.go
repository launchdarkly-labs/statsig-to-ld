// Package converter provides pure conversion logic from Statsig metrics to
// LaunchDarkly metrics. It has no IO dependencies, making it portable into
// other codebases (e.g. Gonfalon backend for a future UI importer).
package converter

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
)

const maxLDKeyLength = 256

// Options configures the conversion behavior.
type Options struct {
	// LDDataSource is the default LD data source key applied to all Warehouse
	// Native metrics. Used when --ld-data-source is provided.
	LDDataSource string

	// SourceMapping maps individual Statsig metric source names to LD data
	// source keys. Takes precedence over LDDataSource for matching sources.
	SourceMapping map[string]string

	// DefaultUnit is the unit of measure for numeric metrics. If empty,
	// defaults to "TODO" with a warning.
	DefaultUnit string

	// UnitTypeMapping maps Statsig unit types to LD context kinds.
	// e.g. {"companyID": "company", "teamID": "team"}.
	// Takes precedence over the default lowercase behavior.
	UnitTypeMapping map[string]string
}

// Result holds the converted LD metric and any warnings about Statsig features
// that could not be carried over.
type Result struct {
	LDMetric launchdarkly.MetricPost
	Warnings []string
}

// IncompatibleError indicates the Statsig metric type has no LD equivalent.
// This is a normal outcome, not a failure — the metric should be logged as
// skipped in the migration report.
type IncompatibleError struct {
	StatsigType string
	Reason      string
}

func (e *IncompatibleError) Error() string {
	return e.Reason
}

// IsIncompatible returns true if the error indicates a Statsig metric type
// that cannot be converted to LaunchDarkly.
func IsIncompatible(err error) bool {
	var target *IncompatibleError
	return errors.As(err, &target)
}

// Convert transforms a Statsig metric into a LaunchDarkly metric definition.
// Returns an IncompatibleError for Statsig types that have no LD equivalent.
// Returns other errors for unexpected/invalid input.
func Convert(sg *statsig.Metric, opts Options) (*Result, error) {
	result := &Result{}

	// ---------------------------------------------------------------
	// Type mapping
	// ---------------------------------------------------------------
	var isNumeric bool
	var unitAgg string
	var analysisType string

	switch sg.Type {
	case "event_count_custom":
		isNumeric = false
		unitAgg = "sum"
		analysisType = "mean"
	case "sum":
		isNumeric = true
		unitAgg = "sum"
		analysisType = "mean"
	case "mean":
		isNumeric = true
		unitAgg = "average"
		analysisType = "mean"
	case "event_user", "event_user_window":
		isNumeric = false
		unitAgg = "average"
		analysisType = "mean"

	// Incompatible types
	case "ratio":
		return nil, &IncompatibleError{
			StatsigType: sg.Type,
			Reason:      fmt.Sprintf("Statsig type %q is not yet supported in LaunchDarkly (no ratio metric type)", sg.Type),
		}
	case "funnel":
		return nil, &IncompatibleError{
			StatsigType: sg.Type,
			Reason:      fmt.Sprintf("Statsig type %q requires a LaunchDarkly metric group, not a single metric", sg.Type),
		}
	case "composite", "composite_sum":
		return nil, &IncompatibleError{
			StatsigType: sg.Type,
			Reason:      fmt.Sprintf("Statsig type %q is not supported in LaunchDarkly (no composite metric type)", sg.Type),
		}
	case "percentile":
		return nil, &IncompatibleError{
			StatsigType: sg.Type,
			Reason:      fmt.Sprintf("Statsig type %q is not supported as a metric type in LaunchDarkly (LD uses percentile as an analysisType, not a standalone type)", sg.Type),
		}
	case "user":
		// Statsig "user" type covers auto-generated user-aggregation metrics
		// (DAU, MAU, WAU, stickiness, retention rates). These are not single
		// event-based metrics and have no direct LD equivalent.
		return nil, &IncompatibleError{
			StatsigType: sg.Type,
			Reason:      fmt.Sprintf("Statsig type %q (auto-generated user aggregations like DAU/MAU/retention) has no direct LaunchDarkly equivalent — recreate manually as a Warehouse Native metric if needed", sg.Type),
		}
	default:
		return nil, fmt.Errorf("unknown Statsig metric type %q — cannot determine LaunchDarkly equivalent", sg.Type)
	}

	// ---------------------------------------------------------------
	// Event key
	// ---------------------------------------------------------------
	var eventKey string
	if len(sg.MetricEvents) > 0 {
		eventKey = sg.MetricEvents[0].Name
	}
	if eventKey == "" {
		return nil, fmt.Errorf("Statsig metric %q has no metricEvents — cannot determine LD eventKey", sg.Name)
	}

	// Warn if multiple metric events — only the first is used
	if len(sg.MetricEvents) > 1 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Statsig metric has %d metric events — only the first (%q) is used; %d additional events are ignored",
				len(sg.MetricEvents), eventKey, len(sg.MetricEvents)-1))
	}

	// ---------------------------------------------------------------
	// Feature warnings for Statsig-specific capabilities
	// ---------------------------------------------------------------

	// Count distinct
	if len(sg.MetricEvents) > 0 && sg.MetricEvents[0].Type == "count_distinct" {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Statsig counts distinct %q values — LaunchDarkly will count all occurrences instead",
				sg.MetricEvents[0].MetadataKey))
	}

	// Metadata-based aggregation
	if len(sg.MetricEvents) > 0 && sg.MetricEvents[0].Type == "metadata" {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Statsig aggregates metadata field %q — LaunchDarkly will aggregate the track() metricValue; ensure events send the same value in metricValue",
				sg.MetricEvents[0].MetadataKey))
	}

	// Event criteria/filters — data-affecting: the migrated metric will be broader
	// than the original because filters are not carried over
	if len(sg.MetricEvents) > 0 && len(sg.MetricEvents[0].Criteria) > 0 {
		criteria := sg.MetricEvents[0].Criteria
		var details []string
		for _, c := range criteria {
			details = append(details, fmt.Sprintf("%s %s %s %v", c.Column, c.Type, c.Condition, c.Values))
		}
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("DATA LOSS: %d event filter criteria will NOT be applied — the LD metric will match all %q events, not just the filtered subset. Dropped filters: [%s]. Manual filter setup required in LD.",
				len(criteria), eventKey, strings.Join(details, "; ")))
	}

	// Warehouse Native advanced features
	if sg.WarehouseNative != nil {
		wn := sg.WarehouseNative
		if wn.WinsorizationHigh != nil || wn.WinsorizationLow != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("winsorization (low=%s, high=%s) is not yet supported in LaunchDarkly — outlier clipping will not be applied",
					fmtOptFloat(wn.WinsorizationLow), fmtOptFloat(wn.WinsorizationHigh)))
		}
		if wn.Cap != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("per-unit capping (cap=%v) is not supported in LaunchDarkly", *wn.Cap))
		}
		if wn.UseLogTransform != nil && *wn.UseLogTransform {
			result.Warnings = append(result.Warnings,
				"log transform is not supported in LaunchDarkly — metric values will not be log-transformed")
		}
	}

	// Custom rollup windows
	if sg.RollupTimeWindow == "custom" && sg.CustomRollUpStart != nil && sg.CustomRollUpEnd != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("custom rollup window (days %v–%v) is not yet supported in LaunchDarkly — measurement window will not be applied",
				*sg.CustomRollUpStart, *sg.CustomRollUpEnd))
	}

	// Daily participation rate
	if sg.RollupTimeWindow == "daily_participation_rate" {
		result.Warnings = append(result.Warnings,
			"daily participation rate rollup is not supported in LaunchDarkly — metric will use standard binary conversion")
	}

	// ---------------------------------------------------------------
	// Success criteria
	// ---------------------------------------------------------------
	successCriteria := "HigherThanBaseline"
	if sg.Directionality == "decrease" {
		successCriteria = "LowerThanBaseline"
	}

	// ---------------------------------------------------------------
	// Randomization units: Statsig "userID" → LD "user"
	//
	// Mapping lookup is case-insensitive on the key (Statsig side) so users
	// don't have to know whether Statsig returns "stableID" or "stableid".
	// Values are preserved as-is — LD context kinds are case-sensitive.
	// ---------------------------------------------------------------
	lowerMapping := lowerKeyMapping(opts.UnitTypeMapping)
	var randUnits []string
	for _, u := range sg.UnitTypes {
		// Exact match wins, then case-insensitive
		if mapped, ok := opts.UnitTypeMapping[u]; ok {
			randUnits = append(randUnits, mapped)
			continue
		}
		if mapped, ok := lowerMapping[strings.ToLower(u)]; ok {
			randUnits = append(randUnits, mapped)
			continue
		}
		switch strings.ToLower(u) {
		case "userid":
			randUnits = append(randUnits, "user")
		default:
			randUnits = append(randUnits, strings.ToLower(u))
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Statsig unitType %q may not match an LD context kind — verify in LD or use --unit-type-mapping", u))
		}
	}

	// ---------------------------------------------------------------
	// LD metric key: derived from Statsig ID (name::type) for idempotency
	// ---------------------------------------------------------------
	ldKey := SanitizeKey(sg.ID)
	if ldKey == "" {
		return nil, fmt.Errorf("Statsig metric %q (id=%q) produces an empty LD key after sanitization", sg.Name, sg.ID)
	}
	if len(ldKey) > maxLDKeyLength {
		ldKey = ldKey[:maxLDKeyLength]
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("LD metric key truncated to %d characters (original Statsig ID was very long)", maxLDKeyLength))
	}

	// ---------------------------------------------------------------
	// Description with provenance
	// ---------------------------------------------------------------
	desc := sg.Description
	if desc == "" {
		desc = fmt.Sprintf("Converted from Statsig metric %q (type=%s)", sg.Name, sg.Type)
	} else {
		desc = fmt.Sprintf("%s [converted from Statsig metric %q]", desc, sg.Name)
	}

	// ---------------------------------------------------------------
	// Unit of measure (required for numeric LD metrics)
	// ---------------------------------------------------------------
	var unit string
	if isNumeric {
		if opts.DefaultUnit != "" {
			unit = opts.DefaultUnit
		} else {
			unit = "TODO"
			result.Warnings = append(result.Warnings,
				"unit of measure set to placeholder \"TODO\" — update in the LD UI or re-run with --default-unit")
		}
	}

	// ---------------------------------------------------------------
	// Tags: merge Statsig tags + import marker
	// ---------------------------------------------------------------
	tags := []string{"statsig-import"}
	for _, t := range sg.Tags {
		// LD tags are lowercase alphanumeric + hyphens
		sanitized := SanitizeKey(t)
		if sanitized != "" && sanitized != "statsig-import" {
			tags = append(tags, sanitized)
		}
	}

	// ---------------------------------------------------------------
	// Build the LD metric
	// ---------------------------------------------------------------
	result.LDMetric = launchdarkly.MetricPost{
		Key:                 ldKey,
		Kind:                "custom",
		Name:                sg.Name,
		Description:         desc,
		EventKey:            eventKey,
		IsNumeric:           &isNumeric,
		SuccessCriteria:     successCriteria,
		UnitAggregationType: unitAgg,
		AnalysisType:        analysisType,
		RandomizationUnits:  randUnits,
		Unit:                unit,
		Tags:                tags,
	}

	// Default missing units to 0 for numeric metrics
	if isNumeric {
		result.LDMetric.EventDefault = &launchdarkly.EventDefault{
			Disabled: false,
			Value:    0,
		}
	}

	// ---------------------------------------------------------------
	// Warehouse Native data source resolution
	// ---------------------------------------------------------------
	if sg.MetricSourceName != "" {
		dsKey := resolveDataSource(sg.MetricSourceName, opts)
		if dsKey != "" {
			result.LDMetric.DataSource = &launchdarkly.DataSource{Key: dsKey}
		} else {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("no LD data source specified for Statsig source %q — metric created without data source binding", sg.MetricSourceName))
		}
	} else if opts.LDDataSource != "" {
		// No explicit source name on the metric, but a global default was provided
		result.LDMetric.DataSource = &launchdarkly.DataSource{Key: opts.LDDataSource}
	}

	return result, nil
}

// lowerKeyMapping returns a copy of m with all keys lowercased, used for
// case-insensitive lookup of unit-type mappings. If two keys collide on
// lowercase, the last one wins — exact-case lookup at the call site is the
// way to disambiguate intentionally.
func lowerKeyMapping(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

// resolveDataSource determines the LD data source key for a Statsig metric
// source name using the two-tier resolution: source mapping → global default.
func resolveDataSource(sourceName string, opts Options) string {
	if opts.SourceMapping != nil {
		if key, ok := opts.SourceMapping[sourceName]; ok {
			return key
		}
	}
	return opts.LDDataSource
}

// SanitizeKey converts a Statsig metric ID (e.g. "purchase_revenue::sum") into
// a valid LD metric key (e.g. "purchase-revenue-sum"). Lowercase, alphanumeric
// and hyphens only.
func SanitizeKey(id string) string {
	key := strings.ToLower(id)
	key = nonAlphanumeric.ReplaceAllString(key, "-")
	key = strings.Trim(key, "-")
	return key
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

func fmtOptFloat(f *float64) string {
	if f == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%.4f", *f)
}
