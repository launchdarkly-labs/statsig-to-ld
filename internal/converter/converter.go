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
	// unitAggField is the column for a count_distinct aggregation. Only set by
	// the ratio path; LD requires it when unitAgg is "count_distinct".
	unitAggField string
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
	default:
		return termSpec{}, nil, fmt.Errorf("unsupported ratio term aggregation %q on event %q", ev.Type, ev.Name)
	}
}

// convertRatio converts a Statsig (cloud) ratio metric. The numerator and
// denominator are carried inline as metricEvents[0] and metricEvents[1] — this
// is how the Statsig Console API actually represents a ratio (verified against
// the live API). Statsig does NOT use metricComponentMetrics for ratios (that
// field is for composite metrics) and rejects a ratio defined that way. The
// numerator's settings sit at the top level of the LD MetricPost; the
// denominator populates the Denominator subfield. Identity and shared fields
// (key, name, tags, randomizationUnits, etc.) come from the ratio metric.
func convertRatio(sg *statsig.Metric, opts Options) (*Result, error) {
	if len(sg.MetricEvents) != 2 {
		return nil, fmt.Errorf("ratio metric %q expected 2 metric events (numerator + denominator), got %d", sg.Name, len(sg.MetricEvents))
	}
	result := &Result{}

	numEv := sg.MetricEvents[0]
	denEv := sg.MetricEvents[1]
	if numEv.Name == "" {
		return nil, fmt.Errorf("ratio metric %q: its Statsig numerator event has no name, so there is no event key to map to the LaunchDarkly metric", sg.Name)
	}
	if denEv.Name == "" {
		return nil, fmt.Errorf("ratio metric %q: its Statsig denominator event has no name, so there is no event name to map to the LaunchDarkly denominator", sg.Name)
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
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Statsig unitType %q may not match an LD context kind — verify in LD or use --unit-type-mapping", u))
		}
	}

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
	}

	// Data source resolution. LaunchDarkly ratio metrics are warehouse-native:
	// the API rejects a ratio without a data source ("Ratio metrics require a
	// warehouse data source", HTTP 400). Resolve from the Statsig source name
	// (mapped) or the --ld-data-source default; if neither yields one, warn at
	// convert time — surfaced even in dry-run — so the user supplies one rather
	// than hitting the 400 at create time.
	if dsKey := resolveDataSource(sg.MetricSourceName, opts); dsKey != "" {
		result.LDMetric.DataSource = &launchdarkly.DataSource{Key: dsKey}
	} else {
		result.Warnings = append(result.Warnings,
			"LaunchDarkly ratio metrics require a warehouse data source — none resolved; pass --ld-data-source <key> (or --source-mapping) or LD will reject creation (HTTP 400)")
	}

	return result, nil
}
