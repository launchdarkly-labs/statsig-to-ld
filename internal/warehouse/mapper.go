package warehouse

import (
	"regexp"
	"strings"

	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
)

// WarehouseTypes maps warehouse type to experimentation integration key.
var WarehouseTypes = map[string]string{
	"snowflake":  "snowflake-experimentation",
	"bigquery":   "bigquery-experimentation",
	"databricks": "databricks-experimentation",
	"redshift":   "redshift-experimentation",
}

// DataExportTypes maps warehouse type to data export destination kind.
var DataExportTypes = map[string]string{
	"snowflake":  "snowflake-v2",
	"bigquery":   "bigquery",
	"databricks": "databricks",
	"redshift":   "redshift",
}

// MetricTypeConfig defines how a Statsig metric type maps to LD fields.
type MetricTypeConfig struct {
	IsNumeric           bool
	UnitAggregationType string
	AnalysisType        string
}

// MetricTypeMap maps Statsig metric types to LD metric configuration.
var MetricTypeMap = map[string]MetricTypeConfig{
	"sum":                {IsNumeric: true, UnitAggregationType: "sum"},
	"mean":               {IsNumeric: true, UnitAggregationType: "average"},
	"event_count":        {IsNumeric: true, UnitAggregationType: "sum"},
	"event_count_custom": {IsNumeric: true, UnitAggregationType: "sum"},
	"count":              {IsNumeric: true, UnitAggregationType: "sum"},
	"count_distinct":     {IsNumeric: true, UnitAggregationType: "sum"},
	"latest_value":       {IsNumeric: true, UnitAggregationType: "average"},
	"percentile":         {IsNumeric: true, AnalysisType: "percentile"},
	"user":               {IsNumeric: false},
	"user_count":         {IsNumeric: false},
	"conversion":         {IsNumeric: false},
	"participation_rate": {IsNumeric: false},
	"retention":          {IsNumeric: false},
}

// UnsupportedMetricTypes lists Statsig metric types with no LD equivalent.
var UnsupportedMetricTypes = map[string]bool{
	"ratio":     true,
	"funnel":    true,
	"composite": true,
	"undefined": true,
}

var reNonAlnumKey = regexp.MustCompile(`[^a-z0-9\-._]`)
var reMultiDash = regexp.MustCompile(`-{2,}`)
var reNonAlnumTag = regexp.MustCompile(`[^a-zA-Z0-9._\-]`)

// SanitizeKey converts a name to a valid LD resource key.
func SanitizeKey(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	key = reNonAlnumKey.ReplaceAllString(key, "-")
	key = reMultiDash.ReplaceAllString(key, "-")
	key = strings.Trim(key, "-")
	return j.Truncate(key, 256)
}

// SanitizeTag converts a tag to a valid LD tag.
func SanitizeTag(tag string) string {
	t := strings.TrimSpace(tag)
	t = reNonAlnumTag.ReplaceAllString(t, "-")
	t = reMultiDash.ReplaceAllString(t, "-")
	t = strings.Trim(t, "-")
	return j.Truncate(t, 64)
}

// SanitizeTags sanitizes and deduplicates a list of tags.
func SanitizeTags(tags []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, tag := range tags {
		cleaned := SanitizeTag(tag)
		if cleaned != "" && !seen[cleaned] {
			seen[cleaned] = true
			result = append(result, cleaned)
		}
	}
	return result
}

