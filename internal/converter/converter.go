// Package converter provides pure conversion logic from Statsig metrics to
// LaunchDarkly metrics. It has no IO dependencies, making it portable into
// other codebases (e.g. Gonfalon backend for a future UI importer).
package converter

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
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

	// SourceUnitTypes maps a Statsig metric source name to the unit types (id
	// types) declared on that source. Warehouse-native metrics frequently omit
	// unitTypes on the metric itself; the analysis unit comes from the source's
	// id-type mapping. When a metric has no unitTypes, the converter falls back
	// to its source's unit types here before defaulting to "user".
	SourceUnitTypes map[string][]string

	// WidenAnalysisUnits unions a metric's own unitTypes with the id types its
	// metric source declares, rather than letting its own unitTypes win outright.
	// A warehouse-native metric's units come from its source: Statsig supports a
	// metric at several units of analysis as long as the source maps those id
	// types. Inert for cloud metrics, which carry their own unitTypes and have no
	// warehouse metric source to widen from.
	WidenAnalysisUnits bool

	// ExtraAnalysisUnits are LD context kinds added to every converted metric,
	// for units with no Statsig counterpart the converter can see.
	ExtraAnalysisUnits []string
}

// Warning codes are stable identifiers for the diagnostics the converter emits.
// They travel into the migration report next to the human message, so a reader
// can aggregate outcomes with a tool instead of pattern-matching prose, and so
// rewording a message does not silently break someone's analysis. Codes also
// survive the redaction customers apply before sharing a report, where free text
// often does not.
const (
	WarnDailyParticipationRate   = "daily_participation_rate_approximated"
	WarnNoValueColumn            = "whn_no_value_column"
	WarnCountDistinctApproximate = "count_distinct_approximated"
	WarnMultipleMetricEvents     = "multiple_metric_events"
	WarnCountDistinctEvent       = "count_distinct_event_metric"
	WarnMetadataAggregation      = "metadata_aggregation"
	WarnKeyTruncated             = "ld_key_truncated"
	WarnNoDataSourceForSource    = "no_data_source_for_source"
	WarnUnitTypeUnmapped         = "unit_type_unmapped"
	WarnAnalysisUnitFromSource   = "analysis_unit_from_source"
	WarnAnalysisUnitDefaulted    = "analysis_unit_defaulted"
	WarnAnalysisUnitsWidened     = "analysis_units_widened"
	WarnWinsorizationNotApplied  = "winsorization_not_applied"
	WarnPerUnitCapDropped        = "per_unit_cap_dropped"
	WarnLogTransformDropped      = "log_transform_dropped"
	WarnValueThresholdDropped    = "value_threshold_dropped"
	WarnAnalysisFeaturesDropped  = "whn_analysis_features_dropped"
	WarnWindowNoDataSource       = "window_no_data_source"
	WarnDailyParticipationRatio  = "daily_participation_ratio_term"
	WarnRatioNoDataSource        = "ratio_no_data_source"

	// Filter conversion.
	WarnFilterApplied            = "filter_applied"
	WarnFilterNoDataSource       = "filter_no_data_source"
	WarnFilterCloudUnsupported   = "filter_cloud_metric_unsupported"
	WarnFilterConditionBlocked   = "filter_condition_unsupported"
	WarnFilterDuplicateLocations = "filter_criteria_in_two_locations"
)

// FilterOutcome records what happened to one metric term's Statsig filter
// criteria. A ratio metric produces one per term, so a reader can see that (say)
// the numerator converted while the denominator was blocked. Emitted into the
// migration report so filter results can be counted directly rather than
// recovered by parsing warning text.
type FilterOutcome struct {
	// Term is which part of the metric this covers: "warehouse-native" or "event"
	// for a simple metric, "numerator" or "denominator" for a ratio.
	Term string
	// Criteria is how many Statsig criteria the term carried.
	Criteria int
	// Applied reports whether a LaunchDarkly filter was emitted for the term.
	Applied bool
	// BlockedBy is a stable code for why no filter was emitted. Empty when applied.
	BlockedBy string
	// BlockedCondition is the Statsig condition responsible, when one is. Empty
	// when the term was blocked for a reason other than a specific condition.
	BlockedCondition string
}

