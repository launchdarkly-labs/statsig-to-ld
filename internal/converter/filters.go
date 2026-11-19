package converter

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

// LaunchDarkly metric filter operators. LaunchDarkly has no "equals"; equality is
// expressed as "in" with a single value. "exists" is a presence check and is the
// only operator that takes no values: negate=false means the column has a value,
// negate=true means it does not.
const (
	ldOpIn                 = "in"
	ldOpContains           = "contains"
	ldOpStartsWith         = "startsWith"
	ldOpEndsWith           = "endsWith"
	ldOpLessThan           = "lessThan"
	ldOpLessThanOrEqual    = "lessThanOrEqual"
	ldOpGreaterThan        = "greaterThan"
	ldOpGreaterThanOrEqual = "greaterThanOrEqual"
	ldOpExists             = "exists"
)

// LaunchDarkly's own validation limits on a metric filter. Exceeding either is a
// rejected save, so the converter checks them before emitting.
const (
	ldFilterMaxLeaves    = 10
	ldFilterMaxStringLen = 1024
)

// Statsig criterion types. Only "metadata" filters on a data column, which is
// what a LaunchDarkly eventProperty filter needs. "user" and "user_custom" are
// context-attribute filters, which LaunchDarkly rejects on warehouse-native
// metrics outright.
const statsigCriterionTypeMetadata = "metadata"

// unsupportedCriteriaError explains why a term's criteria could not be converted.
// It names the offending condition so the operator can see exactly what blocked
// the metric rather than a generic failure.
type unsupportedCriteriaError struct {
	// Code is a stable identifier for the class of problem, for aggregation in the
	// migration report. Condition names the Statsig condition responsible, when
	// one is; some problems (too many clauses) are not tied to a single condition.
	Code      string
	Condition string
	Reason    string
}

func (e *unsupportedCriteriaError) Error() string {
	if e.Condition == "" {
		return e.Reason
	}
	return fmt.Sprintf("Statsig condition %q cannot be converted: %s", e.Condition, e.Reason)
}

func unsupported(condition, reason string) error {
	return &unsupportedCriteriaError{Code: FilterBlockedCondition, Condition: condition, Reason: reason}
}

// unsupportedCoded is unsupported() with an explicit code, for problems that are
// not "this condition has no equivalent".
func unsupportedCoded(code, condition, reason string) error {
	return &unsupportedCriteriaError{Code: code, Condition: condition, Reason: reason}
}

// Codes for why a term's filter could not be produced. They appear as
// FilterOutcome.BlockedBy in the migration report.
const (
	FilterBlockedCondition      = "unsupported_condition"
	FilterBlockedCriterionType  = "unsupported_criterion_type"
	FilterBlockedNullOverride   = "null_vacuous_override"
	FilterBlockedMissingColumn  = "missing_column"
	FilterBlockedValue          = "invalid_value"
	FilterBlockedValueCount     = "wrong_value_count"
	FilterBlockedTooManyClauses = "too_many_clauses"
	FilterBlockedNoDataSource   = "no_data_source"
	FilterBlockedCloudMetric    = "cloud_metric"
	// FilterBlockedDuplicateLocation means the metric carried criteria on both
	// warehouseNative and metricEvents, and only the warehouseNative ones convert.
	FilterBlockedDuplicateLocation = "criteria_in_two_locations"
	// FilterBlockedSQLFilterColumn means the data source lacks the column that holds
	// a Statsig SQL filter's result.
	FilterBlockedSQLFilterColumn = "sql_filter_column_missing"
)

// SQLFilters lists, by Statsig source name, the SQL filters of the metrics this
// package converts that need a column, each once, in the order first seen. A filter
// written as LaunchDarkly clauses needs none.
func SQLFilters(metrics []statsig.Metric) map[string][]string {
	out := map[string][]string{}
	add := func(source string, criteria []statsig.Criterion) {
		if source == "" {
			return
		}
		translated := sqlFilterTranslations(criteria)
		for i, c := range criteria {
			if _, done := translated[i]; done {
				continue
			}
			if sql, ok := c.SQLFilter(); ok && !slices.Contains(out[source], sql) {
				out[source] = append(out[source], sql)
			}
		}
	}
	for i := range metrics {
		m := &metrics[i]
		if !m.IsWarehouseNative() || !m.HasWarehouseAggregation() {
			continue
		}
		num := m.NumeratorSourceName()
		if m.EffectiveType() == "ratio" {
			add(num, m.NumeratorCriteria())
			den := m.WarehouseNative.DenominatorMetricSourceName
			if den == "" {
				den = num
			}
			add(den, m.WarehouseNative.DenominatorCriteria)
			continue
		}
		if _, err := termSpecFor(m.EffectiveType()); err == nil {
			add(num, m.NumeratorCriteria())
		}
	}
	return out
}

