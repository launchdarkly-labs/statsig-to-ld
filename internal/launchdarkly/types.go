package launchdarkly

// ============================================================================
// Metric types (consumed by `metrics convert`)
// ============================================================================

// MetricPost is the request body for creating an LD metric via the REST API.
type MetricPost struct {
	Key                  string           `json:"key"`
	Kind                 string           `json:"kind"`
	Name                 string           `json:"name,omitempty"`
	Description          string           `json:"description,omitempty"`
	EventKey             string           `json:"eventKey,omitempty"`
	IsNumeric            *bool            `json:"isNumeric,omitempty"`
	SuccessCriteria      string           `json:"successCriteria,omitempty"`
	UnitAggregationType  string           `json:"unitAggregationType,omitempty"`
	UnitAggregationField string           `json:"unitAggregationField,omitempty"`
	AnalysisType         string           `json:"analysisType,omitempty"`
	Unit                 string           `json:"unit,omitempty"`
	Tags                 []string         `json:"tags,omitempty"`
	EventDefault         *EventDefault    `json:"eventDefault,omitempty"`
	DataSource           *DataSource      `json:"dataSource,omitempty"`
	Denominator          *DenominatorPost `json:"denominator,omitempty"`

	// AnalysisUnits supersedes the deprecated randomizationUnits field, which held
	// the same list.
	AnalysisUnits []string `json:"analysisUnits,omitempty"`

	// Measurement window offsets, in milliseconds. LD only accepts these on
	// metrics backed by a snowflake-experimentation data source.
	WindowStartOffset *int64 `json:"windowStartOffset,omitempty"`
	WindowEndOffset   *int64 `json:"windowEndOffset,omitempty"`

	// Winsorization percentiles on a 0–100 scale. Not valid on occurrence
	// metrics (non-numeric average).
	WinsorLowerPercentile *float32 `json:"winsorLowerPercentile,omitempty"`
	WinsorUpperPercentile *float32 `json:"winsorUpperPercentile,omitempty"`

	// Filters narrows which events the numerator counts. On a warehouse-native
	// metric these filter warehouse columns.
	Filters *EventFilter `json:"filters,omitempty"`
}

// EventFilter is a node in a LaunchDarkly metric filter tree: either a group
// combining child nodes, or a leaf clause narrowing on one attribute.
//
// Constraints LaunchDarkly enforces on save, which the converter must respect:
//   - Warehouse-native metrics accept only eventProperty leaves. A contextAttribute
//     anywhere in the tree, including nested in a group, is rejected.
//   - Group nodes must not set Attribute, ContextKind, or Negate.
//   - Maximum nesting depth 3 and maximum 10 leaf clauses.
//   - "exists" takes no values; any other operator needs at least one. The
//     comparison operators additionally require exactly one and forbid Negate.
type EventFilter struct {
	// Type is one of group, eventProperty, or contextAttribute.
	Type string `json:"type"`
	// Attribute is the event property name, or on a warehouse-native metric the
	// warehouse column name. Unset on group nodes.
	Attribute string `json:"attribute,omitempty"`
	// Op is the group combinator (and / or) or the leaf operator (in, contains,
	// startsWith, endsWith, lessThan, lessThanOrEqual, greaterThan,
	// greaterThanOrEqual, before, after, exists).
	Op string `json:"op"`
	// Values holds child EventFilter nodes on a group, or scalar strings, numbers,
	// and booleans on a leaf. Always serialized, since LaunchDarkly treats a
	// missing values field as invalid. Empty for the "exists" operator.
	Values []any `json:"values"`
	// ContextKind is required on contextAttribute leaves and unset otherwise.
	ContextKind string `json:"contextKind,omitempty"`
	// Negate inverts the operator: "in" becomes "not in", and on "exists" it turns
	// a has-a-value check into a has-no-value check.
	Negate bool `json:"negate"`
}

// Filter node types and group combinators.
const (
	EventFilterTypeGroup         = "group"
	EventFilterTypeEventProperty = "eventProperty"
	EventFilterTypeContextAttr   = "contextAttribute"

	EventFilterGroupOpAnd = "and"
	EventFilterGroupOpOr  = "or"
)

// DenominatorPost configures the denominator term of a ratio metric.
// The numerator's equivalents are top-level fields on MetricPost.
type DenominatorPost struct {
	EventName            string `json:"eventName"`
	IsNumeric            bool   `json:"isNumeric"`
	UnitAggregationType  string `json:"unitAggregationType"`
	UnitAggregationField string `json:"unitAggregationField,omitempty"`
	// DataSource is the denominator's warehouse data source, independent of the
	// numerator's (top-level) DataSource. Omit for SDK-hosted (cloud) ratios.
	DataSource *DataSource `json:"dataSource,omitempty"`
	// Filters narrows which events the denominator counts, independently of the
	// numerator's filter.
	Filters *EventFilter `json:"filters,omitempty"`
}

// EventDefault configures the default event value for missing units.
type EventDefault struct {
	Disabled bool    `json:"disabled"`
	Value    float64 `json:"value"`
}

// DataSource links an LD metric to a Warehouse Native data source.
type DataSource struct {
	Key string `json:"key"`
}

// MetricResponse is the LD API response after creating a metric.
type MetricResponse struct {
	ID   string `json:"_id"`
	Key  string `json:"key"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// ============================================================================
// Flag types (consumed by `flags import` and `targeting import`, PRs 5 and 6)
// ============================================================================

// Flag is the LD flag shell payload. Mirrors the goaltender importer's shape.
// Per-env targeting (rules, targets, fallthrough) is applied separately via
// PatchFlag — not via this struct.
type Flag struct {
	Defaults     Defaults    `json:"defaults,omitempty"`
	Description  string      `json:"description"`
	Key          string      `json:"key"`
	MaintainerID string      `json:"maintainerId,omitempty"`
	Name         string      `json:"name"`
	Tags         []string    `json:"tags"`
	Temporary    bool        `json:"temporary"`
	Variations   []Variation `json:"variations"`
}

// Defaults is the on/off variation index pair for a flag.
type Defaults struct {
	OnVariation  int `json:"onVariation"`
	OffVariation int `json:"offVariation"`
}

// Variation is one named return value for a flag.
type Variation struct {
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`
	Value       any    `json:"value"`
}

// FailedFlag captures a flag that could not be constructed or created.
type FailedFlag struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// ============================================================================
// Environment types (consumed by `targeting import`, PR 6)
// ============================================================================

// Environment is the subset of /api/v2/projects/{proj}/environments fields the
// importer cares about. The LD API returns many more fields (sdkKey, mobileKey,
// defaultTtl, etc.) that are ignored.
type Environment struct {
	Key   string   `json:"key"`
	Name  string   `json:"name"`
	Color string   `json:"color"`
	Tags  []string `json:"tags,omitempty"`
}

// ============================================================================
// Private response shapes
// ============================================================================

type listFlagsResponse struct {
	Items      []Flag `json:"items"`
	TotalCount int    `json:"totalCount"`
}

type listEnvironmentsResponse struct {
	Items []Environment `json:"items"`
	Links struct {
		Next struct {
			Href string `json:"href"`
		} `json:"next"`
	} `json:"_links"`
}
