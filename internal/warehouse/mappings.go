package warehouse

import j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"

// ExportMappings are the column mappings a Statsig source defines itself, in its case.
// The create path's defaults (a "timestamp" column, a user_id user context) and the
// preview's guesses are left out, so an update never swaps a working mapping for a guess.
type ExportMappings struct {
	Timestamp string
	Value     string
	// Contexts is nil when the source maps no id types.
	Contexts map[string]string
}

// MappingsFromExport derives them as MapMetricSourceToDataSource does.
func MappingsFromExport(source map[string]any) ExportMappings {
	cm, _ := MapMetricSourceToDataSource(source, "", "", "")["columnMappings"].(map[string]any)
	m := ExportMappings{Value: j.GetStr(cm, "valueColumn")}
	if j.GetStr(source, "timestampColumn") != "" {
		m.Timestamp = j.GetStr(cm, "timestampColumn")
	}
	for _, raw := range j.GetSlice(source, "idTypeMapping") {
		if mapping, ok := raw.(map[string]any); ok && j.GetStr(mapping, "column") != "" {
			m.Contexts, _ = cm["contexts"].(map[string]string)
			break
		}
	}
	return m
}
