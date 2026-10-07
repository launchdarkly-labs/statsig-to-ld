package warehouse

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
)

// Statsig metric sources have no event-key concept: a metric selects its rows
// with filter criteria, not by matching an event name. LaunchDarkly requires a
// key column on every warehouse data source and filters each metric on
// key_column = metric.eventKey. The constant event key bridges the two: the
// data source SQL projects one literal column whose value is the data source
// key, and every migrated metric uses that key as its eventKey, so the key
// filter matches every row and the metric's own filters do the selecting.

// constantEventKeyBase is the projected column name. Prefixed so it is unlikely
// to collide with a customer column; collisions are still checked.
const constantEventKeyBase = "ld_event_key"

// constantEventKeyAlias is the derived-table alias for the wrapped source SQL.
// Redshift requires an alias on a FROM subquery; the others accept one.
const constantEventKeyAlias = "ld_src"

// ConstantEventKeyColumn returns the column name the wrapper projects, in the
// case the warehouse stores an unquoted alias in. LaunchDarkly references data
// source columns as quoted identifiers, so the case must match exactly:
// Snowflake folds unquoted identifiers to upper case; Redshift folds them to
// lower case; BigQuery and Databricks match column names case-insensitively.
func ConstantEventKeyColumn(whType string) string {
	if whType == "snowflake" || whType == "" {
		return strings.ToUpper(constantEventKeyBase)
	}
	return constantEventKeyBase
}

// WarehouseTypeForIntegration maps an experimentation integration key back to
// its warehouse type.
func WarehouseTypeForIntegration(integrationKey string) string {
	for wt, key := range WarehouseTypes {
		if key == integrationKey {
			return wt
		}
	}
	return ""
}

var reStatsigMacro = regexp.MustCompile(`\{\s*statsig_[a-zA-Z0-9_]*\s*\}`)

// sqlScan is the result of a lexical pass over a SQL string.
type sqlScan struct {
	// significant holds the byte offsets of every character outside strings,
	// quoted identifiers, and comments that is not whitespace.
	significant []int
	// semicolons holds the offsets of top-level semicolons.
	semicolons []int
	// code is the SQL with comments and string literals blanked to spaces, so
	// text inside them is not mistaken for SQL. Offsets match the input.
	code string
}

// doubleQuotedIsString reports whether the warehouse reads "..." as a string
// literal. Snowflake and Redshift read it as a quoted identifier.
func doubleQuotedIsString(whType string) bool {
	return whType == "bigquery" || whType == "databricks"
}

// scanSQL walks the SQL once, tracking string literals, quoted identifiers,
// dollar-quoted strings, and comments, so that semicolons and comment markers
// inside them are not mistaken for structure. It is a lexer, not a parser: it
// only needs to find where the statement really ends.
func scanSQL(sql, whType string) (sqlScan, error) {
	var s sqlScan
	code := []byte(sql)
	blank := func(from, to int) {
		for k := from; k <= to && k < len(code); k++ {
			if code[k] != '\n' {
				code[k] = ' '
			}
		}
	}
	backslashEscapes := whType != "redshift"
	n := len(sql)
	for i := 0; i < n; i++ {
		c := sql[i]
		next := byte(0)
		if i+1 < n {
			next = sql[i+1]
		}
		switch {
		case c == '-' && next == '-',
			c == '/' && next == '/' && whType == "snowflake",
			c == '#' && whType == "bigquery":
			start := i
			for i < n && sql[i] != '\n' {
				i++
			}
			blank(start, i-1)
		case c == '/' && next == '*':
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return s, fmt.Errorf("unterminated /* comment")
			}
			blank(i, i+2+end+1)
			i += 2 + end + 1
		case c == '\'' || c == '"' || c == '`':
			// Backslash escapes apply only inside string literals. A quoted
			// identifier ends at the first undoubled quote, so "dir\" is valid.
			isString := c == '\'' || (c == '"' && doubleQuotedIsString(whType))
			s.significant = append(s.significant, i)
			end, err := skipQuoted(sql, i, c, backslashEscapes && isString)
			if err != nil {
				return s, err
			}
			if isString {
				blank(i+1, end-1)
			}
			s.significant = append(s.significant, end)
			i = end
		case c == '$' && next == '$':
			end := strings.Index(sql[i+2:], "$$")
			if end < 0 {
				return s, fmt.Errorf("unterminated $$ string")
			}
			blank(i+2, i+2+end-1)
			s.significant = append(s.significant, i)
			i += 2 + end + 1
			s.significant = append(s.significant, i)
		case c == ';':
			s.semicolons = append(s.semicolons, i)
			s.significant = append(s.significant, i)
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
		default:
			s.significant = append(s.significant, i)
		}
	}
	s.code = string(code)
	return s, nil
}

