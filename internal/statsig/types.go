package statsig

import "encoding/json"

// ============================================================================
// Metric types (consumed by `metrics convert`)
// ============================================================================

// Metric represents a Statsig metric definition as returned by the Console API.
type Metric struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Type           string        `json:"type"`
	Description    string        `json:"description"`
	Directionality string        `json:"directionality"`
	UnitTypes      []string      `json:"unitTypes"`
	MetricEvents   []MetricEvent `json:"metricEvents"`
	Tags           []string      `json:"tags"`

	RollupTimeWindow  string   `json:"rollupTimeWindow"`
	CustomRollUpStart *float64 `json:"customRollUpStart"`
	CustomRollUpEnd   *float64 `json:"customRollUpEnd"`

	WarehouseNative *WarehouseNative `json:"warehouseNative"`

	MetricComponentMetrics []ComponentMetric `json:"metricComponentMetrics"`
	FunnelEventList        []FunnelEvent     `json:"funnelEventList"`
	FunnelCountDistinct    string            `json:"funnelCountDistinct"`

	MetricSourceName string `json:"metricSourceName"`

	// Lineage lists the raw events/metrics a metric derives from. Built-in
	// event_count metrics carry their counted event here and have no
	// metricEvents entry.
	Lineage Lineage `json:"lineage"`
}

// Lineage records the events and metrics a Statsig metric is derived from.
type Lineage struct {
	Events  []string `json:"events"`
	Metrics []string `json:"metrics"`
}

// MetricEvent represents an event definition within a Statsig metric.
type MetricEvent struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	MetadataKey string      `json:"metadataKey"`
	Criteria    []Criterion `json:"criteria"`
}

// Criterion represents a filter condition on a metric event. Field contract
// confirmed against Statsig's own marshaling model, CriteriaAPIModel in
// statsig-io/terraform-provider-statsig internal/resource_metric/metric_model.go.
//
// Values is always a string array, even for numeric and boolean comparisons, so
// values must be coerced per condition before being handed to LaunchDarkly.
type Criterion struct {
	Type      string   `json:"type"`
	Column    string   `json:"column"`
	Condition string   `json:"condition"`
	Values    []string `json:"values"`

	// NullVacuousOverride changes how Statsig treats nulls when evaluating the
	// criterion. LaunchDarkly has no equivalent, so its presence makes a criterion
	// unconvertible rather than being silently ignored.
	NullVacuousOverride *bool `json:"nullVacuousOverride,omitempty"`
}

// WarehouseNative holds Statsig warehouse-native metric config. For these
// metrics the top-level Metric.Type is "user_warehouse"/"hybrid_warehouse" and
// the real shape lives in Aggregation. Field names/shapes are confirmed against
// Statsig's own public repos: real metric dumps in statsig-io/semantic_layer and
// the marshaling contract in statsig-io/terraform-provider-statsig
// (WarehouseNativeAPIModel). Two source forms occur in the wild — a flat
// single-source form (MetricSourceName/ValueColumn/Criteria directly here) and a
// MetricSources array — so the converter reads both (see NumeratorValueColumn /
// NumeratorCriteria).
type WarehouseNative struct {
	// Aggregation is the real shape: count | sum | mean | count_distinct |
	// percentile | daily_participation | ratio | funnel.
	Aggregation string `json:"aggregation"`

	MetricSources []MetricSource `json:"metricSources"`

	// Flat single-source form: source name, value column, and filter criteria
	// live directly on warehouseNative (Statsig's Terraform model and the
	// semantic_layer dumps use this form).
	MetricSourceName string      `json:"metricSourceName"`
	ValueColumn      string      `json:"valueColumn"`
	Criteria         []Criterion `json:"criteria"`

	// Windowing lives inside warehouseNative for WHN metrics (top-level on the
	// metric for cloud metrics).
	RollupTimeWindow  string   `json:"rollupTimeWindow"`
	CustomRollUpStart *float64 `json:"customRollUpStart"`
	CustomRollUpEnd   *float64 `json:"customRollUpEnd"`

	// Ratio terms (top-level Aggregation is "ratio"): the numerator uses the
	// fields above; the denominator has its own aggregation, column, and filters.
	NumeratorAggregation        string      `json:"numeratorAggregation"`
	DenominatorMetricSourceName string      `json:"denominatorMetricSourceName"`
	DenominatorAggregation      string      `json:"denominatorAggregation"`
	DenominatorValueColumn      string      `json:"denominatorValueColumn"`
	DenominatorCriteria         []Criterion `json:"denominatorCriteria"`

	WinsorizationHigh *float64 `json:"winsorizationHigh"`
	WinsorizationLow  *float64 `json:"winsorizationLow"`
	Cap               *float64 `json:"cap"`
	Percentile        *float64 `json:"percentile"`
	UseLogTransform   *bool    `json:"useLogTransform"`
	ValueThreshold    *float64 `json:"valueThreshold"`

	// Advanced analysis features with no direct LaunchDarkly equivalent; the
	// converter flags them but they don't change the core metric definition.
	CupedAttributionWindow *float64 `json:"cupedAttributionWindow"`
	MetricDimensionColumns []string `json:"metricDimensionColumns"`
	WaitForCohortWindow    *bool    `json:"waitForCohortWindow"`
	MetricBakeDays         *float64 `json:"metricBakeDays"`
}

