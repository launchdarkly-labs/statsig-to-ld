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
}

// Result holds the converted LD metric and any warnings about Statsig features
// that could not be carried over.
type Result struct {
	LDMetric launchdarkly.MetricPost
	Warnings []string
	// LossyReasons is the subset of warnings that mark the conversion as lossy —
	// a Statsig feature that changes the metric's meaning was dropped or
	// approximated. Populated via addLossy; these messages also appear in
	// Warnings. Drives the default skip that --convert-lossy overrides.
	LossyReasons []string
}

// addLossy records a message that both warns the user and marks the conversion
// as lossy (a Statsig feature was dropped or approximated). The message is added
// to both Warnings and LossyReasons.
func (r *Result) addLossy(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.Warnings = append(r.Warnings, msg)
	r.LossyReasons = append(r.LossyReasons, msg)
}

// IsLossy reports whether the conversion dropped or approximated a Statsig
// feature. Lossy metrics are skipped by default; --convert-lossy converts them.
func (r *Result) IsLossy() bool { return len(r.LossyReasons) > 0 }

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

	// A warehouse-native metric must carry an aggregation; without one there is
	// nothing to dispatch on. Fail explicitly (parsing gap or incomplete metric)
	// rather than falling through to the generic "unknown type" error.
	if sg.IsWarehouseNative() && !sg.HasWarehouseAggregation() {
		return nil, fmt.Errorf("warehouse-native metric %q has no aggregation — likely a parsing gap or an incomplete Statsig metric", sg.Name)
	}

	// Dispatch on the effective aggregation: warehouse-native metrics carry it in
	// warehouseNative.aggregation, cloud metrics in the top-level type.
	effectiveType := sg.EffectiveType()
	if effectiveType == "ratio" {
		return convertRatio(sg, opts)
	}

	spec, err := termSpecFor(effectiveType)
	if err != nil {
		return nil, err
	}
	isNumeric := spec.isNumeric
	unitAgg := spec.unitAgg
	analysisType := spec.analysisType
	eventDefault := spec.eventDefault
	unitAggField := spec.unitAggField

	if effectiveType == "daily_participation" {
		result.addLossy("Statsig daily_participation (per-unit share of active days) has no exact LaunchDarkly equivalent — approximated as a binary metric, which loses the per-day rate")
	}

	// Event key resolution:
	//   1. metricEvents[0].Name — cloud metrics with explicit events.
	//   2. warehouse-native value column — the flat warehouseNative.valueColumn or
	//      metricSources[0].valueColumn (both forms occur; see NumeratorValueColumn).
	var eventKey string
	if len(sg.MetricEvents) > 0 {
		eventKey = sg.MetricEvents[0].Name
	} else if sg.IsWarehouseNative() {
		if col := sg.NumeratorValueColumn(); col != "" {
			eventKey = col
			if unitAggField == "" {
				unitAggField = col
			}
		} else if src := sg.NumeratorSourceName(); src != "" {
			// count / daily_participation / user aggregations count rows or units
			// and carry no value column. LD still needs an event key, so use the
			// source name as a provisional stand-in pending live LD verification.
			eventKey = src
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("warehouse-native %q metric has no value column (it counts rows/units); LD eventKey set to the source name %q provisionally — verify against a created LD metric", effectiveType, src))
		}
	}
	if eventKey == "" {
		if sg.IsWarehouseNative() {
			return nil, fmt.Errorf("warehouse-native metric %q: cannot determine LD eventKey — no valueColumn or metric source on warehouseNative", sg.Name)
		}
		return nil, fmt.Errorf("Statsig metric %q has no metricEvents — cannot determine LD eventKey", sg.Name)
	}

	// Warn if multiple metric events — only the first is used (lossy: the
	// additional events are dropped).
	if len(sg.MetricEvents) > 1 {
		result.addLossy("Statsig metric has %d metric events — only the first (%q) is used; %d additional events are ignored",
			len(sg.MetricEvents), eventKey, len(sg.MetricEvents)-1)
	}

	// ---------------------------------------------------------------
	// Feature warnings for Statsig-specific capabilities. Anything that drops or
	// approximates a Statsig feature is recorded via addLossy so the CLI can skip
	// it by default (overridable with --convert-lossy).
	// ---------------------------------------------------------------

	// Count distinct
	if len(sg.MetricEvents) > 0 && sg.MetricEvents[0].Type == "count_distinct" {
		result.addLossy("Statsig counts distinct %q values — LaunchDarkly will count all occurrences instead",
			sg.MetricEvents[0].MetadataKey)
	}

	// Metadata-based aggregation
	if len(sg.MetricEvents) > 0 && sg.MetricEvents[0].Type == "metadata" {
		result.addLossy("Statsig aggregates metadata field %q — LaunchDarkly will aggregate the track() metricValue; ensure events send the same value in metricValue",
			sg.MetricEvents[0].MetadataKey)
	}

	// Event criteria/filters — data-affecting: the migrated metric will be broader
	// than the original because filters are not carried over
	if len(sg.MetricEvents) > 0 && len(sg.MetricEvents[0].Criteria) > 0 {
		criteria := sg.MetricEvents[0].Criteria
		var details []string
		for _, c := range criteria {
			details = append(details, fmt.Sprintf("%s %s %s %v", c.Column, c.Type, c.Condition, c.Values))
		}
		result.addLossy("DATA LOSS: %d event filter criteria will NOT be applied — the LD metric will match all %q events, not just the filtered subset. Dropped filters: [%s]. Manual filter setup required in LD.",
			len(criteria), eventKey, strings.Join(details, "; "))
	}

	// Warehouse-native filters live on warehouseNative.criteria (or per source),
	// not metricEvents; drop them with the same DATA LOSS warning.
	if sg.IsWarehouseNative() {
		if crit := sg.NumeratorCriteria(); len(crit) > 0 {
			var details []string
			for _, c := range crit {
				details = append(details, fmt.Sprintf("%s %s %s %v", c.Column, c.Type, c.Condition, c.Values))
			}
			result.addLossy("DATA LOSS: %d warehouse-native filter criteria will NOT be applied — the LD metric will match all rows, not just the filtered subset. Dropped filters: [%s]. Manual filter setup required in LD.",
				len(crit), strings.Join(details, "; "))
		}
	}

	// Warehouse Native advanced features
	winsorLower, winsorUpper := winsorPercentiles(result, sg.WarehouseNative, isNumeric, unitAgg)
	warnUnsupportedWarehouseFeatures(result, sg.WarehouseNative)

	// Custom rollup windows are mapped to LD window offsets after the metric is
	// built (they depend on whether a data source is bound). See below.

	// Daily participation rate
	if sg.RollupTimeWindow == "daily_participation_rate" {
		result.addLossy("daily participation rate rollup is not supported in LaunchDarkly — metric will use standard binary conversion")
	}

	// ---------------------------------------------------------------
	// Success criteria
	// ---------------------------------------------------------------
	successCriteria := "HigherThanBaseline"
	if sg.Directionality == "decrease" {
		successCriteria = "LowerThanBaseline"
	}

	// ---------------------------------------------------------------
	// Randomization units: Statsig "userID" → LD "user" (see randomizationUnits)
	// ---------------------------------------------------------------
	randUnits, ruWarnings := randomizationUnits(sg, opts)
	result.Warnings = append(result.Warnings, ruWarnings...)

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
		Key:                  ldKey,
		Kind:                 "custom",
		Name:                 sg.Name,
		Description:          desc,
		EventKey:             eventKey,
		IsNumeric:            &isNumeric,
		SuccessCriteria:      successCriteria,
		UnitAggregationType:  unitAgg,
		UnitAggregationField: unitAggField,
		AnalysisType:         analysisType,
		RandomizationUnits:   randUnits,
		Unit:                 unit,
		Tags:                 tags,

		WinsorLowerPercentile: winsorLower,
		WinsorUpperPercentile: winsorUpper,
	}

	result.LDMetric.EventDefault = eventDefault

	// Data source: warehouse-native source, else top-level source name, else the
	// global default.
	if srcName := sg.NumeratorSourceName(); srcName != "" {
		dsKey := resolveDataSource(srcName, opts)
		if dsKey != "" {
			result.LDMetric.DataSource = &launchdarkly.DataSource{Key: dsKey}
		} else {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("no LD data source specified for Statsig source %q — metric created without data source binding", srcName))
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
	applyCustomWindow(result, sg)

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

// randomizationUnits maps Statsig unit types to LD randomization units (context
// kinds): "userID" → "user"; others pass through lowercased with an advisory
// warning unless remapped via --unit-type-mapping. Warehouse-native metrics
// often carry no unitTypes (the unit comes from the source's idTypeMapping), so
// an empty result defaults to "user".
func randomizationUnits(sg *statsig.Metric, opts Options) (units, warnings []string) {
	lowerMapping := lowerKeyMapping(opts.UnitTypeMapping)
	for _, u := range sg.UnitTypes {
		if mapped, ok := opts.UnitTypeMapping[u]; ok {
			units = append(units, mapped)
			continue
		}
		if mapped, ok := lowerMapping[strings.ToLower(u)]; ok {
			units = append(units, mapped)
			continue
		}
		switch strings.ToLower(u) {
		case "userid":
			units = append(units, "user")
		default:
			units = append(units, strings.ToLower(u))
			warnings = append(warnings,
				fmt.Sprintf("Statsig unitType %q may not match an LD context kind — verify in LD or use --unit-type-mapping", u))
		}
	}
	if len(units) == 0 {
		units = []string{"user"}
		warnings = append(warnings,
			"no Statsig unitTypes on the metric (common for warehouse-native, where the unit comes from the source's idTypeMapping) — defaulted the LD randomization unit to \"user\"; use --unit-type-mapping if that's wrong")
	}
	return units, warnings
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

// winsorPercentiles converts Statsig winsorization fractions (0–1) to LD
// percentiles (0–100). LD rejects winsorization on occurrence metrics
// (non-numeric average), where it records a lossy warning and returns no bounds.
func winsorPercentiles(result *Result, wn *statsig.WarehouseNative, isNumeric bool, unitAgg string) (lower, upper *float32) {
	if wn == nil || (wn.WinsorizationHigh == nil && wn.WinsorizationLow == nil) {
		return nil, nil
	}
	if !isNumeric && unitAgg == "average" {
		result.addLossy("winsorization (low=%s, high=%s) not applied — LaunchDarkly does not support winsorization on occurrence metrics",
			fmtOptFloat(wn.WinsorizationLow), fmtOptFloat(wn.WinsorizationHigh))
		return nil, nil
	}
	if wn.WinsorizationLow != nil {
		v := float32(*wn.WinsorizationLow * 100)
		lower = &v
	}
	if wn.WinsorizationHigh != nil {
		v := float32(*wn.WinsorizationHigh * 100)
		upper = &v
	}
	return lower, upper
}

// warnUnsupportedWarehouseFeatures flags Statsig warehouse-native features that
// LaunchDarkly can't represent. Value-affecting features (capping, log transform,
// value threshold) are lossy — they change the numbers, so the metric is skipped
// by default. Analysis-only features (CUPED, dimension columns, cohort wait,
// bake days) are advisory: they aren't replicated, but the core metric
// definition (aggregation/column/filter) still converts faithfully.
func warnUnsupportedWarehouseFeatures(result *Result, wn *statsig.WarehouseNative) {
	if wn == nil {
		return
	}
	if wn.Cap != nil {
		result.addLossy("per-unit capping (cap=%v) is not supported in LaunchDarkly", *wn.Cap)
	}
	if wn.UseLogTransform != nil && *wn.UseLogTransform {
		result.addLossy("log transform is not supported in LaunchDarkly — metric values will not be log-transformed")
	}
	if wn.ValueThreshold != nil {
		result.addLossy("value threshold (%v) is not supported in LaunchDarkly — the metric will not apply it", *wn.ValueThreshold)
	}

	var dropped []string
	if wn.CupedAttributionWindow != nil {
		dropped = append(dropped, "CUPED attribution window")
	}
	if len(wn.MetricDimensionColumns) > 0 {
		dropped = append(dropped, "metric dimension columns")
	}
	if wn.WaitForCohortWindow != nil && *wn.WaitForCohortWindow {
		dropped = append(dropped, "wait-for-cohort window")
	}
	if wn.MetricBakeDays != nil {
		dropped = append(dropped, "metric bake days")
	}
	if len(dropped) > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Statsig warehouse-native analysis features not carried over to LaunchDarkly (metric definition is unaffected): %s", strings.Join(dropped, ", ")))
	}
}

// applyCustomWindow maps a Statsig custom rollup window (days) to LD window
// offsets (ms). Cloud metrics carry the window at the metric top level;
// warehouse-native metrics carry it inside warehouseNative — read whichever
// applies. LD only accepts window offsets on warehouse-backed metrics, so it
// applies them only when a data source is bound, recording a lossy warning
// otherwise.
func applyCustomWindow(result *Result, sg *statsig.Metric) {
	rollup, start, end := sg.RollupTimeWindow, sg.CustomRollUpStart, sg.CustomRollUpEnd
	if sg.IsWarehouseNative() && sg.WarehouseNative != nil {
		wn := sg.WarehouseNative
		if wn.RollupTimeWindow != "" {
			rollup = wn.RollupTimeWindow
		}
		if wn.CustomRollUpStart != nil {
			start = wn.CustomRollUpStart
		}
		if wn.CustomRollUpEnd != nil {
			end = wn.CustomRollUpEnd
		}
	}
	if rollup != "custom" || start == nil || end == nil {
		return
	}
	if result.LDMetric.DataSource != nil {
		s := int64(*start * millisPerDay)
		e := int64(*end * millisPerDay)
		result.LDMetric.WindowStartOffset = &s
		result.LDMetric.WindowEndOffset = &e
	} else {
		result.addLossy("custom rollup window (days %v–%v) needs a warehouse (snowflake) data source in LaunchDarkly — not applied; pass --ld-data-source to enable it",
			*start, *end)
	}
}

// termSpec is the LD per-term shape derived from a Statsig metric type.
// Shared by the simple-metric path and ratio components.
type termSpec struct {
	isNumeric    bool
	unitAgg      string
	analysisType string
	eventDefault *launchdarkly.EventDefault
	// unitAggField is the column for a count_distinct aggregation. Only set by
	// the ratio path; LD requires it when unitAgg is "count_distinct".
	unitAggField string
}

// termSpecFor maps a Statsig metric type to its LD term shape. Returns
// IncompatibleError for unsupported types, including "ratio" — ratio
// components must themselves be simple metrics.
func termSpecFor(typ string) (termSpec, error) {
	switch typ {
	case "event_count_custom", "event_count", "count":
		// "count" is the warehouse-native spelling of event_count_custom.
		return termSpec{isNumeric: false, unitAgg: "sum", analysisType: "mean"}, nil
	case "count_distinct":
		// Warehouse-native count-distinct; the caller fills unitAggregationField
		// from the source valueColumn.
		return termSpec{isNumeric: false, unitAgg: "count_distinct", analysisType: "mean"}, nil
	case "daily_participation":
		// Statsig daily participation is a per-unit rate (active days / window
		// days). LD has no exact equivalent; approximate as a binary metric and
		// let the caller mark it lossy (skipped unless --convert-lossy), matching
		// how the daily-participation rollup is handled.
		return termSpec{isNumeric: false, unitAgg: "average", analysisType: "mean"}, nil
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

// ratioTermSpec maps a ratio component event's Statsig aggregation type to its
// LD term shape, reusing the simple-metric mapping via termSpecFor. Statsig
// ratio events use count / count_distinct / value / metadata aggregations.
// Returns any lossy-conversion warnings alongside the spec.
func ratioTermSpec(ev statsig.MetricEvent) (termSpec, []string, error) {
	var warnings []string
	switch ev.Type {
	case "count_distinct":
		// A named column → count(distinct <column>), which LaunchDarkly supports
		// only on warehouse-native metrics (not hosted). Map to count_distinct +
		// the field; on a hosted metric LD will reject it, which is correct — the
		// feature genuinely isn't available there.
		if ev.MetadataKey != "" {
			return termSpec{isNumeric: false, unitAgg: "count_distinct", analysisType: "mean", unitAggField: ev.MetadataKey}, nil, nil
		}
		// No column: a cloud count_distinct counts distinct units (users). The
		// LaunchDarkly equivalent is a binary metric — non-numeric with average
		// aggregation, which is exactly "count distinct of the analysis unit".
		// This is a faithful mapping, so no warning.
		spec, _ := termSpecFor("event_user")
		return spec, nil, nil
	case "metadata":
		warnings = append(warnings,
			fmt.Sprintf("Statsig aggregates metadata field %q — LaunchDarkly will aggregate the track() metricValue; ensure events send the same value in metricValue", ev.MetadataKey))
		spec, err := termSpecFor("sum")
		return spec, warnings, err
	case "count", "":
		spec, err := termSpecFor("event_count_custom")
		return spec, warnings, err
	case "value", "sum":
		spec, err := termSpecFor("sum")
		return spec, warnings, err
	case "mean":
		spec, err := termSpecFor("mean")
		return spec, warnings, err
	case "daily_participation":
		// Approximated as binary; convertRatio marks the whole ratio lossy.
		spec, err := termSpecFor("daily_participation")
		return spec, warnings, err
	default:
		return termSpec{}, nil, fmt.Errorf("unsupported ratio term aggregation %q on event %q", ev.Type, ev.Name)
	}
}

// convertRatio converts a Statsig (cloud) ratio metric. The Statsig Console API
// represents a ratio by carrying the numerator and denominator inline as
// metricEvents[0] and metricEvents[1]; metricComponentMetrics is for composite
// metrics, not ratios, and a ratio defined that way is rejected. The numerator's
// settings sit at the top level of the LD MetricPost; the denominator populates
// the Denominator subfield. Identity and shared fields (key, name, tags,
// randomizationUnits, etc.) come from the ratio metric.
func convertRatio(sg *statsig.Metric, opts Options) (*Result, error) {
	result := &Result{}

	// Extract both terms and their sources. Cloud ratios carry them inline as
	// metricEvents[0]/[1] sharing one source; warehouse-native ratios carry
	// per-term sources in warehouseNative (numerator in metricSources[0],
	// denominator in denominatorMetricSourceName).
	var numEv, denEv statsig.MetricEvent
	var numSrcName, denSrcName string

	if sg.IsWarehouseNative() {
		wn := sg.WarehouseNative
		if wn == nil {
			return nil, fmt.Errorf("warehouse-native ratio %q has no warehouseNative config", sg.Name)
		}
		// Top-level Aggregation is "ratio"; each term has its own column,
		// aggregation, and filters. The numerator uses the shared warehouseNative
		// value column/criteria; the denominator has denominator* fields.
		numEv = statsig.MetricEvent{Name: sg.NumeratorValueColumn(), Type: wn.NumeratorAggregation, Criteria: sg.NumeratorCriteria()}
		denEv = statsig.MetricEvent{Name: wn.DenominatorValueColumn, Type: wn.DenominatorAggregation, Criteria: wn.DenominatorCriteria}
		numSrcName = sg.NumeratorSourceName()
		denSrcName = wn.DenominatorMetricSourceName
	} else {
		if len(sg.MetricEvents) != 2 {
			return nil, fmt.Errorf("ratio metric %q expected 2 metric events (numerator + denominator), got %d", sg.Name, len(sg.MetricEvents))
		}
		// Statsig stores a cloud ratio's two events positionally: metricEvents[0]
		// is the DENOMINATOR and metricEvents[1] is the NUMERATOR (verified against
		// the Statsig console).
		denEv = sg.MetricEvents[0]
		numEv = sg.MetricEvents[1]
		if numEv.Name == "" {
			return nil, fmt.Errorf("ratio metric %q: its Statsig numerator event has no name, so there is no event key to map to the LaunchDarkly metric", sg.Name)
		}
		if denEv.Name == "" {
			return nil, fmt.Errorf("ratio metric %q: its Statsig denominator event has no name, so there is no event name to map to the LaunchDarkly denominator", sg.Name)
		}
		// Cloud ratios share one source across both terms.
		numSrcName = sg.MetricSourceName
		denSrcName = sg.MetricSourceName
	}

	numSpec, numWarn, err := ratioTermSpec(numEv)
	if err != nil {
		return nil, fmt.Errorf("ratio metric %q numerator: %w", sg.Name, err)
	}
	denSpec, denWarn, err := ratioTermSpec(denEv)
	if err != nil {
		return nil, fmt.Errorf("ratio metric %q denominator: %w", sg.Name, err)
	}
	for _, w := range numWarn {
		result.Warnings = append(result.Warnings, "numerator: "+w)
	}
	for _, w := range denWarn {
		result.Warnings = append(result.Warnings, "denominator: "+w)
	}

	if numEv.Type == "daily_participation" || denEv.Type == "daily_participation" {
		result.addLossy("Statsig daily_participation ratio term approximated as binary in LaunchDarkly — the per-day rate is lost")
	}

	// Event filter criteria are not carried over (parity with the simple path).
	if len(numEv.Criteria) > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("DATA LOSS: %d numerator event filter criteria will NOT be applied — the LD metric matches all %q events. Manual filter setup required in LD.", len(numEv.Criteria), numEv.Name))
	}
	if len(denEv.Criteria) > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("DATA LOSS: %d denominator event filter criteria will NOT be applied — the LD metric matches all %q events. Manual filter setup required in LD.", len(denEv.Criteria), denEv.Name))
	}

	ldKey := SanitizeKey(sg.ID)
	if ldKey == "" {
		return nil, fmt.Errorf("Statsig metric %q (id=%q) produces an empty LD key after sanitization", sg.Name, sg.ID)
	}
	if len(ldKey) > maxLDKeyLength {
		ldKey = ldKey[:maxLDKeyLength]
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("LD metric key truncated to %d characters (original Statsig ID was very long)", maxLDKeyLength))
	}

	successCriteria := "HigherThanBaseline"
	if sg.Directionality == "decrease" {
		successCriteria = "LowerThanBaseline"
	}

	randUnits, ruWarnings := randomizationUnits(sg, opts)
	result.Warnings = append(result.Warnings, ruWarnings...)

	desc := sg.Description
	if desc == "" {
		desc = fmt.Sprintf("Converted from Statsig ratio metric %q (numerator=%q, denominator=%q)", sg.Name, numEv.Name, denEv.Name)
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

	// Winsorization maps to the numerator term (LD's top-level percentile
	// fields). Statsig has a single metric-level setting; the denominator term's
	// winsorization is not separately expressible here.
	winsorLower, winsorUpper := winsorPercentiles(result, sg.WarehouseNative, numSpec.isNumeric, numSpec.unitAgg)
	warnUnsupportedWarehouseFeatures(result, sg.WarehouseNative)

	result.LDMetric = launchdarkly.MetricPost{
		Key:                  ldKey,
		Kind:                 "custom",
		Name:                 sg.Name,
		Description:          desc,
		EventKey:             numEv.Name,
		IsNumeric:            &numSpec.isNumeric,
		SuccessCriteria:      successCriteria,
		UnitAggregationType:  numSpec.unitAgg,
		UnitAggregationField: numSpec.unitAggField,
		AnalysisType:         numSpec.analysisType,
		RandomizationUnits:   randUnits,
		Unit:                 unit,
		Tags:                 tags,
		EventDefault:         numSpec.eventDefault,
		Denominator: &launchdarkly.DenominatorPost{
			EventName:            denEv.Name,
			IsNumeric:            denSpec.isNumeric,
			UnitAggregationType:  denSpec.unitAgg,
			UnitAggregationField: denSpec.unitAggField,
		},
		WinsorLowerPercentile: winsorLower,
		WinsorUpperPercentile: winsorUpper,
	}

	// Per-term data source. LD ratios reject creation without one (HTTP 400).
	// Numerator → top-level DataSource; denominator → its own DataSource, falling
	// back to the numerator's (cloud ratios share one source). resolveDataSource
	// applies --source-mapping then the --ld-data-source default.
	numDS := resolveDataSource(numSrcName, opts)
	if numDS != "" {
		result.LDMetric.DataSource = &launchdarkly.DataSource{Key: numDS}
	} else {
		result.Warnings = append(result.Warnings,
			"LaunchDarkly ratio metrics require a warehouse data source for the numerator — none resolved; pass --ld-data-source <key> (or --source-mapping) or LD will reject creation (HTTP 400)")
	}

	denDS := resolveDataSource(denSrcName, opts)
	if denDS == "" {
		denDS = numDS
	}
	if denDS != "" && result.LDMetric.Denominator != nil {
		result.LDMetric.Denominator.DataSource = &launchdarkly.DataSource{Key: denDS}
	}

	// Custom rollup window applies to the whole ratio metric; resolve after the
	// data source is bound (LD gates window offsets on a warehouse data source).
	applyCustomWindow(result, sg)

	return result, nil
}
