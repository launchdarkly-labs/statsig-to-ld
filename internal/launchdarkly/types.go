package launchdarkly

// MetricPost is the request body for creating an LD metric via the REST API.
type MetricPost struct {
	Key                 string        `json:"key"`
	Kind                string        `json:"kind"`
	Name                string        `json:"name,omitempty"`
	Description         string        `json:"description,omitempty"`
	EventKey            string        `json:"eventKey,omitempty"`
	IsNumeric           *bool         `json:"isNumeric,omitempty"`
	SuccessCriteria     string        `json:"successCriteria,omitempty"`
	UnitAggregationType string        `json:"unitAggregationType,omitempty"`
	AnalysisType        string        `json:"analysisType,omitempty"`
	RandomizationUnits  []string      `json:"randomizationUnits,omitempty"`
	Unit                string        `json:"unit,omitempty"`
	Tags                []string      `json:"tags,omitempty"`
	EventDefault        *EventDefault `json:"eventDefault,omitempty"`
	DataSource          *DataSource   `json:"dataSource,omitempty"`
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