// skipQuoted returns the offset of the closing quote for the literal or quoted
// identifier opening at start. A doubled quote is an escaped quote.
func skipQuoted(sql string, start int, quote byte, backslash bool) (int, error) {
	for i := start + 1; i < len(sql); i++ {
		switch sql[i] {
		case '\\':
			if backslash {
				i++
			}
		case quote:
			if i+1 < len(sql) && sql[i+1] == quote {
				i++
				continue
			}
			return i, nil
		}
	}
	return 0, fmt.Errorf("unterminated %c quote", quote)
}

// PrepareSourceSQL makes a Statsig source query safe to nest as a subquery:
// it drops trailing statement terminators (including ones followed by
// comments), and rejects SQL that LaunchDarkly cannot run as a single SELECT —
// multiple statements, or Statsig date macros LaunchDarkly does not expand.
func PrepareSourceSQL(sql, whType string) (string, error) {
	scan, err := scanSQL(sql, whType)
	if err != nil {
		return "", fmt.Errorf("could not read source SQL: %w", err)
	}
	// Only macros in the SQL itself matter. One in a comment does nothing, and
	// Statsig expands a macro to a DATE() call, so one inside a string literal
	// cannot be part of a working query.
	if m := reStatsigMacro.FindAllString(scan.code, -1); len(m) > 0 {
		return "", fmt.Errorf("source SQL uses Statsig macros %v, which LaunchDarkly does not expand; replace them with literal dates or remove the date filter before migrating", uniqueStrings(m))
	}
	if len(scan.significant) == 0 {
		return "", fmt.Errorf("source SQL is empty")
	}
	// Strip terminators from the end: the last significant characters may be
	// one or more semicolons, possibly separated by comments.
	sig := scan.significant
	cut := len(sql)
	for len(sig) > 0 && sql[sig[len(sig)-1]] == ';' {
		cut = sig[len(sig)-1]
		sig = sig[:len(sig)-1]
	}
	for _, pos := range scan.semicolons {
		if pos < cut {
			return "", fmt.Errorf("source SQL contains more than one statement; a LaunchDarkly data source must be a single SELECT")
		}
	}
	if len(sig) == 0 {
		return "", fmt.Errorf("source SQL is empty")
	}
	// Keep any comment that sat between the last token and the terminator, but
	// drop the terminator and everything after it.
	return strings.TrimRight(sql[:cut], " \t\r\n"), nil
}

// WrapWithConstantEventKey nests the source SQL in a SELECT that adds the
// constant event-key column. The closing parenthesis goes on its own line so a
// trailing "--" comment in the source cannot swallow it.
func WrapWithConstantEventKey(sourceSQL, eventKey, column, whType string) (string, error) {
	inner, err := PrepareSourceSQL(sourceSQL, whType)
	if err != nil {
		return "", err
	}
	literal := "'" + strings.ReplaceAll(eventKey, "'", "''") + "'"
	return fmt.Sprintf("SELECT *, %s AS %s FROM (\n%s\n) AS %s", literal, column, inner, constantEventKeyAlias), nil
}

// ChooseEventKeyColumn returns base unless a source column already has that
// name (compared case-insensitively, since warehouses fold case), in which case
// it appends the first free numeric suffix. sqlText, when the real columns are
// unknown, is searched for the name as a fallback.
func ChooseEventKeyColumn(base string, existing []string, sqlText string) string {
	taken := map[string]bool{}
	for _, c := range existing {
		taken[strings.ToLower(c)] = true
	}
	lowerSQL := strings.ToLower(sqlText)
	inUse := func(name string) bool {
		if taken[strings.ToLower(name)] {
			return true
		}
		if len(existing) == 0 {
			return regexp.MustCompile(`\b` + regexp.QuoteMeta(strings.ToLower(name)) + `\b`).MatchString(lowerSQL)
		}
		return false
	}
	name := base
	for i := 2; inUse(name); i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	return name
}

