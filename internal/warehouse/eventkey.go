package warehouse

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
)

// LaunchDarkly filters every warehouse metric on key_column = eventKey, but Statsig
// selects rows only by filter criteria. The constant event key bridges them: the data
// source SQL projects its own key as a literal key column, so the key filter matches
// every row and the metric's filters do the selecting.

const constantEventKeyBase = "ld_event_key"

// Redshift requires an alias on a FROM subquery.
const constantEventKeyAlias = "ld_src"

// ConstantEventKeyColumn returns the column name in the case the warehouse stores it.
// LaunchDarkly quotes column names, and Snowflake folds unquoted ones to upper case.
func ConstantEventKeyColumn(whType string) string {
	if whType == "snowflake" || whType == "" {
		return strings.ToUpper(constantEventKeyBase)
	}
	return constantEventKeyBase
}

// WarehouseTypeForIntegration maps an integration key back to its warehouse type.
func WarehouseTypeForIntegration(integrationKey string) string {
	for wt, key := range WarehouseTypes {
		if key == integrationKey {
			return wt
		}
	}
	return ""
}

var reStatsigMacro = regexp.MustCompile(`\{\s*statsig_[a-zA-Z0-9_]*\s*\}`)

type sqlScan struct {
	// significant holds offsets of non-blank characters outside comments and quoted bodies.
	significant []int
	semicolons  []int
	// code is the SQL with comments and string literals blanked; offsets match the input.
	code string
}

// Snowflake and Redshift read "..." as a quoted identifier, not a string.
func doubleQuotedIsString(whType string) bool {
	return whType == "bigquery" || whType == "databricks"
}

// scanSQL is a lexer, not a parser: it finds comments, literals, and top-level semicolons.
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
			// Backslash escapes apply only in string literals; "dir\" is a valid identifier.
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

// PrepareSourceSQL makes source SQL safe to nest as a subquery: it drops trailing
// terminators and rejects multiple statements and Statsig macros.
func PrepareSourceSQL(sql, whType string) (string, error) {
	scan, err := scanSQL(sql, whType)
	if err != nil {
		return "", fmt.Errorf("could not read source SQL: %w", err)
	}
	// Only macros in code count: in a comment one does nothing, and in a string it cannot work.
	if m := reStatsigMacro.FindAllString(scan.code, -1); len(m) > 0 {
		return "", fmt.Errorf("source SQL uses Statsig macros %v, which LaunchDarkly does not expand; replace them with literal dates or remove the date filter before migrating", uniqueStrings(m))
	}
	if len(scan.significant) == 0 {
		return "", fmt.Errorf("source SQL is empty")
	}
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
	return strings.TrimRight(sql[:cut], " \t\r\n"), nil
}

// WrapWithConstantEventKey nests the source SQL in a SELECT adding the constant column.
// ")" goes on its own line so a trailing "--" comment in the source cannot swallow it.
func WrapWithConstantEventKey(sourceSQL, eventKey, column, whType string) (string, error) {
	inner, err := PrepareSourceSQL(sourceSQL, whType)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT *, %s AS %s FROM (\n%s\n) AS %s", constantKeyLiteral(eventKey, whType), column, inner, constantEventKeyAlias), nil
}

// constantKeyLiteral is the projected value. Redshift types an uncast string literal in
// a subquery as "unknown", which outer GROUP BYs and comparisons can reject.
func constantKeyLiteral(eventKey, whType string) string {
	lit := "'" + strings.ReplaceAll(eventKey, "'", "''") + "'"
	if whType == "redshift" {
		return "CAST(" + lit + " AS VARCHAR(256))"
	}
	return lit
}

// ChooseEventKeyColumn returns base, or base_N if a column already has that name
// (case-insensitively). With no known columns it searches sqlText instead.
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

