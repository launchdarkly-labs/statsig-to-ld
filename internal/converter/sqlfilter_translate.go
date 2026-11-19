package converter

import (
	"strconv"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// translateSQLFilter rewrites a Statsig SQL filter as LaunchDarkly filter clauses to
// AND, when its meaning carries over exactly and LaunchDarkly's filter editor can show
// them: simple column checks joined by AND, OR, and NOT, where an OR holds only single
// checks. Anything else, such as functions or null checks, keeps its column. Each
// filter returned is a clause or an OR group of clauses.
func translateSQLFilter(sql string) ([]launchdarkly.EventFilter, bool) {
	toks, ok := lexSQLFilter(sql)
	if !ok || len(toks) == 0 {
		return nil, false
	}
	p := &sqlFilterParser{toks: toks}
	n, ok := p.disjunction()
	if !ok || p.pos != len(toks) {
		return nil, false
	}
	return n.filters()
}

// SQLFilterFitsColumns reports whether sql becomes clauses whose columns have types
// that fit, by lowercase column name in types.
func SQLFilterFitsColumns(sql string, types map[string]string) bool {
	clauses, ok := translateSQLFilter(sql)
	return ok && clausesFitTypes(clauses, types)
}

// sqlFilterTranslations returns, by criterion index, the clauses each of a term's
// SQL filters becomes, when their columns' types fit (types, by lowercase name; nil
// when unknown). It returns none when the term would then hold more clauses than
// LaunchDarkly allows, so each SQL filter keeps its column.
func sqlFilterTranslations(criteria []statsig.Criterion, types map[string]string) map[int][]launchdarkly.EventFilter {
	out := map[int][]launchdarkly.EventFilter{}
	leaves := len(criteria)
	for i, c := range criteria {
		sql, ok := c.SQLFilter()
		if !ok {
			continue
		}
		if clauses, ok := translateSQLFilter(sql); ok && (types == nil || clausesFitTypes(clauses, types)) {
			out[i] = clauses
			leaves--
			for _, f := range clauses {
				leaves += leafCount(f)
			}
		}
	}
	if leaves > ldFilterMaxLeaves {
		return nil
	}
	return out
}

// clausesFitTypes reports whether each clause's column, by lowercase name in types,
// holds the kind of value the clause compares. LaunchDarkly compares a column's text,
// which matches SQL's comparison only then: SQL also casts 1 or 'TRUE' to true, and
// '10' to a decimal column's 10.00.
func clausesFitTypes(clauses []launchdarkly.EventFilter, types map[string]string) bool {
	for _, c := range clauses {
		if c.Type == launchdarkly.EventFilterTypeGroup {
			for _, v := range c.Values {
				if child, ok := v.(launchdarkly.EventFilter); !ok || !clausesFitTypes([]launchdarkly.EventFilter{child}, types) {
					return false
				}
			}
			continue
		}
		t, ok := types[strings.ToLower(c.Attribute)]
		if !ok || !clauseFitsType(c, t) {
			return false
		}
	}
	return true
}

// clauseFitsType reads colType as LaunchDarkly's preview stores it: the warehouse
// driver's type name, which for Snowflake's NUMBER is FIXED.
func clauseFitsType(c launchdarkly.EventFilter, colType string) bool {
	t := strings.ToUpper(strings.TrimSpace(colType))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	switch c.Op {
	case ldOpLessThan, ldOpLessThanOrEqual, ldOpGreaterThan, ldOpGreaterThanOrEqual:
		return numericColumnTypes[t]
	case ldOpIn:
		if _, ok := c.Values[0].(bool); ok {
			return t == "BOOLEAN" || t == "BOOL"
		}
	}
	return !strings.ContainsAny(t, "<[") && (strings.Contains(t, "CHAR") || strings.Contains(t, "TEXT") || strings.Contains(t, "STRING"))
}

var numericColumnTypes = map[string]bool{
	"NUMBER": true, "FIXED": true, "DECIMAL": true, "DEC": true, "NUMERIC": true, "INT": true, "INTEGER": true,
	"BIGINT": true, "SMALLINT": true, "TINYINT": true, "BYTEINT": true, "FLOAT": true, "FLOAT4": true,
	"FLOAT8": true, "DOUBLE": true, "DOUBLE PRECISION": true, "REAL": true, "DECFLOAT": true,
}

// leafCount counts f's clauses, as LaunchDarkly's limit does.
func leafCount(f launchdarkly.EventFilter) int {
	if f.Type == launchdarkly.EventFilterTypeGroup {
		return len(f.Values)
	}
	return 1
}

type sqlTokenKind int

const (
	sqlIdent sqlTokenKind = iota
	sqlString
	sqlNumber
	sqlSymbol
)

type sqlToken struct {
	kind sqlTokenKind
	text string
}

// lexSQLFilter splits the SQL into identifiers, single-quoted strings, plain
// decimal numbers, and comparison symbols. Anything else fails it, including
// comments, quoted identifiers, backslashes, and Statsig macros.
func lexSQLFilter(sql string) ([]sqlToken, bool) {
	var toks []sqlToken
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case isSQLIdentStart(c):
			j := i + 1
			for j < len(sql) && (isSQLIdentStart(sql[j]) || isSQLDigit(sql[j]) || sql[j] == '$') {
				j++
			}
			toks = append(toks, sqlToken{sqlIdent, sql[i:j]})
			i = j
		case isSQLDigit(c):
			j := i
			for j < len(sql) && isSQLDigit(sql[j]) {
				j++
			}
			if j+1 < len(sql) && sql[j] == '.' && isSQLDigit(sql[j+1]) {
				j++
				for j < len(sql) && isSQLDigit(sql[j]) {
					j++
				}
			}
			if j < len(sql) && (isSQLIdentStart(sql[j]) || sql[j] == '.' || sql[j] == '$') {
				return nil, false
			}
			toks = append(toks, sqlToken{sqlNumber, sql[i:j]})
			i = j
		case c == '\'':
			var b strings.Builder
			j := i + 1
			for {
				if j >= len(sql) || sql[j] == '\\' {
					return nil, false
				}
				if sql[j] == '\'' {
					if j+1 < len(sql) && sql[j+1] == '\'' {
						b.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				b.WriteByte(sql[j])
				j++
			}
			toks = append(toks, sqlToken{sqlString, b.String()})
			i = j + 1
		default:
			sym := ""
			for _, s := range []string{"<=", ">=", "<>", "!=", "=", "<", ">", "(", ")", ",", "-"} {
				if strings.HasPrefix(sql[i:], s) {
					sym = s
					break
				}
			}
			if sym == "" {
				return nil, false
			}
			toks = append(toks, sqlToken{sqlSymbol, sym})
			i += len(sym)
		}
	}
	return toks, true
}

func isSQLIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isSQLDigit(c byte) bool { return c >= '0' && c <= '9' }

// sqlKeywords can never be a column name in a translated filter.
var sqlKeywords = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "IS": true, "NULL": true, "TRUE": true, "FALSE": true,
	"IN": true, "LIKE": true, "ILIKE": true, "RLIKE": true, "REGEXP": true, "BETWEEN": true,
	"ESCAPE": true, "CASE": true, "WHEN": true, "THEN": true, "ELSE": true, "END": true,
	"EXISTS": true, "SELECT": true, "FROM": true, "WHERE": true, "ANY": true, "ALL": true, "SOME": true,
}