// criteriaToFilter converts one term's Statsig filter criteria into a
// LaunchDarkly metric filter.
//
// Statsig combines multiple criteria on a term with AND. Its API carries no
// combinator field, so that is inferred rather than read: Statsig's filter UI
// offers no way to OR two filter rows together, metrics in the wild never repeat a
// column within one term, and Statsig already expresses OR *inside* a criterion as
// several values on one condition. That matches how LaunchDarkly models filters,
// as a group of clauses combined with "and".
//
// It is all-or-nothing on purpose. If any single criterion cannot be mapped
// faithfully, the whole term is rejected and the caller emits no filter at all.
// Applying only the criteria we understand would be worse than applying none:
// because they are AND-ed, dropping one WIDENS the matched set, so the metric
// would look converted, carry a filter, and quietly count more rows than the
// original. A withheld metric is a visible gap; a silently widened one is a wrong
// answer that survives review.
//
// translated holds the clauses that replace SQL filters, by criterion index, and
// filterCols the SQL filter columns of the data source's query.
//
// Returns (nil, nil) when there are no criteria.
func criteriaToFilter(criteria []statsig.Criterion, translated map[int][]launchdarkly.EventFilter, filterCols []warehouse.CustomField) (*launchdarkly.EventFilter, error) {
	if len(criteria) == 0 {
		return nil, nil
	}
	if len(criteria) > ldFilterMaxLeaves {
		return nil, unsupportedCoded(FilterBlockedTooManyClauses, "", fmt.Sprintf(
			"this metric has %d filter criteria, but LaunchDarkly allows at most %d",
			len(criteria), ldFilterMaxLeaves))
	}

	leaves := make([]launchdarkly.EventFilter, 0, len(criteria))
	for i, c := range criteria {
		leaf, err := criterionToLeaf(c)
		if err != nil {
			return nil, err
		}
		if clauses, ok := translated[i]; ok {
			leaves = append(leaves, clauses...)
			continue
		}
		if sql, ok := c.SQLFilter(); ok {
			leaf.Attribute = sqlFilterColumn(sql, filterCols)
		}
		leaves = append(leaves, *leaf)
	}

	// A single criterion needs no wrapper. Emitting a bare leaf keeps the payload
	// minimal and matches what LaunchDarkly's own UI produces.
	if len(leaves) == 1 {
		return &leaves[0], nil
	}

	// Preserve source order so output is deterministic and golden tests are stable.
	// A group node must not carry attribute, contextKind, or negate.
	values := make([]any, 0, len(leaves))
	for _, l := range leaves {
		values = append(values, l)
	}
	return &launchdarkly.EventFilter{
		Type:   launchdarkly.EventFilterTypeGroup,
		Op:     launchdarkly.EventFilterGroupOpAnd,
		Values: values,
	}, nil
}

