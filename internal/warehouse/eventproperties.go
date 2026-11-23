package warehouse

import (
	"fmt"
	"slices"
	"strings"

	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
)

// EventPropertyColumn is one column of a LaunchDarkly data source and, when
// LaunchDarkly would refuse to slice a metric's results by it, why.
type EventPropertyColumn struct {
	Name string
	// Unsupported is empty when LaunchDarkly accepts the column as an event property.
	Unsupported string
}

// maxEventPropertyNameLength is LaunchDarkly's limit on an event property column's name.
const maxEventPropertyNameLength = 255

var eventPropertyIntegerTypes = []string{
	"INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "BYTEINT",
	"INT2", "INT4", "INT8", "INT16", "INT32", "INT64", "INT128", "INT256",
	"UINT8", "UINT16", "UINT32", "UINT64", "UINT128", "UINT256",
	"LONG", "SHORT", "BYTE",
}

var eventPropertyDecimalTypes = []string{"NUMBER", "DECIMAL", "DEC", "NUMERIC", "FIXED"}

// EventPropertyColumns applies LaunchDarkly's event property rules to each column in a
// data source's column mappings: string, boolean and integer columns (a decimal only
// with scale 0), other than the timestamp and context key columns. LaunchDarkly
// rejects the whole metric when any of its event properties breaks a rule.
func EventPropertyColumns(columnMappings map[string]any) []EventPropertyColumn {
	timestamp := j.GetStr(columnMappings, "timestampColumn")
	keyColumns := map[string]string{}
	for kind, col := range j.GetMap(columnMappings, "contexts") {
		if s, ok := col.(string); ok {
			keyColumns[s] = kind
		}
	}
	var out []EventPropertyColumn
	for _, raw := range j.GetSlice(columnMappings, "columns") {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := j.GetStr(c, "name")
		if name == "" {
			continue
		}
		col := EventPropertyColumn{Name: name}
		switch kind, isKey := keyColumns[name]; {
		case len(name) > maxEventPropertyNameLength:
			col.Unsupported = fmt.Sprintf("its name is longer than %d characters", maxEventPropertyNameLength)
		case name == timestamp:
			col.Unsupported = "it is the timestamp column"
		case isKey:
			col.Unsupported = fmt.Sprintf("it is the %s key column", kind)
		default:
			col.Unsupported = unsupportedEventPropertyType(j.GetStr(c, "type"), c["scale"])
		}
		out = append(out, col)
	}
	return out
}

func unsupportedEventPropertyType(colType string, scale any) string {
	t := normalizeColumnType(colType)
	switch {
	case t == "":
		return "it has no recorded type"
	case strings.ContainsAny(t, "<["):
		return fmt.Sprintf("its type is %q", colType)
	case strings.Contains(t, "CHAR"), strings.Contains(t, "TEXT"), strings.Contains(t, "STRING"),
		t == "BOOLEAN", t == "BOOL", slices.Contains(eventPropertyIntegerTypes, t):
		return ""
	case slices.Contains(eventPropertyDecimalTypes, t):
		s, ok := scale.(float64)
		switch {
		case !ok:
			return fmt.Sprintf("its type is %q with no recorded scale", colType)
		case s != 0:
			return fmt.Sprintf("its type is %q with scale %v", colType, s)
		}
		return ""
	}
	return fmt.Sprintf("its type is %q", colType)
}
