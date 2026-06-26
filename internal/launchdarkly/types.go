package launchdarkly

// ============================================================================
// Metric types (consumed by `metrics convert`)
// ============================================================================

// MetricPost is the request body for creating an LD metric via the REST API.
type MetricPost struct {
	Key                 string           `json:"key"`
	Kind                string           `json:"kind"`
	Name                string           `json:"name,omitempty"`
	Description         string           `json:"description,omitempty"`
	EventKey            string           `json:"eventKey,omitempty"`
	IsNumeric           *bool            `json:"isNumeric,omitempty"`
	SuccessCriteria     string           `json:"successCriteria,omitempty"`
	UnitAggregationType string           `json:"unitAggregationType,omitempty"`
	AnalysisType        string           `json:"analysisType,omitempty"`
	RandomizationUnits  []string         `json:"randomizationUnits,omitempty"`
	Unit                string           `json:"unit,omitempty"`
	Tags                []string         `json:"tags,omitempty"`
	EventDefault        *EventDefault    `json:"eventDefault,omitempty"`
	DataSource          *DataSource      `json:"dataSource,omitempty"`
	Denominator         *DenominatorPost `json:"denominator,omitempty"`
}

// DenominatorPost configures the denominator term of a ratio metric.
// The numerator's equivalents are top-level fields on MetricPost.
type DenominatorPost struct {
	EventName            string `json:"eventName"`
	IsNumeric            bool   `json:"isNumeric"`
	UnitAggregationType  string `json:"unitAggregationType"`
	UnitAggregationField string `json:"unitAggregationField,omitempty"`
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
