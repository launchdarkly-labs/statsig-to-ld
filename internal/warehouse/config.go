// Package warehouse provides warehouse-native migration logic including
// config extraction, data source mapping, and interactive setup wizards.
package warehouse

import (
	"fmt"
	"regexp"
	"slices"
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

// SupportedWarehouseTypes are the warehouse types this command can set up.
var SupportedWarehouseTypes = []string{"snowflake", "bigquery", "databricks", "redshift"}

// WarehouseTypeSource records where a resolved warehouse type came from. A type
// inferred from SQL is a guess, and callers must not create LaunchDarkly
// resources off a guess: the type selects the integration key, so getting it
// wrong points every data source at the wrong warehouse.
type WarehouseTypeSource int

const (
	WarehouseTypeUnknown WarehouseTypeSource = iota
	WarehouseTypeFromFlag
	WarehouseTypeFromConnection
	WarehouseTypeGuessedFromSQL
)

// IsConfident reports whether the type was stated rather than inferred.
func (s WarehouseTypeSource) IsConfident() bool {
	return s == WarehouseTypeFromFlag || s == WarehouseTypeFromConnection
}

func (s WarehouseTypeSource) String() string {
	switch s {
	case WarehouseTypeFromFlag:
		return "--warehouse-type"
	case WarehouseTypeFromConnection:
		return "Statsig warehouse connection"
	case WarehouseTypeGuessedFromSQL:
		return "guessed from metric source SQL"
	default:
		return "unknown"
	}
}

// ResolveWarehouseType determines the warehouse type and reports how it was
// determined. Order: an explicit --warehouse-type, then Statsig's warehouse
// connection config, then a guess from metric source SQL.
func ResolveWarehouseType(flag string, whConnections map[string]any, metricSources []map[string]any) (string, WarehouseTypeSource, error) {
	if flag != "" {
		typ := strings.ToLower(strings.TrimSpace(flag))
		if !slices.Contains(SupportedWarehouseTypes, typ) {
			return "", WarehouseTypeUnknown, fmt.Errorf("unsupported warehouse type %q: must be one of %s",
				flag, strings.Join(SupportedWarehouseTypes, ", "))
		}
		return typ, WarehouseTypeFromFlag, nil
	}
	if whConnections != nil {
		if wt := ExtractFromWHConnections(whConnections)["warehouse_type"]; wt != "" {
			return wt, WarehouseTypeFromConnection, nil
		}
	}
	if guess := detectWarehouseTypeFromSQL(metricSources); guess != "" {
		return guess, WarehouseTypeGuessedFromSQL, nil
	}
	return "", WarehouseTypeUnknown, nil
}

// DetectWarehouseType determines the warehouse type from connection config and
// SQL, without saying which of the two it used. Prefer ResolveWarehouseType,
// which reports provenance so a guess can be treated as one.
func DetectWarehouseType(whConnections map[string]any, metricSources []map[string]any) string {
	typ, _, _ := ResolveWarehouseType("", whConnections, metricSources)
	return typ
}

// sqlDialectMarkers are substrings that only appear in one warehouse's SQL.
// Deliberately narrow. Tokens like "::" (a cast in several dialects, and how
// Statsig writes its own metric IDs), a bare backtick (ordinary quoting),
// "VARIANT" (a very common column name in experiment tables), and "DELTA" (any
// identifier containing "delta") all matched far more than the warehouse they
// were meant to identify. A missing guess is fine; a confident wrong one is not.
var sqlDialectMarkers = []struct {
	warehouse string
	markers   []string
}{
	{"snowflake", []string{"SNOWFLAKE", "FLATTEN("}},
	{"bigquery", []string{"BIGQUERY", "UNNEST("}},
	{"databricks", []string{"DATABRICKS"}},
	{"redshift", []string{"REDSHIFT", "GETDATE(", "STRTOL("}},
}

// detectWarehouseTypeFromSQL guesses the warehouse from every source's SQL. It
// scans all sources rather than returning on the first match, and returns
// nothing when they disagree: matching on the first source made the answer
// depend on the order Statsig happened to return them in.
func detectWarehouseTypeFromSQL(metricSources []map[string]any) string {
	seen := map[string]bool{}
	for _, source := range metricSources {
		combined := strings.ToUpper(j.GetStr(source, "sql") + " " + j.GetStr(source, "tableName"))
		for _, d := range sqlDialectMarkers {
			for _, kw := range d.markers {
				if strings.Contains(combined, kw) {
					seen[d.warehouse] = true
					break
				}
			}
		}
	}
	if len(seen) != 1 {
		return ""
	}
	for warehouse := range seen {
		return warehouse
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