// sqlClause is one translated clause. A clause from IS TRUE or IS FALSE cannot be
// negated: NOT (x IS TRUE) also matches null, which no clause expresses.
type sqlClause struct {
	filter    launchdarkly.EventFilter
	negatable bool
}

// sqlNode is a parsed filter: a clause when op is empty, else an AND or OR of nodes.
type sqlNode struct {
	op       string
	clause   sqlClause
	children []sqlNode
}

// sqlGroup joins nodes with op, taking in the children of those that use op too.
func sqlGroup(op string, nodes ...sqlNode) sqlNode {
	g := sqlNode{op: op}
	for _, n := range nodes {
		if n.op == op {
			g.children = append(g.children, n.children...)
		} else {
			g.children = append(g.children, n)
		}
	}
	if len(g.children) == 1 {
		return g.children[0]
	}
	return g
}

// negate returns the node matching exactly the rows where SQL's NOT of n is true. De
// Morgan's laws hold in SQL's three-valued logic, so NOT moves onto each clause.
func (n sqlNode) negate() (sqlNode, bool) {
	if n.op == "" {
		c, ok := negateSQLClause(n.clause)
		return sqlNode{clause: c}, ok
	}
	flipped := launchdarkly.EventFilterGroupOpAnd
	if n.op == flipped {
		flipped = launchdarkly.EventFilterGroupOpOr
	}
	children := make([]sqlNode, len(n.children))
	for i, c := range n.children {
		var ok bool
		if children[i], ok = c.negate(); !ok {
			return sqlNode{}, false
		}
	}
	return sqlGroup(flipped, children...), true
}