// MetricSource is a numerator-side warehouse source. The criteria sub-shape is
// unverified against a live Console API response.
type MetricSource struct {
	MetricSourceName string      `json:"metricSourceName"`
	Criteria         []Criterion `json:"criteria"`
	ValueColumn      string      `json:"valueColumn"`
}

// MetricSourceConfig is a warehouse-native metric source as returned by
// /console/v1/metrics/metric_source/list. Only the fields the converter needs
// are modeled; the analysis unit(s) a metric can use live in IDTypeMapping.
type MetricSourceConfig struct {
	Name          string          `json:"name"`
	IDTypeMapping []IDTypeMapping `json:"idTypeMapping"`
}

// IDTypeMapping maps a Statsig unit ID (e.g. "userID", "companyID") to the
// source column that holds it. StatsigUnitID is the analysis unit.
type IDTypeMapping struct {
	StatsigUnitID string `json:"statsigUnitID"`
	Column        string `json:"column"`
}

// IsWarehouseNative reports whether the metric's real aggregation lives in
// WarehouseNative.Aggregation rather than the top-level Type.
func (m *Metric) IsWarehouseNative() bool {
	return m.Type == "user_warehouse" || m.Type == "hybrid_warehouse" || m.HasWarehouseAggregation()
}

// HasWarehouseAggregation reports whether an explicit warehouse-native
// aggregation is set (the value EffectiveType returns).
func (m *Metric) HasWarehouseAggregation() bool {
	return m.WarehouseNative != nil && m.WarehouseNative.Aggregation != ""
}

// EffectiveType returns the aggregation to dispatch on: the warehouse-native
// aggregation when present, else the top-level type. (Convert rejects a
// warehouse-native metric that reaches it without an aggregation, so the
// fallback is only a defensive default.)
func (m *Metric) EffectiveType() string {
	if m.HasWarehouseAggregation() {
		return m.WarehouseNative.Aggregation
	}
	return m.Type
}

// EffectiveRollupTimeWindow returns the metric's rollup mode: the
// warehouse-native rollupTimeWindow when present, else the top-level value.
// For the unit-count ("daily_participation") family, Statsig's values are
// "daily" (daily participation rate), "max" (one-time event), and "custom"
// (custom attribution window).
func (m *Metric) EffectiveRollupTimeWindow() string {
	if m.WarehouseNative != nil && m.WarehouseNative.RollupTimeWindow != "" {
		return m.WarehouseNative.RollupTimeWindow
	}
	return m.RollupTimeWindow
}

// NumeratorSourceName returns the numerator's warehouse source name, preferring
// MetricSources over the deprecated single-source fields.
func (m *Metric) NumeratorSourceName() string {
	if m.WarehouseNative != nil {
		if len(m.WarehouseNative.MetricSources) > 0 && m.WarehouseNative.MetricSources[0].MetricSourceName != "" {
			return m.WarehouseNative.MetricSources[0].MetricSourceName
		}
		if m.WarehouseNative.MetricSourceName != "" {
			return m.WarehouseNative.MetricSourceName
		}
	}
	return m.MetricSourceName
}

// NumeratorValueColumn returns the numerator's warehouse value column, reading
// either the MetricSources array or the flat warehouseNative.valueColumn form.
func (m *Metric) NumeratorValueColumn() string {
	if m.WarehouseNative == nil {
		return ""
	}
	wn := m.WarehouseNative
	if len(wn.MetricSources) > 0 && wn.MetricSources[0].ValueColumn != "" {
		return wn.MetricSources[0].ValueColumn
	}
	return wn.ValueColumn
}

// NumeratorCriteria returns the numerator's filter criteria, reading either the
// MetricSources array or the flat warehouseNative.criteria form.
func (m *Metric) NumeratorCriteria() []Criterion {
	if m.WarehouseNative == nil {
		return nil
	}
	wn := m.WarehouseNative
	if len(wn.MetricSources) > 0 && len(wn.MetricSources[0].Criteria) > 0 {
		return wn.MetricSources[0].Criteria
	}
	return wn.Criteria
}

// ComponentMetric is a reference to another metric used in composite metrics.
type ComponentMetric struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// FunnelEvent is a step in a funnel metric.
type FunnelEvent struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ============================================================================
// Gate / Dynamic Config / Environment / Override types
// (consumed by `flags import` and `targeting import`, added in PRs 4 and 5)
// ============================================================================