// criterionToLeaf maps a single Statsig criterion to a LaunchDarkly eventProperty
// clause, or returns an error explaining why it cannot be mapped.
func criterionToLeaf(c statsig.Criterion) (*launchdarkly.EventFilter, error) {
	// nullVacuousOverride changes how Statsig treats nulls when evaluating the
	// criterion. LaunchDarkly has no equivalent knob, so honoring the criterion
	// without it would silently change which rows match.
	if c.NullVacuousOverride != nil {
		return nil, unsupportedCoded(FilterBlockedNullOverride, c.Condition,
			"it sets nullVacuousOverride to change how empty values are treated, and LaunchDarkly has no equivalent setting, so the filter would match a different set of rows")
	}

	// Only column filters map. "user" / "user_custom" are context-attribute
	// filters, which LaunchDarkly rejects on warehouse-native metrics, and "value"
	// filters on the metric value rather than a column.
	if c.Type != "" && c.Type != statsigCriterionTypeMetadata {
		return nil, unsupportedCoded(FilterBlockedCriterionType, c.Condition, fmt.Sprintf(
			"it filters on %q, but a warehouse-native metric in LaunchDarkly can only filter on a column from its data source", c.Type))
	}

	// These carry no column, or can never map, so they are handled before the
	// column check.
	switch c.Condition {
	// statsig-to-ld warehouse adds each SQL filter to its data source's query as a
	// true/false column, so the filter becomes a check on that column.
	case "sql_filter":
		sql, ok := c.SQLFilter()
		if !ok {
			reason := fmt.Sprintf("it should carry its SQL as one non-empty value, but has %d value(s)", len(c.Values))
			if len(c.Values) == 1 {
				reason = "its SQL is empty"
			}
			return nil, unsupportedCoded(FilterBlockedValueCount, c.Condition, reason)
		}
		return &launchdarkly.EventFilter{
			Type:      launchdarkly.EventFilterTypeEventProperty,
			Attribute: statsig.SQLFilterColumn(sql),
			Op:        ldOpIn,
			Values:    []any{true},
		}, nil

	// TODO: support Statsig's "is after exposure" filtering.
	//
	// These compare a column against each unit's own exposure time, so the value on
	// the other side of the comparison is different for every row. LaunchDarkly's
	// before/after operators compare against one fixed date, so there is nothing to
	// map onto today: this needs a new filter capability on the LaunchDarkly side
	// that can reference the exposure timestamp.
	//
	// Worth confirming how much this is actually used before building it. Statsig
	// documents it as "is after exposure":
	// https://docs.statsig.com/statsig-warehouse-native/configuration/metrics#is-after-exposure
	case "after_exposure", "before_exposure":
		return nil, unsupported(c.Condition,
			"it compares the column against each unit's exposure time, but LaunchDarkly can only compare against a fixed date")
	}

	column := strings.TrimSpace(c.Column)
	if column == "" {
		return nil, unsupportedCoded(FilterBlockedMissingColumn, c.Condition,
			"it names no column, so there is nothing to filter on")
	}

	leaf := &launchdarkly.EventFilter{
		Type:      launchdarkly.EventFilterTypeEventProperty,
		Attribute: column,
	}

	switch c.Condition {
	// Equality and set membership. LaunchDarkly's "in" is any-of, which is also
	// what Statsig's value picker means ("ANY OF"), so "=" with one value and
	// "in" with several both land on "in".
	case "in", "=":
		vals, err := stringValues(c)
		if err != nil {
			return nil, err
		}
		leaf.Op = ldOpIn
		leaf.Values = vals
	case "not_in":
		vals, err := stringValues(c)
		if err != nil {
			return nil, err
		}
		leaf.Op = ldOpIn
		leaf.Negate = true
		leaf.Values = vals

	// Substring operators.
	case "contains", "not_contains":
		vals, err := stringValues(c)
		if err != nil {
			return nil, err
		}
		leaf.Op = ldOpContains
		leaf.Negate = c.Condition == "not_contains"
		leaf.Values = vals
	case "starts_with":
		vals, err := stringValues(c)
		if err != nil {
			return nil, err
		}
		leaf.Op = ldOpStartsWith
		leaf.Values = vals
	case "ends_with":
		vals, err := stringValues(c)
		if err != nil {
			return nil, err
		}
		leaf.Op = ldOpEndsWith
		leaf.Values = vals

	// Numeric comparisons. These MUST carry a JSON number, not a string.
	// LaunchDarkly compares numerically and ignores a filter value that is not a
	// number, so a string here would quietly match nothing instead of failing.
	// LaunchDarkly also takes exactly one value here and does not allow negation.
	case ">":
		v, err := singleNumberValue(c)
		if err != nil {
			return nil, err
		}
		leaf.Op = ldOpGreaterThan
		leaf.Values = []any{v}
	case ">=":
		v, err := singleNumberValue(c)
		if err != nil {
			return nil, err
		}
		leaf.Op = ldOpGreaterThanOrEqual
		leaf.Values = []any{v}
	case "<":
		v, err := singleNumberValue(c)
		if err != nil {
			return nil, err
		}
		leaf.Op = ldOpLessThan
		leaf.Values = []any{v}
	case "<=":
		v, err := singleNumberValue(c)
		if err != nil {
			return nil, err
		}
		leaf.Op = ldOpLessThanOrEqual
		leaf.Values = []any{v}

	// Presence checks. LaunchDarkly's "exists" is the positive form, so non_null
	// maps to negate=false and is_null to negate=true. Statsig accepts and stores
	// stray values on these conditions, but LaunchDarkly rejects any filter that
	// carries values with "exists", so they are dropped unconditionally.
	case "non_null":
		leaf.Op = ldOpExists
		leaf.Values = []any{}
	case "is_null":
		leaf.Op = ldOpExists
		leaf.Negate = true
		leaf.Values = []any{}

	// Boolean checks. LaunchDarkly filter values may be booleans as well as strings
	// and numbers, so a true/false check is just "in" with one boolean value.
	// Whatever Statsig happened to store in values is ignored, since the condition
	// itself carries the whole meaning.
	//
	// This assumes the column really is a boolean. Warehouse-native filters compare
	// the column's text form, and a boolean column renders as "true"/"false", so the
	// match lines up. A column that stores 1/0 or "TRUE" instead renders as "1" or
	// "TRUE" and will not match, in which case the filter selects no rows.
	case "is_true", "is_false":
		leaf.Op = ldOpIn
		leaf.Values = []any{c.Condition == "is_true"}

	// The never-mappable conditions are rejected above, before the column check.
	default:
		return nil, unsupported(c.Condition, "LaunchDarkly has no matching filter operator")
	}

	return leaf, nil
}