// codedWarning pairs a stable code with its human message, for the helpers that
// build warnings before a Result exists.
type codedWarning struct {
	code string
	msg  string
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

	// WarningCodes runs parallel to Warnings: WarningCodes[i] is the stable code
	// for Warnings[i]. LossyCodes does the same for LossyReasons. The two
	// add* helpers below are the only writers, which is what keeps them aligned —
	// never append to these slices directly.
	WarningCodes []string
	LossyCodes   []string

	// FilterOutcomes records one entry per metric term that carried filter
	// criteria, whether or not a filter was produced.
	FilterOutcomes []FilterOutcome
}

// addWarning records an advisory message. It does NOT mark the conversion lossy,
// so the metric still converts by default.
func (r *Result) addWarning(code, format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
	r.WarningCodes = append(r.WarningCodes, code)
}

// addLossy records a message that both warns the user and marks the conversion
// as lossy (a Statsig feature was dropped or approximated). The message is added
// to both Warnings and LossyReasons.
func (r *Result) addLossy(code, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.Warnings = append(r.Warnings, msg)
	r.WarningCodes = append(r.WarningCodes, code)
	r.LossyReasons = append(r.LossyReasons, msg)
	r.LossyCodes = append(r.LossyCodes, code)
}

// addCoded records an already-built warning from a helper that had no Result.
func (r *Result) addCoded(prefix string, ws ...codedWarning) {
	for _, w := range ws {
		r.addWarning(w.code, "%s%s", prefix, w.msg)
	}
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

	// Daily participation RATE is a per-unit fraction of active days, which
	// LaunchDarkly has no aggregation for; approximating it as a binary
	// (participated-or-not) metric drops the rate — lossy. In Statsig's unit-count
	// participation family (warehouse-native "daily_participation" and cloud
	// "event_user"), the rate is the DEFAULT rollup: it appears as an unset
	// rollupTimeWindow, "daily" (warehouse-native), or "daily_participation_rate"
	// (cloud, legacy). Only the explicit "max" (one-time) and "custom" (windowed)
	// rollups are a per-unit binary flag that converts to a LaunchDarkly binary
	// metric with no loss. (A "custom" window is handled separately below and is
	// only lossy when left unbound.)
	if effectiveType == "daily_participation" || effectiveType == "event_user" {
		switch sg.EffectiveRollupTimeWindow() {
		case "", "daily", "daily_participation_rate":
			result.addLossy(WarnDailyParticipationRate, "Statsig daily participation rate (per-unit fraction of active days) has no exact LaunchDarkly equivalent — approximated as a binary metric, which loses the per-day rate")
		}
	}

	// Event key resolution:
	//   1. metricEvents[0].Name — cloud metrics with explicit events.
	//   2. warehouse-native value column — the flat warehouseNative.valueColumn or
	//      metricSources[0].valueColumn (both forms occur; see NumeratorValueColumn).
	//   3. lineage.events[0] — built-in event_count metrics, which have no
	//      metricEvents and carry the counted event in lineage.
	var eventKey string
	if len(sg.MetricEvents) > 0 {
		eventKey = sg.MetricEvents[0].Name
	} else if sg.IsWarehouseNative() {
		if col := sg.NumeratorValueColumn(); col != "" {
			eventKey = col
			// count_distinct needs the column as unitAggregationField; sum / mean /
			// count convey the value through eventKey only and must not set it.
			if unitAgg == "count_distinct" && unitAggField == "" {
				unitAggField = col
			}
		} else if src := sg.NumeratorSourceName(); src != "" {
			// count / daily_participation / user aggregations count rows or units
			// and carry no value column. LD still needs an event key, so use the
			// source name as a provisional stand-in pending live LD verification.
			eventKey = src
			result.addWarning(WarnNoValueColumn,
				"warehouse-native %q metric has no value column (it counts rows/units); LD eventKey set to the source name %q provisionally — verify against a created LD metric", effectiveType, src)
		}
	} else if len(sg.Lineage.Events) > 0 {
		eventKey = sg.Lineage.Events[0]
	}
	if eventKey == "" {
		if sg.IsWarehouseNative() {
			return nil, fmt.Errorf("warehouse-native metric %q: cannot determine LD eventKey — no valueColumn or metric source on warehouseNative", sg.Name)
		}
		return nil, fmt.Errorf("Statsig metric %q has no metricEvents or lineage events — cannot determine LD eventKey", sg.Name)
	}

	// LaunchDarkly supports unitAggregationType=count_distinct ONLY on ratio
	// metrics (live-confirmed: HTTP 400 "count_distinct is only supported for
	// ratio metrics"). A non-ratio count_distinct maps to a binary metric —
	// faithful when it counts distinct units (no column), lossy when it counts
	// distinct values of a column (that per-value count can't be expressed).
	if unitAgg == "count_distinct" {
		hadColumn := unitAggField != ""
		isNumeric = false
		unitAgg = "average"
		unitAggField = ""
		if hadColumn {
			result.addLossy(WarnCountDistinctApproximate, "LaunchDarkly supports count_distinct only on ratio metrics; this non-ratio metric is approximated as a binary metric, losing the distinct-value count")
		}
	}

	// Warn if multiple metric events — only the first is used (lossy: the
	// additional events are dropped).
	if len(sg.MetricEvents) > 1 {
		result.addLossy(WarnMultipleMetricEvents, "Statsig metric has %d metric events — only the first (%q) is used; %d additional events are ignored",
			len(sg.MetricEvents), eventKey, len(sg.MetricEvents)-1)
	}

	// ---------------------------------------------------------------
	// Feature warnings for Statsig-specific capabilities. Anything that drops or
	// approximates a Statsig feature is recorded via addLossy so the CLI can skip
	// it by default (overridable with --convert-lossy).
	// ---------------------------------------------------------------

	// Count distinct
	if len(sg.MetricEvents) > 0 && sg.MetricEvents[0].Type == "count_distinct" {
		result.addLossy(WarnCountDistinctEvent, "Statsig counts distinct %q values — LaunchDarkly will count all occurrences instead",
			sg.MetricEvents[0].MetadataKey)
	}

	// Metadata-based aggregation
	if len(sg.MetricEvents) > 0 && sg.MetricEvents[0].Type == "metadata" {
		result.addLossy(WarnMetadataAggregation, "Statsig aggregates metadata field %q — LaunchDarkly will aggregate the track() metricValue; ensure events send the same value in metricValue",
			sg.MetricEvents[0].MetadataKey)
	}

	// Filter criteria. Warehouse-native criteria live on warehouseNative.criteria
	// (or per source); cloud criteria live on metricEvents. Both are resolved into
	// a LaunchDarkly metric filter further down, once the data source is known —
	// LaunchDarkly only interprets a column filter as a warehouse column when the
	// metric is bound to a data source. See convertTermCriteria.
	var termCriteria []statsig.Criterion
	termCriteriaLabel := "event"
	if sg.IsWarehouseNative() {
		termCriteria = sg.NumeratorCriteria()
		termCriteriaLabel = "warehouse-native"
		// A warehouse-native metric carries its criteria on warehouseNative, but if
		// any also show up on metricEvents they must not be dropped in silence —
		// an unreported dropped filter is the one outcome this conversion must never
		// produce, because the metric then measures more than it claims to.
		if len(sg.MetricEvents) > 0 && len(sg.MetricEvents[0].Criteria) > 0 {
			result.addLossy(WarnFilterDuplicateLocations, "DATA LOSS: %s on metricEvents %s not applied. This warehouse-native metric carries filter criteria in two places and only the warehouseNative ones convert. Dropped filters: [%s]. Add them by hand in LaunchDarkly if you need them.",
				criteriaPhrase(len(sg.MetricEvents[0].Criteria), "event"),
				wasWere(len(sg.MetricEvents[0].Criteria)),
				criteriaDetail(sg.MetricEvents[0].Criteria))
			result.FilterOutcomes = append(result.FilterOutcomes, FilterOutcome{
				Term:      "event",
				Criteria:  len(sg.MetricEvents[0].Criteria),
				BlockedBy: FilterBlockedDuplicateLocation,
			})
		}
	} else if len(sg.MetricEvents) > 0 {
		termCriteria = sg.MetricEvents[0].Criteria
	}

	// Warehouse Native advanced features
	winsorLower, winsorUpper := winsorPercentiles(result, sg.WarehouseNative, isNumeric, unitAgg)
	warnUnsupportedWarehouseFeatures(result, sg.WarehouseNative)

	// Custom rollup windows are mapped to LD window offsets after the metric is
	// built (they depend on whether a data source is bound). See below.
	//
	// (The daily-participation-rate rollup is handled at the aggregation check
	// above via EffectiveRollupTimeWindow.)

	// ---------------------------------------------------------------
	// Success criteria
	// ---------------------------------------------------------------
	successCriteria := "HigherThanBaseline"
	if sg.Directionality == "decrease" {
		successCriteria = "LowerThanBaseline"
	}

	// ---------------------------------------------------------------
	// Analysis units: Statsig "userID" → LD "user" (see analysisUnits)
	// ---------------------------------------------------------------
	units, unitWarnings := analysisUnits(sg, opts)
	result.addCoded("", unitWarnings...)

	// ---------------------------------------------------------------
	// LD metric key: derived from Statsig ID (name::type) for idempotency
	// ---------------------------------------------------------------
	ldKey := SanitizeKey(sg.ID)
	if ldKey == "" {
		return nil, fmt.Errorf("Statsig metric %q (id=%q) produces an empty LD key after sanitization", sg.Name, sg.ID)
	}
	if len(ldKey) > maxLDKeyLength {
		ldKey = ldKey[:maxLDKeyLength]
		result.addWarning(WarnKeyTruncated,
			"LD metric key truncated to %d characters (original Statsig ID was very long)", maxLDKeyLength)
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
		AnalysisUnits:        units,
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
			result.addWarning(WarnNoDataSourceForSource,
				"no LD data source specified for Statsig source %q — metric created without data source binding", srcName)
		}
	} else if opts.LDDataSource != "" {
		// No explicit source name on the metric, but a global default was provided
		result.LDMetric.DataSource = &launchdarkly.DataSource{Key: opts.LDDataSource}
	}

	// ---------------------------------------------------------------
	// Metric filters: map Statsig filter criteria to a LaunchDarkly metric filter.
	// Resolved here, after the data source binding, because a filter is only
	// meaningful once LaunchDarkly knows the metric reads warehouse columns.
	// ---------------------------------------------------------------
	result.LDMetric.Filters = convertTermCriteria(
		result, termCriteriaLabel, termCriteria,
		sg.IsWarehouseNative(), result.LDMetric.DataSource != nil)

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

