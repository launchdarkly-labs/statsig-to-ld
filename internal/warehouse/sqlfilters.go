package warehouse

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// Redshift requires an alias on a FROM subquery.
const sqlFiltersAlias = "statsig_filters"

const (
	sqlFiltersHead = "SELECT *, "
	sqlFiltersTail = "\n) AS " + sqlFiltersAlias
)

var reSQLFilterAlias = regexp.MustCompile(`\) AS ([A-Za-z_][A-Za-z0-9_]*)`)

// SourceColumns are the columns this command adds to a source's query: its custom
// fields, then a true/false column for each Statsig SQL filter its metrics use.
type SourceColumns struct {
	Fields  []CustomField
	Filters []CustomField
}

func (c SourceColumns) Empty() bool { return len(c.Fields) == 0 && len(c.Filters) == 0 }

// All lists the custom fields, then the SQL filters.
func (c SourceColumns) All() []CustomField { return append(slices.Clone(c.Fields), c.Filters...) }

// SQLFilterColumns gives each SQL filter a column named after its SQL, numbered from
// _2 where the name, ignoring case, is an earlier one's or in taken. Formula holds
// the filter's SQL.
func SQLFilterColumns(sqls []string, taken ...string) []CustomField {
	used := usedNames(taken)
	out := make([]CustomField, 0, len(sqls))
	for _, sql := range sqls {
		sql = strings.TrimSpace(sql)
		out = append(out, CustomField{Key: freeName(statsig.SQLFilterColumn(sql), used), Formula: sql})
	}
	return out
}

// RenumberSQLFilters renumbers each filter of c whose column columns, a query's
// result columns, holds more than once, ignoring case, except those named in keep. It
// reports whether it renamed one.
func RenumberSQLFilters(c SourceColumns, columns []string, keep ...string) (SourceColumns, bool) {
	clash := CollidingCustomFields(columns, c.Filters)
	if len(clash) == 0 {
		return c, false
	}
	used := usedNames(append(slices.Clone(columns), columnNames(c.All())...))
	c.Filters = slices.Clone(c.Filters)
	renamed := false
	for i, f := range c.Filters {
		if slices.Contains(clash, f.Key) && !slices.Contains(keep, f.Key) {
			c.Filters[i].Key = freeName(statsig.SQLFilterColumn(f.Formula), used)
			renamed = true
		}
	}
	return c, renamed
}

// ColumnTypes maps the lowercase name of each column in columns, a data source's
// column list, to its type. A query lists the source's columns before those this
// command adds, so the first column by a name wins. It is nil when the list names none.
func ColumnTypes(columns any) map[string]string {
	var out map[string]string
	add := func(m map[string]any) {
		if name := j.GetStr(m, "name"); name != "" {
			if out == nil {
				out = map[string]string{}
			}
			if _, seen := out[strings.ToLower(name)]; !seen {
				out[strings.ToLower(name)] = j.GetStr(m, "type")
			}
		}
	}
	switch cols := columns.(type) {
	case []any:
		for _, c := range cols {
			if m, ok := c.(map[string]any); ok {
				add(m)
			}
		}
	case []map[string]any:
		for _, m := range cols {
			add(m)
		}
	case []FallbackColumn:
		for _, c := range cols {
			add(map[string]any{"name": c.Name, "type": c.Type})
		}
	}
	return out
}

// SameSQLFilter reports whether a and b are one filter's SQL.
func SameSQLFilter(a, b string) bool {
	norm := func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n") }
	return norm(a) == norm(b)
}

func usedNames(names []string) map[string]bool {
	used := make(map[string]bool, len(names))
	for _, n := range names {
		used[strings.ToLower(n)] = true
	}
	return used
}

// freeName returns base, or base numbered from _2, whichever used lacks, and marks
// it used.
func freeName(base string, used map[string]bool) string {
	name := base
	for n := 2; used[strings.ToLower(name)]; n++ {
		name = base + "_" + strconv.Itoa(n)
	}
	used[strings.ToLower(name)] = true
	return name
}