// stringValues returns the criterion's values as LaunchDarkly JSON strings.
//
// Empty-string values are rejected. Statsig stores "" as a distinct value, but
// whether its engine treats it as an empty string or as null is a
// compute-semantics question its API cannot answer, and the two map to different
// LaunchDarkly shapes (a plain "in" versus an OR of "in" and a negated "exists").
// Guessing would silently change which rows match.
func stringValues(c statsig.Criterion) ([]any, error) {
	if len(c.Values) == 0 {
		return nil, unsupportedCoded(FilterBlockedValueCount, c.Condition, "it needs at least one filter value, but none were set")
	}
	out := make([]any, 0, len(c.Values))
	for _, v := range c.Values {
		if v == "" {
			return nil, unsupportedCoded(FilterBlockedValue, c.Condition,
				"one of its values is empty, and it is unclear whether that means an empty value or no value at all; LaunchDarkly treats those differently")
		}
		if len(v) > ldFilterMaxStringLen {
			return nil, unsupportedCoded(FilterBlockedValue, c.Condition, fmt.Sprintf(
				"one of its values is %d characters, longer than LaunchDarkly's limit of %d", len(v), ldFilterMaxStringLen))
		}
		out = append(out, v)
	}
	return out, nil
}

// singleNumberValue parses a comparison criterion's single value as a number.
//
// LaunchDarkly requires exactly one value for a comparison operator. Statsig may
// carry several; silently reducing them to a min or max would change the metric's
// meaning, so a multi-value comparison is rejected instead.
func singleNumberValue(c statsig.Criterion) (float64, error) {
	if len(c.Values) != 1 {
		return 0, unsupportedCoded(FilterBlockedValueCount, c.Condition, fmt.Sprintf(
			"a numeric comparison in LaunchDarkly takes exactly one value, but this criterion has %d", len(c.Values)))
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(c.Values[0]), 64)
	if err != nil {
		return 0, unsupportedCoded(FilterBlockedValue, c.Condition, fmt.Sprintf(
			"its value %q is not a number, so the comparison would match no rows at all", c.Values[0]))
	}
	return n, nil
}

// criteriaPhrase renders the count with a matching noun, e.g. "1 numerator filter
// criterion" or "3 numerator filter criteria". Most terms carry exactly one, and
// "1 criteria" reads badly in a report someone has to act on.
func criteriaPhrase(n int, label string) string {
	noun := "criteria"
	if n == 1 {
		noun = "criterion"
	}
	return fmt.Sprintf("%d %s filter %s", n, label, noun)
}

// wasWere agrees with the criterion count.
func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

// criteriaDetail renders criteria for a warning message, so a dropped filter can
// be reconstructed by hand in LaunchDarkly.
func criteriaDetail(criteria []statsig.Criterion) string {
	details := make([]string, 0, len(criteria))
	for _, c := range criteria {
		// sql_filter and malformed criteria carry no column; say so rather than
		// leaving a blank that reads as a formatting bug.
		column := strings.TrimSpace(c.Column)
		if column == "" {
			column = "(no column)"
		}
		details = append(details, fmt.Sprintf("%s %s %s %v", column, c.Type, c.Condition, c.Values))
	}
	return strings.Join(details, "; ")
}