// MapMetricSourceToDataSource converts a Statsig metric source to an LD data source request body.
func MapMetricSourceToDataSource(source map[string]any, envKey, integrationKey string) map[string]any {
	name := j.GetStr(source, "name")
	if name == "" {
		name = "unnamed-source"
	}
	key := SanitizeKey(name)

	sqlQuery := j.GetStr(source, "sql")
	tableName := j.GetStr(source, "tableName")
	sourceType := j.GetStr(source, "sourceType")
	if sourceType == "" {
		sourceType = "query"
	}

	rawTags := j.GetStrSlice(source, "tags")
	body := map[string]any{
		"key":            key,
		"name":           name,
		"integrationKey": integrationKey,
		"environmentKey": envKey,
		"tags":           SanitizeTags(rawTags),
	}

	if desc := j.GetStr(source, "description"); desc != "" {
		body["description"] = desc
	}

	if sourceType == "table" && tableName != "" {
		body["tableName"] = tableName
	} else if sqlQuery != "" {
		body["sqlQuery"] = sqlQuery
	} else if tableName != "" {
		body["tableName"] = tableName
	} else {
		body["tableName"] = name
	}

	timestampCol := j.GetStr(source, "timestampColumn")
	if timestampCol == "" {
		timestampCol = "timestamp"
	}

	contexts := map[string]string{}
	idTypeMappings := j.GetSlice(source, "idTypeMapping")
	for _, raw := range idTypeMappings {
		mapping, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		statsigUnit := j.GetStr(mapping, "statsigUnitID")
		column := j.GetStr(mapping, "column")
		if column == "" {
			continue
		}
		if strings.Contains(strings.ToLower(statsigUnit), "user") {
			contexts["user"] = column
		} else {
			contexts[SanitizeKey(statsigUnit)] = column
		}
	}
	if len(contexts) == 0 {
		contexts["user"] = "user_id"
	}

	columnMappings := map[string]any{
		"timestampColumn": timestampCol,
		"contexts":        contexts,
	}

	customFields := j.GetSlice(source, "customFieldMapping")
	for _, raw := range customFields {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fieldName := strings.ToLower(j.GetStr(field, "fieldName"))
		colVal := j.GetStr(field, "formula")
		if colVal == "" {
			colVal = j.GetStr(field, "column")
		}
		if strings.Contains(fieldName, "key") || strings.Contains(fieldName, "event") {
			columnMappings["keyColumn"] = colVal
		}
		if strings.Contains(fieldName, "value") || strings.Contains(fieldName, "amount") {
			columnMappings["valueColumn"] = colVal
		}
	}

	// Build fallback columns list (used when preview is unavailable)
	type colDef struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	seen := map[string]bool{}
	var columns []colDef
	addCol := func(name, colType string) {
		if name != "" && !seen[name] {
			columns = append(columns, colDef{Name: name, Type: colType})
			seen[name] = true
		}
	}
	addCol(timestampCol, "TIMESTAMP")
	for _, col := range contexts {
		addCol(col, "TEXT")
	}
	if kc, ok := columnMappings["keyColumn"].(string); ok {
		addCol(kc, "TEXT")
	}
	if vc, ok := columnMappings["valueColumn"].(string); ok {
		addCol(vc, "FLOAT")
	}

	columnMappings["columns"] = columns
	body["columnMappings"] = columnMappings
	return body
}

// MapMetric converts a Statsig metric to an LD metric request body.
// Returns (body, warning). body is nil for unsupported types.
func MapMetric(statsigMetric map[string]any, dsKeyLookup map[string]string) (map[string]any, string) {
	metricType := strings.ToLower(j.GetStr(statsigMetric, "type"))
	if metricType == "" {
		metricType = "custom"
	}
	name := j.GetStr(statsigMetric, "name")
	metricID := j.GetStr(statsigMetric, "id")
	if metricID == "" {
		metricID = name
	}
	key := SanitizeKey(metricID)
	warning := ""

	if UnsupportedMetricTypes[metricType] {
		return nil, "\"" + name + "\" has unsupported type \"" + metricType + "\" -- no direct LD equivalent"
	}

	rawTags := j.GetStrSlice(statsigMetric, "tags")
	displayName := name
	if len(displayName) > 768 {
		displayName = displayName[:768]
	}
	if displayName == "" {
		displayName = key
	}

	body := map[string]any{
		"key":  key,
		"name": displayName,
		"kind": "custom",
		"tags": SanitizeTags(rawTags),
	}

	if desc := j.GetStr(statsigMetric, "description"); desc != "" {
		body["description"] = j.Truncate(desc, 768)
	}

	tc, found := MetricTypeMap[metricType]
	if !found {
		tc = MetricTypeConfig{IsNumeric: true}
	}
	body["isNumeric"] = tc.IsNumeric

	if tc.UnitAggregationType != "" {
		body["unitAggregationType"] = tc.UnitAggregationType
	}
	if tc.AnalysisType != "" {
		body["analysisType"] = tc.AnalysisType
		if tc.AnalysisType == "percentile" {
			pct := j.GetFloat(statsigMetric, "percentile")
			v := int(pct)
			if v < 1 {
				v = 50
			}
			if v > 99 {
				v = 99
			}
			body["percentileValue"] = v
			body["eventDefault"] = map[string]any{"disabled": true}
		}
	}

	if tc.IsNumeric {
		body["eventKey"] = key
		unit := j.GetStr(statsigMetric, "unit")
		if unit == "" {
			unit = "count"
		}
		body["unit"] = unit

		dir := strings.ToLower(j.GetStr(statsigMetric, "directionality"))
		if dir == "decrease" {
			body["successCriteria"] = "LowerThanBaseline"
		} else {
			body["successCriteria"] = "HigherThanBaseline"
		}
	} else {
		body["eventKey"] = key
	}

	whNative := j.GetMap(statsigMetric, "warehouseNative")
	if whNative != nil {
		metricSources := j.GetSlice(whNative, "metricSources")
		if len(metricSources) > 0 {
			if first, ok := metricSources[0].(map[string]any); ok {
				sourceName := j.GetStr(first, "metricSourceName")
				if dsKey, ok := dsKeyLookup[sourceName]; ok {
					body["dataSource"] = map[string]any{"key": dsKey}
				} else {
					warning = "\"" + name + "\" references metric source \"" + sourceName + "\" which was not migrated"
				}
			}
		}
	}

	return body, warning
}