// analysisUnits builds the list of context kinds an experiment may analyze a
// metric by, picking one per metric at experiment creation. Contributions, in
// order: the metric's own unitTypes, the id types its metric source declares
// (a fallback unless Options.WidenAnalysisUnits), then
// Options.ExtraAnalysisUnits. Duplicates drop out, first occurrence wins.
func analysisUnits(sg *statsig.Metric, opts Options) (units []string, warnings []codedWarning) {
	var fromSource []string
	if src := sg.NumeratorSourceName(); src != "" {
		fromSource = opts.SourceUnitTypes[src]
	}

	own, ownWarnings := mapUnitTypes(sg.UnitTypes, opts)
	units, warnings = own, ownWarnings

	// Warehouse-native metrics usually omit unitTypes, carrying the unit on the source.
	usedSourceAsFallback := len(own) == 0 && len(fromSource) > 0
	if usedSourceAsFallback || (opts.WidenAnalysisUnits && len(fromSource) > 0) {
		sourceUnits, sourceWarnings := mapUnitTypes(fromSource, opts)
		units = appendUnique(units, sourceUnits...)
		warnings = appendCodedUnique(warnings, sourceWarnings...)
	}

	switch {
	case usedSourceAsFallback:
		warnings = append(warnings, codedWarning{WarnAnalysisUnitFromSource,
			fmt.Sprintf("metric has no unitTypes of its own; used the metric source's analysis unit(s) %v (from its id-type mapping)", fromSource)})
	case len(units) > len(own):
		warnings = append(warnings, codedWarning{WarnAnalysisUnitsWidened,
			fmt.Sprintf("analysis units widened from %v to %v using the metric source's id-type mapping; pass --widen-analysis-units=false to keep only the metric's own unit types", own, units)})
	}

	// Already LD context kinds, so they bypass the unit-type mapping.
	units = appendUnique(units, opts.ExtraAnalysisUnits...)

	if len(units) == 0 {
		units = []string{"user"}
		warnings = append(warnings, codedWarning{WarnAnalysisUnitDefaulted,
			"no Statsig unitTypes on the metric and none resolvable from its source — defaulted the LD analysis unit to \"user\"; pass --unit-type-mapping or check the metric source's id-type mapping if that's wrong"})
	}
	return units, warnings
}