// ApplyConstantEventKey rewrites a data source body built by
// MapMetricSourceToDataSource so its SQL projects the constant event key and
// its key column points at it. existingColumns are the source's real columns
// from a preview, used to avoid a name collision; pass nil when unknown.
// Returns the column name used.
func ApplyConstantEventKey(body map[string]any, whType string, existingColumns []string) (string, error) {
	key := j.GetStr(body, "key")
	// MapMetricSourceToDataSource turns a Statsig table into SELECT * FROM it,
	// so no sqlQuery means Statsig gave neither SQL nor a table. Its tableName
	// is then only the source's display name, which is not a table.
	sqlQuery := j.GetStr(body, "sqlQuery")
	if strings.TrimSpace(sqlQuery) == "" {
		return "", fmt.Errorf("data source %q: the Statsig source has neither SQL nor a table name, so there is no query to wrap", key)
	}
	column := ChooseEventKeyColumn(ConstantEventKeyColumn(whType), existingColumns, sqlQuery)
	wrapped, err := WrapWithConstantEventKey(sqlQuery, key, column, whType)
	if err != nil {
		return "", fmt.Errorf("data source %q: %w", key, err)
	}
	delete(body, "tableName")
	body["sqlQuery"] = wrapped

	cm, _ := body["columnMappings"].(map[string]any)
	if cm == nil {
		cm = map[string]any{}
		body["columnMappings"] = cm
	}
	cm["keyColumn"] = column
	if cols, ok := cm["columns"].([]FallbackColumn); ok {
		cm["columns"] = append(cols, FallbackColumn{Name: column, Type: "TEXT"})
	}
	return column, nil
}

// PinConstantKeyColumn points keyColumn at the constant column after preview
// reconciliation, using the column's real case from the preview. The preview's
// own key-column guess must never win over the constant column.
func PinConstantKeyColumn(cm map[string]any, column string, realColumns []map[string]any) {
	cm["keyColumn"] = column
	for _, c := range realColumns {
		if name, _ := c["name"].(string); strings.EqualFold(name, column) {
			cm["keyColumn"] = name
			return
		}
	}
}

// reConstantKeyHead matches the head of WrapWithConstantEventKey's output, from
// its first significant character. It tolerates the changes an editor or the
// LaunchDarkly UI can make to saved SQL (CRLF line endings, re-indentation,
// keyword case, a quoted alias), so a source that still projects the constant
// is not mistaken for one that does not.
var reConstantKeyHead = regexp.MustCompile("(?i)^SELECT\\s+\\*\\s*,\\s*'((?:[^']|'')*)'\\s+AS\\s+[\"`]?([A-Za-z0-9_]+)[\"`]?\\s+FROM\\s*\\(")

// reConstantKeyTail matches what may follow the wrapper's closing parenthesis,
// with comments already blanked: the derived-table alias and terminators.
var reConstantKeyTail = regexp.MustCompile("(?i)^\\s*AS\\s+([\"`]?)" + constantEventKeyAlias + "([\"`]?)[\\s;]*$")

// ConstantKeyWrapper is a data source query read back as the output of
// WrapWithConstantEventKey.
type ConstantKeyWrapper struct {
	// EventKey is the projected literal.
	EventKey string
	// Column is the projected column's name as written in the SQL.
	Column string
	// Inner is the wrapped source SQL, trimmed.
	Inner string
}

// ParseConstantKeyWrapper reads a data source query as the output of
// WrapWithConstantEventKey. It accepts the query only when, after leading
// whitespace and comments, the whole statement is
// SELECT *, '<literal>' AS <column> FROM ( <inner> ) AS ld_src, followed by
// nothing but whitespace, comments, or semicolons, and the "(" after FROM is
// closed by the final ")". Parentheses inside strings, quoted identifiers, and
// comments do not count. So a UNION of literal-tagged subqueries, which starts
// the same way, is not read as one constant.
func ParseConstantKeyWrapper(sqlQuery, whType string) (ConstantKeyWrapper, bool) {
	scan, err := scanSQL(sqlQuery, whType)
	if err != nil || len(scan.significant) == 0 {
		return ConstantKeyWrapper{}, false
	}
	start := scan.significant[0]
	head := reConstantKeyHead.FindStringSubmatchIndex(sqlQuery[start:])
	if head == nil {
		return ConstantKeyWrapper{}, false
	}
	// The "(" closing the head must be structural, not inside a string the
	// head regex and the lexer read differently.
	open := start + head[1] - 1
	idx, found := slices.BinarySearch(scan.significant, open)
	if !found {
		return ConstantKeyWrapper{}, false
	}
	closing := -1
	depth := 0
	for _, pos := range scan.significant[idx:] {
		switch sqlQuery[pos] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 {
			closing = pos
			break
		}
	}
	if closing < 0 {
		return ConstantKeyWrapper{}, false
	}
	tail := reConstantKeyTail.FindStringSubmatch(scan.code[closing+1:])
	if tail == nil || tail[1] != tail[2] {
		return ConstantKeyWrapper{}, false
	}
	return ConstantKeyWrapper{
		EventKey: strings.ReplaceAll(sqlQuery[start+head[2]:start+head[3]], "''", "'"),
		Column:   sqlQuery[start+head[4] : start+head[5]],
		Inner:    strings.TrimSpace(sqlQuery[open+1 : closing]),
	}, true
}