// ReconcileColumnMappings uses the preview response to fix column mapping references.
func ReconcileColumnMappings(cm map[string]any, preview map[string]any, realColumns []map[string]any) {
	actual := map[string]string{}
	for _, col := range realColumns {
		name, _ := col["name"].(string)
		if name != "" {
			actual[strings.ToLower(name)] = name
		}
	}

	if ts := j.GetStr(preview, "timestampColumn"); ts != "" {
		cm["timestampColumn"] = ts
	} else if v, ok := cm["timestampColumn"].(string); ok {
		if real, found := actual[strings.ToLower(v)]; found {
			cm["timestampColumn"] = real
		}
	}

	if kc := j.GetStr(preview, "keyColumn"); kc != "" {
		cm["keyColumn"] = kc
	} else if v, ok := cm["keyColumn"].(string); ok {
		if real, found := actual[strings.ToLower(v)]; found {
			cm["keyColumn"] = real
		}
	}

	if vc := j.GetStr(preview, "valueColumn"); vc != "" {
		cm["valueColumn"] = vc
	} else if v, ok := cm["valueColumn"].(string); ok {
		if real, found := actual[strings.ToLower(v)]; found {
			cm["valueColumn"] = real
		}
	}

	if contexts, ok := cm["contexts"].(map[string]string); ok {
		seen := map[string]bool{}
		for kind, col := range contexts {
			if real, found := actual[strings.ToLower(col)]; found {
				if seen[real] {
					delete(contexts, kind)
				} else {
					contexts[kind] = real
					seen[real] = true
				}
			}
		}
	}
}

// BuildPreviewSQL returns the SQL for a data source preview query.
func BuildPreviewSQL(source map[string]any) string {
	sqlQuery := j.GetStr(source, "sql")
	tableName := j.GetStr(source, "tableName")
	sourceType := j.GetStr(source, "sourceType")

	if sourceType == "table" && tableName != "" {
		return "SELECT * FROM " + tableName + " LIMIT 0"
	}
	if sqlQuery != "" {
		return sqlQuery
	}
	if tableName != "" {
		return "SELECT * FROM " + tableName + " LIMIT 0"
	}
	return ""
}

// ExtractPreviewColumns extracts columns from a preview response with full metadata.
func ExtractPreviewColumns(preview map[string]any) []map[string]any {
	rawCols := j.GetSlice(preview, "columns")
	if rawCols == nil {
		return nil
	}
	var cols []map[string]any
	for _, raw := range rawCols {
		col, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := j.GetStr(col, "name")
		colType := j.GetStr(col, "type")
		if name == "" || colType == "" {
			continue
		}
		entry := map[string]any{"name": name, "type": colType}
		if v, ok := col["length"]; ok {
			entry["length"] = v
		}
		if v, ok := col["precision"]; ok {
			entry["precision"] = v
		}
		if v, ok := col["scale"]; ok {
			entry["scale"] = v
		}
		if v, ok := col["nullable"]; ok {
			entry["nullable"] = v
		}
		cols = append(cols, entry)
	}
	return cols
}
