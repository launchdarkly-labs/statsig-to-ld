package converter

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
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
	return &unsupportedCriteriaError{Condition: condition, Reason: reason}
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
// Returns (nil, nil) when there are no criteria.
func criteriaToFilter(criteria []statsig.Criterion) (*launchdarkly.EventFilter, error) {
	if len(criteria) == 0 {
		return nil, nil
	}
	if len(criteria) > ldFilterMaxLeaves {
		return nil, unsupported("", fmt.Sprintf(
			"this metric has %d filter criteria, but LaunchDarkly allows at most %d",
			len(criteria), ldFilterMaxLeaves))
	}

	leaves := make([]launchdarkly.EventFilter, 0, len(criteria))
	for _, c := range criteria {
		leaf, err := criterionToLeaf(c)
		if err != nil {
			return nil, err
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
		return nil, unsupported(c.Condition,
			"it sets nullVacuousOverride to change how empty values are treated, and LaunchDarkly has no equivalent setting, so the filter would match a different set of rows")
	}

	// Only column filters map. "user" / "user_custom" are context-attribute
	// filters, which LaunchDarkly rejects on warehouse-native metrics, and "value"
	// filters on the metric value rather than a column.
	if c.Type != "" && c.Type != statsigCriterionTypeMetadata {
		return nil, unsupported(c.Condition, fmt.Sprintf(
			"it filters on %q, but a warehouse-native metric in LaunchDarkly can only filter on a column from its data source", c.Type))
	}

	// Conditions that can never map are rejected before the column check, so the
	// reason names the condition. sql_filter in particular never carries a column,
	// so the column check would otherwise mask the real explanation.
	switch c.Condition {
	case "sql_filter":
		return nil, unsupported(c.Condition,
			"it is a raw SQL snippet, which cannot be turned into a LaunchDarkly filter")
	case "after_exposure", "before_exposure":
		return nil, unsupported(c.Condition,
			"it compares the column against each unit's exposure time, but LaunchDarkly can only compare against a fixed date")
	case "is_true", "is_false":
		return nil, unsupported(c.Condition,
			"LaunchDarkly does not support a true/false column check yet")
	}

	column := strings.TrimSpace(c.Column)
	if column == "" {
		return nil, unsupported(c.Condition,
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
		return nil, unsupported(c.Condition, "it needs at least one filter value, but none were set")
	}
	out := make([]any, 0, len(c.Values))
	for _, v := range c.Values {
		if v == "" {
			return nil, unsupported(c.Condition,
				"one of its values is empty, and it is unclear whether that means an empty value or no value at all; LaunchDarkly treats those differently")
		}
		if len(v) > ldFilterMaxStringLen {
			return nil, unsupported(c.Condition, fmt.Sprintf(
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
		return 0, unsupported(c.Condition, fmt.Sprintf(
			"a numeric comparison in LaunchDarkly takes exactly one value, but this criterion has %d", len(c.Values)))
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(c.Values[0]), 64)
	if err != nil {
		return 0, unsupported(c.Condition, fmt.Sprintf(
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
// warehouseNative and hasDataSource are both required for a filter to be emitted:
//
//   - Cloud (SDK-event) criteria are out of scope. Their criterion types are
//     context attributes, which LaunchDarkly rejects on warehouse-native metrics,
//     and the hosted filter mapping is a separate piece of work.
//   - Without a bound data source LaunchDarkly treats the metric as SDK-hosted,
//     where the same eventProperty clause means "extract this key from the event's
//     JSON payload" rather than "read this warehouse column". Emitting the filter
//     anyway would produce a metric that saves and then measures something else.
//     LaunchDarkly also refuses the "exists" operator on a hosted metric outright.
func convertTermCriteria(
	result *Result,
	termLabel string,
	criteria []statsig.Criterion,
	warehouseNative bool,
	hasDataSource bool,
) *launchdarkly.EventFilter {
	if len(criteria) == 0 {
		return nil
	}
	detail := criteriaDetail(criteria)

	if !warehouseNative {
		result.addLossy("DATA LOSS: %s %s not applied. Filter conversion currently works on warehouse-native metrics only, so this metric will match every event instead of just the filtered subset. Dropped filters: [%s]. Add them by hand in LaunchDarkly if you need them.",
			criteriaPhrase(len(criteria), termLabel), wasWere(len(criteria)), detail)
		return nil
	}

	if !hasDataSource {
		result.addLossy("DATA LOSS: %s %s not applied. LaunchDarkly metric filters need a warehouse data source and this metric is not bound to one. Pass --ld-data-source or --source-mapping and run again to convert the filters. Dropped filters: [%s].",
			criteriaPhrase(len(criteria), termLabel), wasWere(len(criteria)), detail)
		return nil
	}

	filter, err := criteriaToFilter(criteria)
	if err != nil {
		result.addLossy("DATA LOSS: %s %s not applied. %v. This metric would match every row instead of just the filtered subset. Dropped filters: [%s]. Add them by hand in LaunchDarkly if you need them.",
			criteriaPhrase(len(criteria), termLabel), wasWere(len(criteria)), err, detail)
		return nil
	}

	result.Warnings = append(result.Warnings,
		fmt.Sprintf("converted %s into a LaunchDarkly metric filter (metric filters only work on Snowflake data sources today)",
			criteriaPhrase(len(criteria), termLabel)))
	return filter
}
