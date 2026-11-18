package statsig

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

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

// SQLFilterColumn names the data source column that holds a SQL filter's result.
// warehouse adds the column and metrics convert filters on it, so both derive the
// name from the SQL alone.
func SQLFilterColumn(sql string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sql)))
	return "sql_filter_" + hex.EncodeToString(sum[:4])
}
