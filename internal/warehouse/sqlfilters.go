package warehouse

import (
	"fmt"
	"regexp"
	"slices"
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

var reSQLFilterAlias = regexp.MustCompile(`\) AS (sql_filter_[0-9a-f]{8})`)

// SourceColumns are the columns this command adds to a source's query: its custom
// fields, then a true/false column for each Statsig SQL filter its metrics use.
type SourceColumns struct {
	Fields  []CustomField
	Filters []CustomField
}

func (c SourceColumns) Empty() bool { return len(c.Fields) == 0 && len(c.Filters) == 0 }

// All lists the custom fields, then the SQL filters.
func (c SourceColumns) All() []CustomField { return append(slices.Clone(c.Fields), c.Filters...) }

// SQLFilterColumns gives each SQL filter its column. Formula holds the filter's SQL.
func SQLFilterColumns(sqls []string) []CustomField {
	out := make([]CustomField, 0, len(sqls))
	for _, sql := range sqls {
		out = append(out, CustomField{Key: statsig.SQLFilterColumn(sql), Formula: strings.TrimSpace(sql)})
	}
	return out
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
	if lineComment {
		return "(\n" + sql + "\n)", nil
	}
	return "(" + sql + ")", nil
}

// MissingColumns compares query, the inner query of a data source this command
// wrapped, with the one BuildSourceQuery builds from sql and c. ok is false unless
// query is sql with some of those columns: edited since, or built from other SQL or
// other custom fields. merged is what an update writes: c, plus the SQL filter
// columns query has and c lacks, which a bound metric may still filter on.
func MissingColumns(query, sql string, c SourceColumns, whType string) (missing, merged SourceColumns, ok bool) {
	base, have := splitSQLFilters(query)
	prepared, err := PrepareSourceSQL(sql, whType)
	if err != nil {
		return SourceColumns{}, SourceColumns{}, false
	}
	switch {
	case SameSQL(base, prepared):
		missing.Fields = c.Fields
	case len(c.Fields) > 0:
		withFields, err := WithCustomFields(sql, c.Fields, whType)
		if err != nil || !SameSQL(base, withFields) {
			return SourceColumns{}, SourceColumns{}, false
		}
	default:
		return SourceColumns{}, SourceColumns{}, false
	}
	merged.Fields = c.Fields
	merged.Filters = append(merged.Filters, have...)
	for _, f := range c.Filters {
		if !slices.ContainsFunc(have, func(h CustomField) bool { return h.Key == f.Key }) {
			missing.Filters = append(missing.Filters, f)
			merged.Filters = append(merged.Filters, f)
		}
	}
	return missing, merged, true
}

// splitSQLFilters undoes the SQL filter layer BuildSourceQuery adds. Each column's
// name must be the one its SQL hashes to, so an edited filter is not taken for one.
func splitSQLFilters(query string) (string, []CustomField) {
	q := strings.TrimSpace(query)
	if !strings.HasPrefix(q, sqlFiltersHead+"(") || !strings.HasSuffix(q, sqlFiltersTail) {
		return query, nil
	}
	rest := q[len(sqlFiltersHead) : len(q)-len(sqlFiltersTail)]
	var filters []CustomField
	for {
		f, n, ok := nextSQLFilter(rest)
		if !ok {
			return query, nil
		}
		filters = append(filters, f)
		rest = rest[n:]
		if after, ok := strings.CutPrefix(rest, ", "); ok {
			rest = after
			continue
		}
		if inner, ok := strings.CutPrefix(rest, " FROM (\n"); ok {
			return inner, filters
		}
		return query, nil
	}
}

// nextSQLFilter reads "(<sql>) AS <column>" at the start of s.
func nextSQLFilter(s string) (CustomField, int, bool) {
	if !strings.HasPrefix(s, "(") {
		return CustomField{}, 0, false
	}
	for _, m := range reSQLFilterAlias.FindAllStringSubmatchIndex(s, -1) {
		sql, col := strings.TrimSpace(s[1:m[0]]), s[m[2]:m[3]]
		if statsig.SQLFilterColumn(sql) == col {
			return CustomField{Key: col, Formula: sql}, m[1], true
		}
	}
	return CustomField{}, 0, false
}