// namedAfter reports whether col is a name this command gives sql's column: its
// own, numbered, or the hash an earlier version used.
func namedAfter(col, sql string) bool {
	base := statsig.SQLFilterColumn(sql)
	if strings.EqualFold(col, base) || strings.EqualFold(col, LegacySQLFilterColumn(sql)) {
		return true
	}
	if len(col) <= len(base)+1 || !strings.EqualFold(col[:len(base)+1], base+"_") {
		return false
	}
	n, err := strconv.Atoi(col[len(base)+1:])
	return err == nil && n >= 2 && strconv.Itoa(n) == col[len(base)+1:]
}

// LegacySQLFilterColumn is the name an earlier version gave sql's column, a hash
// unique to the SQL.
func LegacySQLFilterColumn(sql string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sql)))
	return "sql_filter_" + hex.EncodeToString(sum[:8])
}

// ApplySourceColumns adds c to a MapMetricSourceToDataSource body's query. It reports
// whether c adds any column.
func ApplySourceColumns(body map[string]any, c SourceColumns, whType string) (bool, error) {
	if c.Empty() {
		return false, nil
	}
	sql, err := BuildSourceQuery(j.GetStr(body, "sqlQuery"), c, whType)
	if err != nil {
		return true, fmt.Errorf("data source %q: %w", j.GetStr(body, "key"), err)
	}
	body["sqlQuery"] = sql
	return true, nil
}

// BuildSourceQuery nests sql in a SELECT that adds the custom fields, and that in one
// that adds the SQL filters, so a filter can use a custom field.
func BuildSourceQuery(sql string, c SourceColumns, whType string) (string, error) {
	var err error
	if len(c.Fields) > 0 {
		sql, err = WithCustomFields(sql, c.Fields, whType)
	} else {
		sql, err = PrepareSourceSQL(sql, whType)
	}
	if err != nil || len(c.Filters) == 0 {
		return sql, err
	}
	cols := make([]string, len(c.Filters))
	for i, f := range c.Filters {
		expr, err := sqlFilterExpr(f, whType)
		if err != nil {
			return "", err
		}
		cols[i] = expr + " AS " + f.Key
	}
	return sqlFiltersHead + strings.Join(cols, ", ") + " FROM (\n" + sql + sqlFiltersTail, nil
}

// ListSQLFilterColumns adds filters' columns to cm's fallback column list, which a
// preview would replace.
func ListSQLFilterColumns(cm map[string]any, filters []CustomField) {
	cols, ok := cm["columns"].([]FallbackColumn)
	if !ok {
		return
	}
	for _, f := range filters {
		cols = append(cols, FallbackColumn{Name: f.Key, Type: "BOOLEAN"})
	}
	cm["columns"] = cols
}

// ValidateSQLFilter reports why BuildSourceQuery would refuse f.
func ValidateSQLFilter(f CustomField, whType string) error {
	_, err := sqlFilterExpr(f, whType)
	return err
}

func sqlFilterExpr(f CustomField, whType string) (string, error) {
	sql, lineComment, err := selectExpression(fmt.Sprintf("Statsig SQL filter %q", f.Formula), "SQL", f.Formula, whType)
	if err != nil {
		return "", err
	}
	return parenthesize(sql, lineComment), nil
}

// MissingColumns compares query, the inner query of a data source this command
// wrapped, with the one BuildSourceQuery builds from sql and c. ok is false unless
// query is sql with some of those columns: edited since, or built from other SQL or
// other custom fields. merged is what an update writes: c, plus the SQL filter
// columns query has and c lacks, which a bound metric may still filter on.
func MissingColumns(query, sql string, c SourceColumns, whType string) (missing, merged SourceColumns, ok bool) {
	base, have := splitSQLFilters(query, whType)
	prepared, err := PrepareSourceSQL(sql, whType)
	if err != nil {
		return SourceColumns{}, SourceColumns{}, false
	}
	switch {
	case SameSQL(base, prepared):
		missing.Fields = c.Fields
	case len(c.Fields) > 0:
		if !hasCustomFields(base, sql, c.Fields, whType) {
			return SourceColumns{}, SourceColumns{}, false
		}
	default:
		return SourceColumns{}, SourceColumns{}, false
	}
	merged.Fields = c.Fields
	merged.Filters = MergeSQLFilters(have, c.Filters, columnNames(c.Fields)...)
	if len(merged.Filters) > len(have) {
		missing.Filters = slices.Clone(merged.Filters[len(have):])
	}
	return missing, merged, true
}