// ApplyConstantEventKey wraps a MapMetricSourceToDataSource body's SQL and points its
// key column at the constant. existingColumns may be nil. Returns the column name.
func ApplyConstantEventKey(body map[string]any, whType string, existingColumns []string) (string, error) {
	key := j.GetStr(body, "key")
	// No sqlQuery means Statsig gave neither SQL nor a table; tableName is then a display name.
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

// PinConstantKeyColumn sets keyColumn to the constant column, in the preview's case.
func PinConstantKeyColumn(cm map[string]any, column string, realColumns []map[string]any) {
	cm["keyColumn"] = column
	for _, c := range realColumns {
		if name, _ := c["name"].(string); strings.EqualFold(name, column) {
			cm["keyColumn"] = name
			return
		}
	}
}

// reConstantKeyHead matches the wrapper's head, tolerating edits the LaunchDarkly UI or
// an editor may make (CRLF, re-indentation, keyword case, a quoted alias).
var reConstantKeyHead = regexp.MustCompile(`(?i)^SELECT\s+\*\s*,\s*` + reConstantKeyLiteral + `\s+AS\s+` + reQuote + `([A-Za-z0-9_]+)` + reQuote + `\s+FROM\s*\(`)

// reConstantKeyLiteral matches the projected value, plain or cast as on Redshift.
const reConstantKeyLiteral = `(?:'((?:[^']|'')*)'|CAST\s*\(\s*'((?:[^']|'')*)'\s+AS\s+VARCHAR\s*(?:\(\s*\d+\s*\))?\s*\))`

const reQuote = "[\"`]?"

// headParts returns the literal and column of a head match on s.
func headParts(s string, m []int) (literal, column string) {
	lit := m[2:4]
	if lit[0] < 0 {
		lit = m[4:6]
	}
	return strings.ReplaceAll(s[lit[0]:lit[1]], "''", "'"), s[m[6]:m[7]]
}

// reConstantKeyTail matches the alias and terminators after the closing ")".
var reConstantKeyTail = regexp.MustCompile("(?i)^\\s*AS\\s+([\"`]?)" + constantEventKeyAlias + "([\"`]?)[\\s;]*$")

// ConstantKeyWrapper is a query parsed as WrapWithConstantEventKey output.
type ConstantKeyWrapper struct {
	EventKey string
	Column   string
	Inner    string
}

// ParseConstantKeyWrapper reads a query as WrapWithConstantEventKey output. The "("
// after FROM must close at the final ")", ignoring ones in strings and comments, so a
// UNION of literal-tagged subqueries is not read as one constant.
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
	// The head's "(" must be structural, not inside a string the regex misread.
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
	literal, column := headParts(sqlQuery[start:], head)
	return ConstantKeyWrapper{
		EventKey: literal,
		Column:   column,
		Inner:    strings.TrimSpace(sqlQuery[open+1 : closing]),
	}, true
}

// ParseConstantEventKey returns the constant a wrapped query projects, when the key
// column is the projected column.
func ParseConstantEventKey(sqlQuery, keyColumn, whType string) (string, bool) {
	w, ok := ParseConstantKeyWrapper(sqlQuery, whType)
	if !ok || !strings.EqualFold(w.Column, strings.TrimSpace(keyColumn)) {
		return "", false
	}
	return w.EventKey, true
}

// ConstantKeyState is how a data source's query relates to the wrapper.
type ConstantKeyState int

const (
	NoConstantKey ConstantKeyState = iota
	ConstantKeyWrapped
	// ConstantKeyEdited means the wrapper's head survives but the rest was edited (e.g. a WHERE).
	ConstantKeyEdited
)

// reConstantKeyHeadLoose is reConstantKeyHead with the alias's AS optional.
var reConstantKeyHeadLoose = regexp.MustCompile(`(?i)^SELECT\s+\*\s*,\s*` + reConstantKeyLiteral + `\s*(?:AS\s+)?` + reQuote + `([A-Za-z0-9_]+)` + reQuote + `\s+FROM\s*\(`)

// ClassifyConstantKey reads a query and key column against the wrapper. A query that
// fails the strict match but opens with the wrapper's head for dsKey is
// ConstantKeyEdited: every row it returns still carries the constant.
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
	literal, column := headParts(sqlQuery[start:], head)
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