// mapUnitTypes converts Statsig unit types to LD context kinds, preserving
// order and dropping duplicates: "userID" → "user"; others pass through
// lowercased with an advisory warning unless remapped via --unit-type-mapping.
func mapUnitTypes(unitTypes []string, opts Options) (units []string, warnings []codedWarning) {
	lowerMapping := lowerKeyMapping(opts.UnitTypeMapping)
	for _, u := range unitTypes {
		if mapped, ok := opts.UnitTypeMapping[u]; ok {
			units = appendUnique(units, mapped)
			continue
		}
		if mapped, ok := lowerMapping[strings.ToLower(u)]; ok {
			units = appendUnique(units, mapped)
			continue
		}
		switch strings.ToLower(u) {
		case "userid":
			units = appendUnique(units, "user")
		default:
			units = appendUnique(units, strings.ToLower(u))
			warnings = appendCodedUnique(warnings, codedWarning{WarnUnitTypeUnmapped,
				fmt.Sprintf("Statsig unitType %q may not match an LD context kind — verify in LD or use --unit-type-mapping", u)})
		}
	}
	return units, warnings
}

// appendCodedUnique appends the warnings whose message is not already in dst,
// preserving order.
func appendCodedUnique(dst []codedWarning, values ...codedWarning) []codedWarning {
	for _, v := range values {
		if !slices.ContainsFunc(dst, func(w codedWarning) bool { return w.msg == v.msg }) {
			dst = append(dst, v)
		}
	}
	return dst
}