// filters lays n out as clauses and OR groups of clauses, to AND inside the term's
// group. Anything deeper would pass LaunchDarkly's limit of three levels.
func (n sqlNode) filters() ([]launchdarkly.EventFilter, bool) {
	items := []sqlNode{n}
	if n.op == launchdarkly.EventFilterGroupOpAnd {
		items = n.children
	}
	out := make([]launchdarkly.EventFilter, 0, len(items))
	for _, item := range items {
		if item.op == "" {
			out = append(out, item.clause.filter)
			continue
		}
		values := make([]any, 0, len(item.children))
		for _, c := range item.children {
			if c.op != "" {
				return nil, false
			}
			values = append(values, c.clause.filter)
		}
		out = append(out, launchdarkly.EventFilter{Type: launchdarkly.EventFilterTypeGroup, Op: launchdarkly.EventFilterGroupOpOr, Values: values})
	}
	return out, true
}

type sqlFilterParser struct {
	toks []sqlToken
	pos  int
}

func (p *sqlFilterParser) peek() (sqlToken, bool) {
	if p.pos >= len(p.toks) {
		return sqlToken{}, false
	}
	return p.toks[p.pos], true
}

func (p *sqlFilterParser) keyword(word string) bool {
	t, ok := p.peek()
	if ok && t.kind == sqlIdent && strings.EqualFold(t.text, word) {
		p.pos++
		return true
	}
	return false
}

func (p *sqlFilterParser) symbol(s string) bool {
	t, ok := p.peek()
	if ok && t.kind == sqlSymbol && t.text == s {
		p.pos++
		return true
	}
	return false
}

// disjunction reads conjunctions joined by OR.
func (p *sqlFilterParser) disjunction() (sqlNode, bool) {
	var nodes []sqlNode
	for {
		n, ok := p.conjunction()
		if !ok {
			return sqlNode{}, false
		}
		nodes = append(nodes, n)
		if !p.keyword("OR") {
			return sqlGroup(launchdarkly.EventFilterGroupOpOr, nodes...), true
		}
	}
}

// conjunction reads checks joined by AND.
func (p *sqlFilterParser) conjunction() (sqlNode, bool) {
	var nodes []sqlNode
	for {
		n, ok := p.unary()
		if !ok {
			return sqlNode{}, false
		}
		nodes = append(nodes, n)
		if !p.keyword("AND") {
			return sqlGroup(launchdarkly.EventFilterGroupOpAnd, nodes...), true
		}
	}
}

func (p *sqlFilterParser) unary() (sqlNode, bool) {
	if p.keyword("NOT") {
		n, ok := p.unary()
		if !ok {
			return sqlNode{}, false
		}
		return n.negate()
	}
	if p.symbol("(") {
		n, ok := p.disjunction()
		if !ok || !p.symbol(")") {
			return sqlNode{}, false
		}
		return n, true
	}
	return p.predicate()
}

// predicate reads one column check: a bare boolean column, IS TRUE or IS FALSE,
// [NOT] IN, [NOT] LIKE, or a comparison with a literal.
func (p *sqlFilterParser) predicate() (sqlNode, bool) {
	t, ok := p.peek()
	if !ok || t.kind != sqlIdent || sqlKeywords[strings.ToUpper(t.text)] {
		return sqlNode{}, false
	}
	p.pos++
	col := t.text
	leaf := func(op string, negate bool, values ...any) sqlNode {
		return sqlNode{clause: sqlClause{filter: propertyFilter(col, op, negate, values...), negatable: true}}
	}
	clause := func(c sqlClause, ok bool) (sqlNode, bool) { return sqlNode{clause: c}, ok }

	next, ok := p.peek()
	if !ok || (next.kind == sqlSymbol && next.text == ")") ||
		(next.kind == sqlIdent && (strings.EqualFold(next.text, "AND") || strings.EqualFold(next.text, "OR"))) {
		return leaf(ldOpIn, false, true), true
	}
	switch {
	case p.keyword("IS"):
		for _, b := range []bool{true, false} {
			if p.keyword(strconv.FormatBool(b)) {
				n := leaf(ldOpIn, false, b)
				n.clause.negatable = false
				return n, true
			}
		}
		return sqlNode{}, false
	case p.keyword("NOT"):
		var c sqlClause
		switch {
		case p.keyword("IN"):
			c, ok = p.inList(col)
		case p.keyword("LIKE"):
			c, ok = p.like(col)
		default:
			return sqlNode{}, false
		}
		if !ok {
			return sqlNode{}, false
		}
		return clause(negateSQLClause(c))
	case p.keyword("IN"):
		return clause(p.inList(col))
	case p.keyword("LIKE"):
		return clause(p.like(col))
	case p.symbol("="), p.symbol("<>"), p.symbol("!="):
		negate := p.toks[p.pos-1].text != "="
		if s, ok := p.str(); ok {
			return leaf(ldOpIn, negate, s), true
		}
		for _, b := range []bool{true, false} {
			if p.keyword(strconv.FormatBool(b)) {
				return leaf(ldOpIn, false, b != negate), true
			}
		}
		n, ok := p.number()
		if !ok {
			return sqlNode{}, false
		}
		// "in" compares text, and a decimal column's text is 10.00, so equality is a range.
		eq := sqlGroup(launchdarkly.EventFilterGroupOpAnd, leaf(ldOpGreaterThanOrEqual, false, n), leaf(ldOpLessThanOrEqual, false, n))
		if negate {
			return eq.negate()
		}
		return eq, true
	}
	op, ok := sqlComparisons[next.text]
	if next.kind != sqlSymbol || !ok {
		return sqlNode{}, false
	}
	p.pos++
	n, ok := p.number()
	if !ok {
		return sqlNode{}, false
	}
	return leaf(op, false, n), true
}