// ParseConstantEventKey reports the event key a data source's SQL projects as a
// constant, when the SQL was produced by WrapWithConstantEventKey (see
// ParseConstantKeyWrapper) and the data source's key column is that projected
// column. whType selects the warehouse's comment and quoting rules.
func ParseConstantEventKey(sqlQuery, keyColumn, whType string) (string, bool) {
	w, ok := ParseConstantKeyWrapper(sqlQuery, whType)
	if !ok || !strings.EqualFold(w.Column, strings.TrimSpace(keyColumn)) {
		return "", false
	}
	return w.EventKey, true
}

// ConstantKeyState is how a data source's query relates to the constant
// event-key wrapper.
type ConstantKeyState int

const (
	// NoConstantKey means the data source does not project the constant.
	NoConstantKey ConstantKeyState = iota
	// ConstantKeyWrapped means the whole query is the wrapper as
	// WrapWithConstantEventKey writes it (see ParseConstantKeyWrapper).
	ConstantKeyWrapped
	// ConstantKeyEdited means the query still opens with the wrapper's head for
	// the data source's own key, but what follows was changed after creation,
	// for example an appended WHERE or LIMIT.
	ConstantKeyEdited
)

// reConstantKeyHeadLoose is reConstantKeyHead with the column alias's AS
// optional.
var reConstantKeyHeadLoose = regexp.MustCompile("(?i)^SELECT\\s+\\*\\s*,\\s*'((?:[^']|'')*)'\\s*(?:AS\\s+)?[\"`]?([A-Za-z0-9_]+)[\"`]?\\s+FROM\\s*\\(")

// ClassifyConstantKey reads a data source's query and key column against the
// constant event-key wrapper. dsKey is the data source's key.
//
// ConstantKeyWrapped is the strict whole-statement match. Failing that, the
// query is ConstantKeyEdited when, after leading whitespace and comments, it
// opens with SELECT *, '<literal>' AS <column> FROM ( where the literal is
// dsKey, the column is the key column (case-insensitively), and the "(" is
// structural. Every row such a query returns still carries the constant, so
// metrics on it can use dsKey as their event key. A query that opens the same
// way with another literal, such as a UNION of literal-tagged subqueries, is
// NoConstantKey.
func ClassifyConstantKey(sqlQuery, keyColumn, dsKey, whType string) (ConstantKeyWrapper, ConstantKeyState) {
	keyColumn = strings.TrimSpace(keyColumn)
	if keyColumn == "" {
		return ConstantKeyWrapper{}, NoConstantKey
	}
	if w, ok := ParseConstantKeyWrapper(sqlQuery, whType); ok && strings.EqualFold(w.Column, keyColumn) {
		return w, ConstantKeyWrapped
	}
	scan, err := scanSQL(sqlQuery, whType)
	if err != nil || len(scan.significant) == 0 {
		return ConstantKeyWrapper{}, NoConstantKey
	}
	start := scan.significant[0]
	head := reConstantKeyHeadLoose.FindStringSubmatchIndex(sqlQuery[start:])
	if head == nil {
		return ConstantKeyWrapper{}, NoConstantKey
	}
	if _, found := slices.BinarySearch(scan.significant, start+head[1]-1); !found {
		return ConstantKeyWrapper{}, NoConstantKey
	}
	literal := strings.ReplaceAll(sqlQuery[start+head[2]:start+head[3]], "''", "'")
	column := sqlQuery[start+head[4] : start+head[5]]
	if literal != dsKey || !strings.EqualFold(column, keyColumn) {
		return ConstantKeyWrapper{}, NoConstantKey
	}
	return ConstantKeyWrapper{EventKey: literal, Column: column}, ConstantKeyEdited
}

// SameSQL reports whether two queries are equal ignoring whitespace runs.
func SameSQL(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