// Gate represents a Feature Gate from /console/v1/gates. The list response
// already includes rules + nested conditions; no per-gate GET is needed for
// targeting import.
type Gate struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	IsEnabled   bool       `json:"isEnabled"`
	Tags        []string   `json:"tags"`
	Type        string     `json:"type"`
	IDType      string     `json:"idType"`
	Rules       []GateRule `json:"rules,omitempty"`
}

// GateRule is one gate's targeting rule. Environments is nullable: nil means
// "applies to all environments" per Statsig docs. PassPercentage is nullable
// too (rare; treated as 0 by transformation).
type GateRule struct {
	ID             string      `json:"id,omitempty"`
	Name           string      `json:"name"`
	PassPercentage *float64    `json:"passPercentage,omitempty"`
	Conditions     []Condition `json:"conditions"`
	// Environments is nil when the field is absent OR null in the API response.
	// When present and an array (even empty), the rule scopes to those Statsig
	// envs. Distinguishing null vs [] requires the pointer.
	Environments *[]string `json:"environments,omitempty"`
}

// Condition is one clause within a targeting rule. CustomID is set for
// unit_id conditions (the Statsig non-userID unit). Field is set for
// custom_field conditions.
type Condition struct {
	Type        string `json:"type"`
	Operator    string `json:"operator,omitempty"`
	TargetValue any    `json:"targetValue,omitempty"`
	Field       string `json:"field,omitempty"`
	CustomID    string `json:"customID,omitempty"`
}

// DynamicConfig represents a Dynamic Config from /console/v1/dynamic_configs.
type DynamicConfig struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	IsEnabled    bool            `json:"isEnabled"`
	Tags         []string        `json:"tags"`
	DefaultValue json.RawMessage `json:"defaultValue"`
	Rules        []DCRule        `json:"rules"`
}

// DCRule is one targeting rule on a Dynamic Config.
type DCRule struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	PassPercentage float64         `json:"passPercentage"`
	ReturnValue    json.RawMessage `json:"returnValue"`
	// Variants is present on newer Statsig Dynamic Configs and defines the
	// canonical set of return values for the config. When present, import
	// treats variants as the source of LD variations. Older configs without
	// variants fall back to the rule-level returnValue / dc-level
	// defaultValue path.
	Variants []DCVariant `json:"variants,omitempty"`

	// Conditions and Environments mirror the gate-rule fields and are used
	// for targeting-rule import. Older shell-only DC variation building
	// doesn't read these.
	Conditions   []Condition `json:"conditions,omitempty"`
	Environments *[]string   `json:"environments,omitempty"`
}

// DCVariant is one named return value within a Dynamic Config rule.
// PassPercentage is set on newer multi-variant DCs to weight the rollout.
type DCVariant struct {
	Name           string          `json:"name"`
	ReturnValue    json.RawMessage `json:"returnValue"`
	PassPercentage float64         `json:"passPercentage,omitempty"`
}

// Environment is one environment in a Statsig project. Returned from
// /console/v1/environments. Used by env reconciliation to enumerate the
// universe of Statsig envs to map / auto-create on the LD side.
type Environment struct {
	Name           string `json:"name"`
	IsProduction   bool   `json:"isProduction"`
	RequiresReview bool   `json:"requiresReview"`
}

// Override is one env-scoped override entry. Environment is nil when the
// override applies to all envs. PassingIDs map to variation 0 (gates = true,
// DCs = first/passing variant); FailingIDs map to variation 1 (gates = false,
// DCs = default). UnitID is "userID" for user-keyed overrides; other unit
// IDs are accepted but treated as the "user" context kind in v1.
type Override struct {
	Environment *string  `json:"environment"`
	UnitID      string   `json:"unitID"`
	PassingIDs  []string `json:"passingIDs"`
	FailingIDs  []string `json:"failingIDs"`
}

// ============================================================================
// Private list-response shapes
// ============================================================================

// metricListResponse is the page-number-paginated /metrics/list response. Its
// pagination block mirrors the gates / dynamic-config endpoints.
type metricListResponse struct {
	Message    string     `json:"message"`
	Data       []Metric   `json:"data"`
	Pagination pagination `json:"pagination"`
}

// pagination is the page-number pagination block returned by /gates and
// /dynamic_configs.
type pagination struct {
	ItemsPerPage int    `json:"itemsPerPage"`
	PageNumber   int    `json:"pageNumber"`
	TotalItems   int    `json:"totalItems"`
	NextPage     string `json:"nextPage"`
	PreviousPage string `json:"previousPage"`
}

type gatesListResponse struct {
	Message    string     `json:"message"`
	Data       []Gate     `json:"data"`
	Pagination pagination `json:"pagination"`
}

type dynamicConfigsListResponse struct {
	Message    string          `json:"message"`
	Data       []DynamicConfig `json:"data"`
	Pagination pagination      `json:"pagination"`
}

type environmentsResponse struct {
	Data struct {
		Environments []Environment `json:"environments"`
	} `json:"data"`
}

type overridesResponse struct {
	Data struct {
		EnvironmentOverrides []Override `json:"environmentOverrides"`
	} `json:"data"`
}