// appendUnique appends the values not already in dst, preserving order.
func appendUnique(dst []string, values ...string) []string {
	for _, v := range values {
		if v == "" {
			continue
		}
		if !slices.Contains(dst, v) {
			dst = append(dst, v)
		}
	}
	return dst
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
		result.addLossy(WarnWinsorizationNotApplied, "winsorization (low=%s, high=%s) not applied — LaunchDarkly does not support winsorization on occurrence metrics",
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
		result.addLossy(WarnPerUnitCapDropped, "per-unit capping (cap=%v) is not supported in LaunchDarkly", *wn.Cap)
	}
	if wn.UseLogTransform != nil && *wn.UseLogTransform {
		result.addLossy(WarnLogTransformDropped, "log transform is not supported in LaunchDarkly — metric values will not be log-transformed")
	}
	if wn.ValueThreshold != nil {
		result.addLossy(WarnValueThresholdDropped, "value threshold (%v) is not supported in LaunchDarkly — the metric will not apply it", *wn.ValueThreshold)
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
		result.addWarning(WarnAnalysisFeaturesDropped,
			"Statsig warehouse-native analysis features not carried over to LaunchDarkly (metric definition is unaffected): %s", strings.Join(dropped, ", "))
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
		result.addLossy(WarnWindowNoDataSource, "custom rollup window (days %v–%v) needs a warehouse (snowflake) data source in LaunchDarkly — not applied; pass --ld-data-source to enable it",
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
func ratioTermSpec(ev statsig.MetricEvent) (termSpec, []codedWarning, error) {
	var warnings []codedWarning
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
		warnings = append(warnings, codedWarning{WarnMetadataAggregation,
			fmt.Sprintf("Statsig aggregates metadata field %q — LaunchDarkly will aggregate the track() metricValue; ensure events send the same value in metricValue", ev.MetadataKey)})
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
// analysisUnits, etc.) come from the ratio metric.
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
	result.addCoded("numerator: ", numWarn...)
	result.addCoded("denominator: ", denWarn...)

	if numEv.Type == "daily_participation" || denEv.Type == "daily_participation" {
		result.addLossy(WarnDailyParticipationRatio, "Statsig daily_participation ratio term approximated as binary in LaunchDarkly — the per-day rate is lost")
	}

	// Per-term filter criteria are resolved further down, once each term's data
	// source is known. See convertTermCriteria.

	ldKey := SanitizeKey(sg.ID)
	if ldKey == "" {
		return nil, fmt.Errorf("Statsig metric %q (id=%q) produces an empty LD key after sanitization", sg.Name, sg.ID)
	}
	if len(ldKey) > maxLDKeyLength {
		ldKey = ldKey[:maxLDKeyLength]
		result.addWarning(WarnKeyTruncated,
			"LD metric key truncated to %d characters (original Statsig ID was very long)", maxLDKeyLength)
	}

	successCriteria := "HigherThanBaseline"
	if sg.Directionality == "decrease" {
		successCriteria = "LowerThanBaseline"
	}

	units, unitWarnings := analysisUnits(sg, opts)
	result.addCoded("", unitWarnings...)

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
		AnalysisUnits:        units,
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
		result.addWarning(WarnRatioNoDataSource,
			"LaunchDarkly ratio metrics require a warehouse data source for the numerator — none resolved; pass --ld-data-source <key> (or --source-mapping) or LD will reject creation (HTTP 400)")
	}

	denDS := resolveDataSource(denSrcName, opts)
	if denDS == "" {
		denDS = numDS
	}
	if denDS != "" && result.LDMetric.Denominator != nil {
		result.LDMetric.Denominator.DataSource = &launchdarkly.DataSource{Key: denDS}
	}

	// ---------------------------------------------------------------
	// Per-term metric filters. Each term carries its own criteria and its own data
	// source, so each is converted independently: one term can get its filter
	// while the other stays lossy.
	// ---------------------------------------------------------------
	result.LDMetric.Filters = convertTermCriteria(
		result, "numerator", numEv.Criteria, sg.IsWarehouseNative(), numDS != "")
	if result.LDMetric.Denominator != nil {
		result.LDMetric.Denominator.Filters = convertTermCriteria(
			result, "denominator", denEv.Criteria, sg.IsWarehouseNative(), denDS != "")
	}

	// Custom rollup window applies to the whole ratio metric; resolve after the
	// data source is bound (LD gates window offsets on a warehouse data source).
	applyCustomWindow(result, sg)

	return result, nil
}