// hasCustomFields reports whether query is sql with fields, as this version or an
// earlier one wrote them.
func hasCustomFields(query, sql string, fields []CustomField, whType string) bool {
	for _, bare := range []bool{false, true} {
		if built, err := withCustomFields(sql, fields, whType, bare); err == nil && SameSQL(query, built) {
			return true
		}
	}
	return false
}

// SQLFiltersIn lists the SQL filter columns of the layer BuildSourceQuery adds, when
// query has it outermost.
func SQLFiltersIn(query, whType string) []CustomField {
	_, have := splitSQLFilters(query, whType)
	return have
}

// MergeSQLFilters returns have, then the filters in want whose SQL have lacks,
// renumbered where have or taken already uses the name, ignoring case.
func MergeSQLFilters(have, want []CustomField, taken ...string) []CustomField {
	used := usedNames(append(columnNames(have), taken...))
	merged := slices.Clone(have)
	for _, f := range want {
		if slices.ContainsFunc(merged, func(h CustomField) bool { return SameSQLFilter(h.Formula, f.Formula) }) {
			continue
		}
		if used[strings.ToLower(f.Key)] {
			f.Key = freeName(statsig.SQLFilterColumn(f.Formula), used)
		}
		used[strings.ToLower(f.Key)] = true
		merged = append(merged, f)
	}
	return merged
}

func columnNames(fields []CustomField) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Key
	}
	return names
}

// splitSQLFilters undoes the SQL filter layer BuildSourceQuery adds. Each column must
// be named after its SQL, so an edited filter is not taken for one.
func splitSQLFilters(query, whType string) (string, []CustomField) {
	if base, filters, ok := readSQLFilters(query, whType); ok {
		return base, filters
	}
	if strings.Contains(query, "\r\n") {
		if base, filters, ok := readSQLFilters(strings.ReplaceAll(query, "\r\n", "\n"), whType); ok {
			return base, filters
		}
	}
	return query, nil
}

func readSQLFilters(query, whType string) (string, []CustomField, bool) {
	q := strings.TrimSpace(query)
	if !strings.HasPrefix(q, sqlFiltersHead+"(") || !strings.HasSuffix(q, sqlFiltersTail) {
		return "", nil, false
	}
	rest := q[len(sqlFiltersHead) : len(q)-len(sqlFiltersTail)]
	var filters []CustomField
	for {
		f, n, ok := nextSQLFilter(rest, whType)
		if !ok {
			return "", nil, false
		}
		filters = append(filters, f)
		rest = rest[n:]
		if after, ok := strings.CutPrefix(rest, ", "); ok {
			rest = after
			continue
		}
		if inner, ok := strings.CutPrefix(rest, " FROM (\n"); ok {
			return inner, filters, true
		}
		return "", nil, false
	}
}

// nextSQLFilter reads "(<sql>) AS <column>" at the start of s. An alias inside a
// string or comment of the SQL is not its end.
func nextSQLFilter(s, whType string) (CustomField, int, bool) {
	if !strings.HasPrefix(s, "(") {
		return CustomField{}, 0, false
	}
	for _, m := range reSQLFilterAlias.FindAllStringSubmatchIndex(s, -1) {
		sql, col := strings.TrimSpace(s[1:m[0]]), s[m[2]:m[3]]
		if !namedAfter(col, sql) {
			continue
		}
		if scan, err := scanSQL(sql, whType); err == nil && !scan.unbalanced {
			return CustomField{Key: col, Formula: sql}, m[1], true
		}
	}
	return CustomField{}, 0, false
}
