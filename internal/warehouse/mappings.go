package warehouse

import (
	"strings"

	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
)

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

// IsTimestampType reports whether a warehouse column type can hold a data source's
// timestamp: a timestamp, date, or datetime type.
func IsTimestampType(columnType string) bool {
	t := normalizeColumnType(columnType)
	return strings.HasPrefix(t, "TIMESTAMP") || t == "DATE" || t == "DATETIME"
}

// IsNumericType ports LaunchDarkly's isNumeric (metric_data_sources_preview.go), which
// picks the preview's value column.
func IsNumericType(columnType string) bool {
	switch normalizeColumnType(columnType) {
	case "NUMBER", "DECIMAL", "DEC", "NUMERIC", "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "BYTEINT", "FLOAT",
		"FLOAT4", "FLOAT8", "DOUBLE", "DOUBLE PRECISION", "REAL", "DECFLOAT",
		"FIXED",
		"LONG", "SHORT", "BYTE",
		"INT2", "INT4", "INT8",
		"INT64", "FLOAT64", "BIGNUMERIC", "BIGDECIMAL",
		"UINT8", "UINT16", "UINT32", "UINT64", "UINT128", "UINT256",
		"INT16", "INT32", "INT128", "INT256", "FLOAT32", "BFLOAT16",
		"DECIMAL32", "DECIMAL64", "DECIMAL128", "DECIMAL256":
		return true
	}
	return false
}

// normalizeColumnType upper-cases a type, unwraps Nullable(...) and LowCardinality(...),
// and drops a parameter list such as (38,0), as LaunchDarkly does.
func normalizeColumnType(columnType string) string {
	t := strings.ToUpper(strings.TrimSpace(columnType))
	for {
		unwrapped := false
		for _, wrapper := range []string{"NULLABLE(", "LOWCARDINALITY("} {
			if strings.HasPrefix(t, wrapper) && strings.HasSuffix(t, ")") {
				t = strings.TrimSpace(t[len(wrapper) : len(t)-1])
				unwrapped = true
			}
		}
		if !unwrapped {
			break
		}
	}
	if i := strings.Index(t, "("); i > 0 && strings.HasSuffix(t, ")") {
		t = strings.TrimSpace(t[:i])
	}
	return t
}