// convertTermCriteria maps one term's Statsig criteria onto a LaunchDarkly metric
// filter, or records a lossy reason and returns nil when it cannot.
//
// warehouseNative and a data source (dsKey) are both required for a filter to be emitted:
//
//   - Cloud (SDK-event) criteria are not converted yet. See the TODO below for what
//     that would take. LaunchDarkly does support filters on hosted metrics, so this
//     is deferred work rather than something that cannot be done.
//   - Without a bound data source LaunchDarkly treats the metric as SDK-hosted,
//     where the same eventProperty clause means "extract this key from the event's
//     JSON payload" rather than "read this warehouse column". Emitting the filter
//     anyway would produce a metric that saves and then measures something else.
//     LaunchDarkly also refuses the "exists" operator on a hosted metric outright.
//
// TODO: convert filter criteria on cloud (SDK-event) metrics into hosted
// LaunchDarkly metric filters.
//
// LaunchDarkly hosted metrics accept filters, including filters on context
// attributes, so the gap here is a mapping we have not written rather than a
// missing capability. Four things need deciding first:
//
//  1. A LaunchDarkly context-attribute filter must say which context kind it
//     applies to. Statsig criteria carry no equivalent, so the kind has to come
//     from somewhere else (the metric's analysis unit is the obvious guess). Guess
//     wrong and the filter quietly matches nothing.
//  2. Value types matter on the hosted path in a way they do not for warehouse
//     metrics. A warehouse filter compares the column's text form, so passing
//     Statsig's values through as strings is correct. A hosted filter keeps the
//     JSON type, so "5" and 5 are different filters. Statsig only ever gives us
//     strings, so hosted needs a typing rule that warehouse-native does not.
//  3. The "exists" operator is not available on hosted metrics, so is_null and
//     non_null have no mapping there at all.
//  4. Hosted filters sit behind a different feature gate than warehouse-native
//     ones, so the save-time behaviour differs.
//
// dsKey is the term's data source, columns its columns when known, and filterCols the
// SQL filter columns its query adds.
func convertTermCriteria(
	result *Result,
	termLabel string,
	criteria []statsig.Criterion,
	warehouseNative bool,
	dsKey string,
	columns []string,
	filterCols []warehouse.CustomField,
) *launchdarkly.EventFilter {
	if len(criteria) == 0 {
		return nil
	}
	detail := criteriaDetail(criteria)

	blocked := func(code, condition string) {
		result.FilterOutcomes = append(result.FilterOutcomes, FilterOutcome{
			Term: termLabel, Criteria: len(criteria), Applied: false,
			BlockedBy: code, BlockedCondition: condition,
		})
	}

	if !warehouseNative {
		result.addLossy(WarnFilterCloudUnsupported, "DATA LOSS: %s %s not applied. Filter conversion currently works on warehouse-native metrics only, so this metric will match every event instead of just the filtered subset. Dropped filters: [%s]. Add them by hand in LaunchDarkly if you need them.",
			criteriaPhrase(len(criteria), termLabel), wasWere(len(criteria)), detail)
		blocked(FilterBlockedCloudMetric, "")
		return nil
	}

	if dsKey == "" {
		result.addLossy(WarnFilterNoDataSource, "DATA LOSS: %s %s not applied. LaunchDarkly metric filters need a warehouse data source and this metric is not bound to one. Pass --ld-data-source or --source-mapping and run again to convert the filters. Dropped filters: [%s].",
			criteriaPhrase(len(criteria), termLabel), wasWere(len(criteria)), detail)
		blocked(FilterBlockedNoDataSource, "")
		return nil
	}

	translated := sqlFilterTranslations(criteria)
	filter, err := criteriaToFilter(criteria, translated, filterCols)
	if err != nil {
		result.addLossy(WarnFilterConditionBlocked, "DATA LOSS: %s %s not applied. %v. This metric would match every row instead of just the filtered subset. Dropped filters: [%s]. Add them by hand in LaunchDarkly if you need them.",
			criteriaPhrase(len(criteria), termLabel), wasWere(len(criteria)), err, detail)
		var ue *unsupportedCriteriaError
		if errors.As(err, &ue) {
			blocked(ue.Code, ue.Condition)
		} else {
			blocked(FilterBlockedCondition, "")
		}
		return nil
	}

	if missing, unaddable := missingSQLFilterColumns(criteria, translated, filterCols, columns); len(missing) > 0 {
		if unaddable {
			result.addLossy(WarnFilterSQLFilterColumn, "DATA LOSS: %s %s not applied. Data source %q has no %s for the result of its Statsig SQL filter, and warehouse cannot add one: the filter uses a Statsig macro, has more than one statement or unbalanced parentheses, or could not be read. Fix it in Statsig, rerun warehouse with --update-custom-fields, then run metrics convert again. Dropped filters: [%s].",
				criteriaPhrase(len(criteria), termLabel), wasWere(len(criteria)), dsKey, columnPhrase(missing), detail)
		} else {
			result.addLossy(WarnFilterSQLFilterColumn, "DATA LOSS: %s %s not applied. Data source %q has no %s for the result of its Statsig SQL filter. If statsig-to-ld warehouse created the data source, rerun warehouse with --update-custom-fields to add it; otherwise add (<filter SQL>) AS <column> to the data source's query in LaunchDarkly. Then run metrics convert again. Dropped filters: [%s].",
				criteriaPhrase(len(criteria), termLabel), wasWere(len(criteria)), dsKey, columnPhrase(missing), detail)
		}
		blocked(FilterBlockedSQLFilterColumn, "sql_filter")
		return nil
	}
	if len(columns) == 0 && needsSQLFilterColumn(criteria, translated) {
		result.addNote(WarnColumnUnverified)
	}

	result.addWarning(WarnFilterApplied,
		"converted %s into a LaunchDarkly metric filter (metric filters only work on Snowflake data sources today)",
		criteriaPhrase(len(criteria), termLabel))
	result.FilterOutcomes = append(result.FilterOutcomes, FilterOutcome{
		Term: termLabel, Criteria: len(criteria), Applied: true,
	})
	return filter
}

