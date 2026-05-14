// Package warehouse provides warehouse-native migration logic including
// config extraction, data source mapping, and interactive setup wizards.
package warehouse

import (
	"fmt"
	"regexp"
	"strings"

	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
)

var reFromThreePart = regexp.MustCompile(`(?i)\bFROM\s+([A-Za-z_]\w*)\.([A-Za-z_]\w*)\.([A-Za-z_]\w*)`)
var reFromTwoPart = regexp.MustCompile(`(?i)\bFROM\s+([A-Za-z_]\w*)\.([A-Za-z_]\w*)`)

// ExtractFromWHConnections extracts warehouse config from Statsig's wh_connections response.
func ExtractFromWHConnections(whConn map[string]any) map[string]string {
	if whConn == nil {
		return nil
	}
	pf := map[string]string{}

	// Snowflake
	if acct := j.GetStr(whConn, "accountName"); acct != "" {
		pf["warehouse_type"] = "snowflake"
		pf["snowflake_host"] = fmt.Sprintf("%s.snowflakecomputing.com", acct)
		pf["snowflake_account"] = acct
	}
	if host := j.GetStr(whConn, "snowflakeHostAddress"); host != "" {
		pf["warehouse_type"] = "snowflake"
		pf["snowflake_host"] = host
	}
	if v := j.GetStr(whConn, "stagingDatabaseName"); v != "" {
		pf["database"] = v
	}
	if v := j.GetStr(whConn, "stagingSchemaName"); v != "" {
		pf["schema"] = v
	}
	if v := j.GetStr(whConn, "computeWarehouse"); v != "" {
		pf["snowflake_warehouse"] = v
	}

	// BigQuery
	if v := j.GetStr(whConn, "project"); v != "" {
		pf["warehouse_type"] = "bigquery"
		pf["gcp_project"] = v
	}
	if v := j.GetStr(whConn, "consoleComputeProject"); v != "" {
		pf["gcp_project"] = v
	}
	if v := j.GetStr(whConn, "stagingDataset"); v != "" {
		pf["dataset"] = v
	}

	// Databricks
	if j.GetStr(whConn, "host") != "" && j.GetStr(whConn, "path") != "" {
		pf["warehouse_type"] = "databricks"
		pf["databricks_host"] = j.GetStr(whConn, "host")
		pf["databricks_http_path"] = j.GetStr(whConn, "path")
	}
	if v := j.GetStr(whConn, "catalog"); v != "" {
		pf["catalog"] = v
	}
	if v := j.GetStr(whConn, "schema"); v != "" {
		pf["schema"] = v
	}

	// Redshift
	if j.GetStr(whConn, "host") != "" && j.GetStr(whConn, "port") != "" {
		if pf["warehouse_type"] != "databricks" {
			pf["warehouse_type"] = "redshift"
			pf["redshift_host"] = j.GetStr(whConn, "host")
			pf["redshift_port"] = j.GetStr(whConn, "port")
		}
	}
	if v := j.GetStr(whConn, "database"); v != "" {
		pf["database"] = v
	}

	return pf
}

// ExtractFromSQLParsing extracts database/schema from metric source SQL queries.
func ExtractFromSQLParsing(metricSources []map[string]any) map[string]string {
	pf := map[string]string{}
	for _, source := range metricSources {
		tableName := j.GetStr(source, "tableName")
		if tableName != "" {
			parts := strings.Split(tableName, ".")
			if len(parts) == 3 {
				setDefault(pf, "database", parts[0])
				setDefault(pf, "schema", parts[1])
				return pf
			}
			if len(parts) == 2 {
				setDefault(pf, "schema", parts[0])
				return pf
			}
		}

		sql := j.GetStr(source, "sql")
		if sql != "" {
			if m := reFromThreePart.FindStringSubmatch(sql); m != nil {
				setDefault(pf, "database", m[1])
				setDefault(pf, "schema", m[2])
				return pf
			}
			if m := reFromTwoPart.FindStringSubmatch(sql); m != nil {
				setDefault(pf, "schema", m[1])
				return pf
			}
		}
	}
	return pf
}

// DetectWarehouseType determines the warehouse type from connection config and SQL.
func DetectWarehouseType(whConnections map[string]any, metricSources []map[string]any) string {
	if whConnections != nil {
		extracted := ExtractFromWHConnections(whConnections)
		if wt, ok := extracted["warehouse_type"]; ok && wt != "" {
			return wt
		}
	}
	return detectWarehouseTypeFromSQL(metricSources)
}

func detectWarehouseTypeFromSQL(metricSources []map[string]any) string {
	for _, source := range metricSources {
		sql := strings.ToUpper(j.GetStr(source, "sql"))
		table := strings.ToUpper(j.GetStr(source, "tableName"))
		combined := sql + " " + table

		for _, kw := range []string{"SNOWFLAKE", "FLATTEN(", "VARIANT", "::"} {
			if strings.Contains(combined, kw) {
				return "snowflake"
			}
		}
		for _, kw := range []string{"BIGQUERY", "UNNEST(", "STRUCT(", "`"} {
			if strings.Contains(combined, kw) {
				return "bigquery"
			}
		}
		for _, kw := range []string{"DATABRICKS", "DELTA", "CATALOG."} {
			if strings.Contains(combined, kw) {
				return "databricks"
			}
		}
		for _, kw := range []string{"REDSHIFT", "GETDATE(", "STRTOL("} {
			if strings.Contains(combined, kw) {
				return "redshift"
			}
		}
	}
	return ""
}

// MergeConfigs merges two config maps, with override taking precedence.
func MergeConfigs(base, override map[string]string) map[string]string {
	merged := map[string]string{}
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}

func setDefault(m map[string]string, key, val string) {
	if _, ok := m[key]; !ok {
		m[key] = val
	}
}
