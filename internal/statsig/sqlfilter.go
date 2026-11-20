package statsig

import "strings"

const conditionSQLFilter = "sql_filter"

// SQLFilter returns the SQL of a sql_filter criterion. Statsig keeps it as the
// criterion's only value.
func (c Criterion) SQLFilter() (string, bool) {
	if c.Condition != conditionSQLFilter || len(c.Values) != 1 {
		return "", false
	}
	sql := strings.TrimSpace(c.Values[0])
	return sql, sql != ""
}

// SQLFilterColumn is the name warehouse gives the column that holds a SQL filter's
// result, unless the data source already uses it: the filter's words, lowercase,
// joined by underscores, with comparisons spelled out (lower_region_eq_eu).
func SQLFilterColumn(sql string) string {
	var words []string
	for s := strings.TrimSpace(sql); s != ""; {
		if op := comparisonWord(s); op != "" {
			words = append(words, sqlComparisonWords[op])
			s = s[len(op):]
			continue
		}
		n := 0
		for n < len(s) && isWordByte(s[n]) {
			n++
		}
		if w := strings.Trim(s[:n], "_"); w != "" {
			words = append(words, strings.ToLower(w))
		}
		s = s[max(n, 1):]
	}
	name := strings.Join(words, "_")
	if len(name) > maxSQLFilterColumnLen {
		if cut := strings.LastIndexByte(name[:maxSQLFilterColumnLen+1], '_'); cut > 0 {
			name = name[:cut]
		} else {
			name = name[:maxSQLFilterColumnLen]
		}
	}
	switch {
	case name == "":
		return "sql_filter"
	case name[0] >= '0' && name[0] <= '9':
		return "sql_" + name
	case reservedColumnNames[name]:
		return name + "_filter"
	}
	return name
}

// Long names are cut at an underscore, to stay readable.
const maxSQLFilterColumnLen = 60

var sqlComparisonWords = map[string]string{">=": "ge", "<=": "le", "<>": "ne", "!=": "ne", "=": "eq", ">": "gt", "<": "lt"}

func comparisonWord(s string) string {
	for _, op := range []string{">=", "<=", "<>", "!=", "=", ">", "<"} {
		if strings.HasPrefix(s, op) {
			return op
		}
	}
	return ""
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// reservedColumnNames are SQL words a whole name may come out as but no column may
// be called.
var reservedColumnNames = map[string]bool{
	"all": true, "and": true, "any": true, "between": true, "case": true, "current_date": true,
	"current_time": true, "current_timestamp": true, "current_user": true, "distinct": true, "else": true,
	"exists": true, "false": true, "from": true, "in": true, "is": true, "like": true, "localtime": true,
	"localtimestamp": true, "not": true, "null": true, "or": true, "select": true, "some": true,
	"sysdate": true, "then": true, "true": true, "when": true, "where": true,
}
