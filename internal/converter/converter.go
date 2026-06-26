// Package converter provides pure conversion logic from Statsig metrics to
// LaunchDarkly metrics. It has no IO dependencies, making it portable into
// other codebases (e.g. Gonfalon backend for a future UI importer).
package converter

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

const maxLDKeyLength = 256

// millisPerDay converts a Statsig custom-rollup-window bound (expressed in days)
// to a LaunchDarkly window offset (expressed in milliseconds).
const millisPerDay = 24 * 60 * 60 * 1000

// Options configures the conversion behavior.
type Options struct {
	// LDDataSource is the default LD data source key applied to all Warehouse
	// Native metrics. Used when --ld-data-source is provided.
	LDDataSource string

	// SourceMapping maps individual Statsig metric source names to LD data
	// source keys. Takes precedence over LDDataSource for matching sources.
	SourceMapping map[string]string

	// DefaultUnit is the unit of measure for numeric metrics. If empty,
	// defaults to "units" (a generic placeholder).
	DefaultUnit string

	// UnitTypeMapping maps Statsig unit types to LD context kinds.
	// e.g. {"companyID": "company", "teamID": "team"}.
	// Takes precedence over the default lowercase behavior.
	UnitTypeMapping map[string]string

	// MetricsByName keys all in-scope metrics by Statsig name so ratio
	// metrics can resolve their component references. Required for ratio
	// metrics; ignored otherwise.
	MetricsByName map[string]*statsig.Metric
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
	if sg.Type == "ratio" {
		return convertRatio(sg, opts)
	}

	spec, err := termSpecFor(sg.Type)
	if err != nil {
		return nil, err
	}
	isNumeric := spec.isNumeric
	unitAgg := spec.unitAgg
	analysisType := spec.analysisType
	eventDefault := spec.eventDefault

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
	var winsorLower, winsorUpper *float32
	if sg.WarehouseNative != nil {
		wn := sg.WarehouseNative
		if wn.WinsorizationHigh != nil || wn.WinsorizationLow != nil {
			// LD supports winsorization on numeric and count metrics, expressed
			// as 0–100 percentiles (Statsig gives 0–1 fractions). It is rejected
			// on occurrence metrics (non-numeric average), so skip + warn there.
			if !isNumeric && unitAgg == "average" {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("winsorization (low=%s, high=%s) not applied — LaunchDarkly does not support winsorization on occurrence metrics",
						fmtOptFloat(wn.WinsorizationLow), fmtOptFloat(wn.WinsorizationHigh)))
			} else {
				if wn.WinsorizationLow != nil {
					v := float32(*wn.WinsorizationLow * 100)
					winsorLower = &v
				}
				if wn.WinsorizationHigh != nil {
					v := float32(*wn.WinsorizationHigh * 100)
					winsorUpper = &v
				}
			}
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

	// Custom rollup windows are mapped to LD window offsets after the metric is
	// built (they depend on whether a data source is bound). See below.

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
			unit = "units"
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

		WinsorLowerPercentile: winsorLower,
		WinsorUpperPercentile: winsorUpper,
	}

	result.LDMetric.EventDefault = eventDefault

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

	// ---------------------------------------------------------------
	// Windowed metrics: map a Statsig custom rollup window (days) to LD window
	// offsets (milliseconds). LD only accepts window offsets on metrics backed
	// by a warehouse (snowflake-experimentation) data source, so set them only
	// when a data source is bound; otherwise warn.
	// ---------------------------------------------------------------
	if sg.RollupTimeWindow == "custom" && sg.CustomRollUpStart != nil && sg.CustomRollUpEnd != nil {
		if result.LDMetric.DataSource != nil {
			start := int64(*sg.CustomRollUpStart * millisPerDay)
			end := int64(*sg.CustomRollUpEnd * millisPerDay)
			result.LDMetric.WindowStartOffset = &start
			result.LDMetric.WindowEndOffset = &end
		} else {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("custom rollup window (days %v–%v) needs a warehouse (snowflake) data source in LaunchDarkly — not applied; pass --ld-data-source to enable it",
					*sg.CustomRollUpStart, *sg.CustomRollUpEnd))
		}
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

// termSpec is the LD per-term shape derived from a Statsig metric type.
// Shared by the simple-metric path and ratio components.
type termSpec struct {
	isNumeric    bool
	unitAgg      string
	analysisType string
	eventDefault *launchdarkly.EventDefault
}

// termSpecFor maps a Statsig metric type to its LD term shape. Returns
// IncompatibleError for unsupported types, including "ratio" — ratio
// components must themselves be simple metrics.
func termSpecFor(typ string) (termSpec, error) {
	switch typ {
	case "event_count_custom", "event_count":
		return termSpec{isNumeric: false, unitAgg: "sum", analysisType: "mean"}, nil
	case "sum":
		return termSpec{
			isNumeric:    true,
			unitAgg:      "sum",
			analysisType: "mean",
			eventDefault: &launchdarkly.EventDefault{Disabled: false, Value: 0},
		}, nil
	case "mean":
		// Statsig mean excludes units without events; LD must too.
		return termSpec{
			isNumeric:    true,
			unitAgg:      "average",
			analysisType: "mean",
			eventDefault: &launchdarkly.EventDefault{Disabled: true},
		}, nil
	case "event_user", "event_user_window":
		return termSpec{isNumeric: false, unitAgg: "average", analysisType: "mean"}, nil
	case "ratio", "funnel", "composite", "composite_sum", "percentile", "user":
		return termSpec{}, &IncompatibleError{
			StatsigType: typ,
			Reason:      fmt.Sprintf("Statsig type %q is not currently supported", typ),
		}
	case "undefined":
		return termSpec{}, &IncompatibleError{
			StatsigType: typ,
			Reason:      "Statsig metric is not fully configured (setup_incomplete) — finish setting it up in the Statsig console before migrating",
		}
	default:
		return termSpec{}, fmt.Errorf("unknown Statsig metric type %q — cannot determine LaunchDarkly equivalent", typ)
	}
}

// convertRatio resolves the numerator and denominator components from
// MetricComponentMetrics, maps each through termSpecFor, and emits a single
// LD MetricPost where the numerator settings sit at the top level and the
// denominator populates the Denominator subfield. Identity and shared fields
// (key, name, tags, randomizationUnits, etc.) come from the ratio metric.
func convertRatio(sg *statsig.Metric, opts Options) (*Result, error) {
	if len(sg.MetricComponentMetrics) != 2 {
		return nil, fmt.Errorf("ratio metric %q expected 2 component metrics (numerator + denominator), got %d", sg.Name, len(sg.MetricComponentMetrics))
	}
	if opts.MetricsByName == nil {
		return nil, fmt.Errorf("ratio metric %q requires Options.MetricsByName for component resolution", sg.Name)
	}

	numRef := sg.MetricComponentMetrics[0]
	denRef := sg.MetricComponentMetrics[1]
	numerator, ok := opts.MetricsByName[numRef.Name]
	if !ok {
		return nil, fmt.Errorf("ratio metric %q: numerator component %q not found in MetricsByName", sg.Name, numRef.Name)
	}
	denominator, ok := opts.MetricsByName[denRef.Name]
	if !ok {
		return nil, fmt.Errorf("ratio metric %q: denominator component %q not found in MetricsByName", sg.Name, denRef.Name)
	}

	numSpec, err := termSpecFor(numerator.Type)
	if err != nil {
		if IsIncompatible(err) {
			return nil, &IncompatibleError{
				StatsigType: sg.Type,
				Reason:      fmt.Sprintf("ratio metric %q: numerator component %q has unsupported type %q", sg.Name, numerator.Name, numerator.Type),
			}
		}
		return nil, fmt.Errorf("ratio metric %q numerator %q: %w", sg.Name, numerator.Name, err)
	}
	denSpec, err := termSpecFor(denominator.Type)
	if err != nil {
		if IsIncompatible(err) {
			return nil, &IncompatibleError{
				StatsigType: sg.Type,
				Reason:      fmt.Sprintf("ratio metric %q: denominator component %q has unsupported type %q", sg.Name, denominator.Name, denominator.Type),
			}
		}
		return nil, fmt.Errorf("ratio metric %q denominator %q: %w", sg.Name, denominator.Name, err)
	}

	numEventKey, err := firstEventKey(numerator)
	if err != nil {
		return nil, fmt.Errorf("ratio metric %q numerator %q: %w", sg.Name, numerator.Name, err)
	}
	denEventKey, err := firstEventKey(denominator)
	if err != nil {
		return nil, fmt.Errorf("ratio metric %q denominator %q: %w", sg.Name, denominator.Name, err)
	}

	ldKey := SanitizeKey(sg.ID)
	if ldKey == "" {
		return nil, fmt.Errorf("Statsig metric %q (id=%q) produces an empty LD key after sanitization", sg.Name, sg.ID)
	}
	if len(ldKey) > maxLDKeyLength {
		ldKey = ldKey[:maxLDKeyLength]
	}

	successCriteria := "HigherThanBaseline"
	if sg.Directionality == "decrease" {
		successCriteria = "LowerThanBaseline"
	}

	lowerMapping := lowerKeyMapping(opts.UnitTypeMapping)
	var randUnits []string
	for _, u := range sg.UnitTypes {
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
		}
	}

	desc := sg.Description
	if desc == "" {
		desc = fmt.Sprintf("Converted from Statsig ratio metric %q (numerator=%q, denominator=%q)", sg.Name, numerator.Name, denominator.Name)
	} else {
		desc = fmt.Sprintf("%s [converted from Statsig ratio metric %q]", desc, sg.Name)
	}

	tags := []string{"statsig-import"}
	for _, t := range sg.Tags {
		sanitized := SanitizeKey(t)
		if sanitized != "" && sanitized != "statsig-import" {
			tags = append(tags, sanitized)
		}
	}

	var unit string
	if numSpec.isNumeric {
		if opts.DefaultUnit != "" {
			unit = opts.DefaultUnit
		} else {
			unit = "units"
		}
	}

	result := &Result{
		LDMetric: launchdarkly.MetricPost{
			Key:                 ldKey,
			Kind:                "custom",
			Name:                sg.Name,
			Description:         desc,
			EventKey:            numEventKey,
			IsNumeric:           &numSpec.isNumeric,
			SuccessCriteria:     successCriteria,
			UnitAggregationType: numSpec.unitAgg,
			AnalysisType:        numSpec.analysisType,
			RandomizationUnits:  randUnits,
			Unit:                unit,
			Tags:                tags,
			EventDefault:        numSpec.eventDefault,
			Denominator: &launchdarkly.DenominatorPost{
				EventName:           denEventKey,
				IsNumeric:           denSpec.isNumeric,
				UnitAggregationType: denSpec.unitAgg,
			},
		},
	}

	// Data source resolution: ratio metric → numerator → denominator → default.
	if dsKey := resolveDataSource(sg.MetricSourceName, opts); dsKey != "" {
		result.LDMetric.DataSource = &launchdarkly.DataSource{Key: dsKey}
	} else if dsKey := resolveDataSource(numerator.MetricSourceName, opts); dsKey != "" {
		result.LDMetric.DataSource = &launchdarkly.DataSource{Key: dsKey}
	} else if dsKey := resolveDataSource(denominator.MetricSourceName, opts); dsKey != "" {
		result.LDMetric.DataSource = &launchdarkly.DataSource{Key: dsKey}
	} else if opts.LDDataSource != "" {
		result.LDMetric.DataSource = &launchdarkly.DataSource{Key: opts.LDDataSource}
	} else {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("ratio metric %q has no LD data source binding — set one in LD before running the experiment", sg.Name))
	}

	return result, nil
}

// firstEventKey returns metricEvents[0].name, matching how the simple-metric
// path picks an eventKey.
func firstEventKey(m *statsig.Metric) (string, error) {
	if len(m.MetricEvents) == 0 {
		return "", fmt.Errorf("metric %q has no metricEvents — cannot determine LD eventKey", m.Name)
	}
	if m.MetricEvents[0].Name == "" {
		return "", fmt.Errorf("metric %q has an empty metricEvents[0].name", m.Name)
	}
	return m.MetricEvents[0].Name, nil
}