// ldDescriptionMaxLen is LaunchDarkly's limit on a metric's description.
const ldDescriptionMaxLen = 768

// sqlFilterLines gives a description line per Statsig SQL filter in criteria. label
// names the term.
func sqlFilterLines(label string, criteria []statsig.Criterion) string {
	var b strings.Builder
	for _, c := range criteria {
		if sql, ok := c.SQLFilter(); ok {
			b.WriteString("\nStatsig " + label + "SQL filter: " + sql)
		}
	}
	return b.String()
}

// withDescriptionLines adds lines to desc when all of them fit LaunchDarkly's limit.
func withDescriptionLines(desc, lines string) string {
	if len(desc)+len(lines) > ldDescriptionMaxLen {
		return desc
	}
	return desc + lines
}

// sqlFilterColumn names the column that holds sql's result: the one the data
// source's query gives it, else the one warehouse would.
func sqlFilterColumn(sql string, filterCols []warehouse.CustomField) string {
	for _, f := range filterCols {
		if warehouse.SameSQLFilter(f.Formula, sql) {
			return f.Key
		}
	}
	return statsig.SQLFilterColumn(sql)
}

// needsSQLFilterColumn reports whether a SQL filter of the term checks its column.
func needsSQLFilterColumn(criteria []statsig.Criterion, translated map[int][]launchdarkly.EventFilter) bool {
	for i, c := range criteria {
		if _, ok := c.SQLFilter(); ok && translated[i] == nil {
			return true
		}
	}
	return false
}

// missingSQLFilterColumns names the SQL filter columns that columns lack, ignoring
// case, for the filters not in translated, and reports whether warehouse cannot add
// one. With columns unknown it names only those. Metric filters work only on
// Snowflake, so the SQL is read as Snowflake's.
func missingSQLFilterColumns(criteria []statsig.Criterion, translated map[int][]launchdarkly.EventFilter, filterCols []warehouse.CustomField, columns []string) (missing []string, unaddable bool) {
	for i, c := range criteria {
		sql, ok := c.SQLFilter()
		if _, done := translated[i]; !ok || done {
			continue
		}
		f := warehouse.CustomField{Key: sqlFilterColumn(sql, filterCols), Formula: sql}
		if slices.ContainsFunc(columns, func(name string) bool { return strings.EqualFold(name, f.Key) }) {
			continue
		}
		bad := warehouse.ValidateSQLFilter(f, "snowflake") != nil
		if bad || len(columns) > 0 {
			missing = append(missing, f.Key)
			unaddable = unaddable || bad
		}
	}
	return missing, unaddable
}

func columnPhrase(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	if len(names) == 1 {
		return "column " + quoted[0]
	}
	return "columns " + strings.Join(quoted, ", ")
}
