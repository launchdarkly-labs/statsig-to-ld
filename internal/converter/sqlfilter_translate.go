package converter

import (
	"strconv"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
)

// translateSQLFilter rewrites a Statsig SQL filter as LaunchDarkly filter clauses,
// AND-ed, when it is an AND of simple column checks whose meaning carries over
// exactly and which LaunchDarkly's filter editor can show. Anything else, such as
// OR, functions, or null checks, is not translated and keeps its column.
func translateSQLFilter(sql string) ([]launchdarkly.EventFilter, bool) {
	toks, ok := lexSQLFilter(sql)
	if !ok || len(toks) == 0 {
		return nil, false
	}
	p := &sqlFilterParser{toks: toks}
	clauses, ok := p.conjunction()
	if !ok || p.pos != len(toks) {
		return nil, false
	}
	out := make([]launchdarkly.EventFilter, len(clauses))
	for i, c := range clauses {
		out[i] = c.filter
	}
	return out, true
}

// sqlFilterTranslations returns, by criterion index, the clauses each of a term's
// SQL filters becomes. It returns none when the term would then hold more clauses
// than LaunchDarkly allows, so each SQL filter keeps its column.
func sqlFilterTranslations(criteria []statsig.Criterion) map[int][]launchdarkly.EventFilter {
	out := map[int][]launchdarkly.EventFilter{}
	leaves := len(criteria)
	for i, c := range criteria {
		sql, ok := c.SQLFilter()
		if !ok {
			continue
		}
		if clauses, ok := translateSQLFilter(sql); ok {
			out[i] = clauses
			leaves += len(clauses) - 1
		}
	}
	if leaves > ldFilterMaxLeaves {
		return nil
	}
	return out
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

// conjunction reads clauses joined by AND.
func (p *sqlFilterParser) conjunction() ([]sqlClause, bool) {
	var out []sqlClause
	for {
		clauses, ok := p.unary()
		if !ok {
			return nil, false
		}
		out = append(out, clauses...)
		if !p.keyword("AND") {
			return out, true
		}
	}
}

func (p *sqlFilterParser) unary() ([]sqlClause, bool) {
	if p.keyword("NOT") {
		clauses, ok := p.unary()
		if !ok || len(clauses) != 1 {
			return nil, false
		}
		negated, ok := negateSQLClause(clauses[0])
		return []sqlClause{negated}, ok
	}
	if p.symbol("(") {
		clauses, ok := p.conjunction()
		if !ok || !p.symbol(")") {
			return nil, false
		}
		return clauses, true
	}
	c, ok := p.predicate()
	return []sqlClause{c}, ok
}

// predicate reads one column check: a bare boolean column, IS TRUE or IS FALSE,
// [NOT] IN, [NOT] LIKE, or a comparison with a literal.
func (p *sqlFilterParser) predicate() (sqlClause, bool) {
	t, ok := p.peek()
	if !ok || t.kind != sqlIdent || sqlKeywords[strings.ToUpper(t.text)] {
		return sqlClause{}, false
	}
	p.pos++
	col := t.text
	leaf := func(op string, negate bool, values ...any) (sqlClause, bool) {
		return sqlClause{filter: propertyFilter(col, op, negate, values...), negatable: true}, true
	}

	next, ok := p.peek()
	if !ok || (next.kind == sqlSymbol && next.text == ")") || (next.kind == sqlIdent && strings.EqualFold(next.text, "AND")) {
		return leaf(ldOpIn, false, true)
	}
	switch {
	case p.keyword("IS"):
		for _, b := range []bool{true, false} {
			if p.keyword(strconv.FormatBool(b)) {
				c, _ := leaf(ldOpIn, false, b)
				c.negatable = false
				return c, true
			}
		}
		return sqlClause{}, false
	case p.keyword("NOT"):
		var c sqlClause
		switch {
		case p.keyword("IN"):
			c, ok = p.inList(col)
		case p.keyword("LIKE"):
			c, ok = p.like(col)
		default:
			return sqlClause{}, false
		}
		if !ok {
			return sqlClause{}, false
		}
		return negateSQLClause(c)
	case p.keyword("IN"):
		return p.inList(col)
	case p.keyword("LIKE"):
		return p.like(col)
	case p.symbol("="), p.symbol("<>"), p.symbol("!="):
		negate := p.toks[p.pos-1].text != "="
		if s, ok := p.str(); ok {
			return leaf(ldOpIn, negate, s)
		}
		for _, b := range []bool{true, false} {
			if p.keyword(strconv.FormatBool(b)) {
				return leaf(ldOpIn, false, b != negate)
			}
		}
		return sqlClause{}, false
	}
	op, ok := sqlComparisons[next.text]
	if next.kind != sqlSymbol || !ok {
		return sqlClause{}, false
	}
	p.pos++
	n, ok := p.number()
	if !ok {
		return sqlClause{}, false
	}
	return leaf(op, false, n)
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