var sqlComparisons = map[string]string{"<": ldOpLessThan, "<=": ldOpLessThanOrEqual, ">": ldOpGreaterThan, ">=": ldOpGreaterThanOrEqual}

// str reads a string literal LaunchDarkly can hold. An empty string is refused:
// some warehouses treat it as null.
func (p *sqlFilterParser) str() (string, bool) {
	t, ok := p.peek()
	if !ok || t.kind != sqlString || t.text == "" || len(t.text) > ldFilterMaxStringLen {
		return "", false
	}
	p.pos++
	return t.text, true
}

// number reads a number LaunchDarkly compares exactly: at most 14 decimal places
// and below 2^53, where float64 stops holding every whole number.
func (p *sqlFilterParser) number() (float64, bool) {
	sign := 1.0
	if p.symbol("-") {
		sign = -1
	}
	t, ok := p.peek()
	if !ok || t.kind != sqlNumber {
		return 0, false
	}
	if _, frac, found := strings.Cut(t.text, "."); found && len(frac) > 14 {
		return 0, false
	}
	n, err := strconv.ParseFloat(t.text, 64)
	if err != nil || n >= 1<<53 {
		return 0, false
	}
	p.pos++
	return sign * n, true
}

func (p *sqlFilterParser) inList(col string) (sqlClause, bool) {
	if !p.symbol("(") {
		return sqlClause{}, false
	}
	var values []any
	for {
		s, ok := p.str()
		if !ok {
			return sqlClause{}, false
		}
		values = append(values, s)
		if p.symbol(")") {
			return sqlClause{filter: propertyFilter(col, ldOpIn, false, values...), negatable: true}, true
		}
		if !p.symbol(",") {
			return sqlClause{}, false
		}
	}
}

// like reads a LIKE pattern that is a prefix, suffix, substring, or exact match.
func (p *sqlFilterParser) like(col string) (sqlClause, bool) {
	pattern, ok := p.str()
	if !ok || strings.Contains(pattern, "_") {
		return sqlClause{}, false
	}
	core, lead := strings.CutPrefix(pattern, "%")
	core, trail := strings.CutSuffix(core, "%")
	if core == "" || strings.Contains(core, "%") {
		return sqlClause{}, false
	}
	op := ldOpIn
	switch {
	case lead && trail:
		op = ldOpContains
	case lead:
		op = ldOpEndsWith
	case trail:
		op = ldOpStartsWith
	}
	return sqlClause{filter: propertyFilter(col, op, false, core), negatable: true}, true
}

// negateSQLClause returns the clause matching exactly the rows where SQL's NOT of
// it is true, which never includes null.
func negateSQLClause(c sqlClause) (sqlClause, bool) {
	if !c.negatable {
		return sqlClause{}, false
	}
	f := c.filter
	f.Values = append([]any(nil), f.Values...)
	switch f.Op {
	case ldOpLessThan:
		f.Op = ldOpGreaterThanOrEqual
	case ldOpLessThanOrEqual:
		f.Op = ldOpGreaterThan
	case ldOpGreaterThan:
		f.Op = ldOpLessThanOrEqual
	case ldOpGreaterThanOrEqual:
		f.Op = ldOpLessThan
	default:
		if b, ok := f.Values[0].(bool); ok && f.Op == ldOpIn && len(f.Values) == 1 {
			f.Values[0] = !b
		} else {
			f.Negate = !f.Negate
		}
	}
	return sqlClause{filter: f, negatable: true}, true
}

func propertyFilter(attr, op string, negate bool, values ...any) launchdarkly.EventFilter {
	return launchdarkly.EventFilter{Type: launchdarkly.EventFilterTypeEventProperty, Attribute: attr, Op: op, Negate: negate, Values: values}
}
