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
}

// MetricEvent represents an event definition within a Statsig metric.
type MetricEvent struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	MetadataKey string      `json:"metadataKey"`
	Criteria    []Criterion `json:"criteria"`
}

// Criterion represents a filter condition on a metric event.
type Criterion struct {
	Type      string   `json:"type"`
	Column    string   `json:"column"`
	Condition string   `json:"condition"`
	Values    []string `json:"values"`
}

// WarehouseNative contains Statsig Warehouse Native-specific metric configuration.
type WarehouseNative struct {
	WinsorizationHigh *float64 `json:"winsorizationHigh"`
	WinsorizationLow  *float64 `json:"winsorizationLow"`
	Cap               *float64 `json:"cap"`
	Percentile        *float64 `json:"percentile"`
	UseLogTransform   *bool    `json:"useLogTransform"`
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
