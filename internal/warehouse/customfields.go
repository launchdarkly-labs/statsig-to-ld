package warehouse

import (
	"fmt"
	"regexp"
	"strings"

	j "github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
)

// Redshift requires an alias on a FROM subquery.
const customFieldsAlias = "statsig_src"

var reColumnName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// CustomField is a Statsig metric source's custom field: a column computed by a SQL formula.
type CustomField struct {
	Key     string
	Formula string
}

// CustomFields lists source's custom fields. Entries without a key are not Statsig's
// shape and are skipped.
func CustomFields(source map[string]any) []CustomField {
	var out []CustomField
	for _, raw := range j.GetSlice(source, "customFieldMapping") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if f := (CustomField{Key: j.GetStr(m, "key"), Formula: j.GetStr(m, "formula")}); f.Key != "" {
			out = append(out, f)
		}
	}
	return out
}

// WithCustomFields nests sql in a SELECT that adds each field as a column.
func WithCustomFields(sql string, fields []CustomField, whType string) (string, error) {
	cols, err := customFieldColumns(fields, whType)
	if err != nil {
		return "", err
	}
	inner, err := PrepareSourceSQL(sql, whType)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT *, %s FROM (\n%s\n) AS %s", strings.Join(cols, ", "), inner, customFieldsAlias), nil
}

// ValidateCustomFields reports the first field WithCustomFields would refuse.
func ValidateCustomFields(fields []CustomField, whType string) error {
	_, err := customFieldColumns(fields, whType)
	return err
}

// CollidingCustomFields names the fields whose column the query returns more than once,
// ignoring case.
func CollidingCustomFields(columns []string, fields []CustomField) []string {
	count := map[string]int{}
	for _, c := range columns {
		count[strings.ToLower(c)]++
	}
	var out []string
	for _, f := range fields {
		if count[strings.ToLower(f.Key)] > 1 {
			out = append(out, f.Key)
		}
	}
	return out
}

func customFieldColumns(fields []CustomField, whType string) ([]string, error) {
	cols := make([]string, len(fields))
	seen := map[string]bool{}
	for i, f := range fields {
		lower := strings.ToLower(f.Key)
		if seen[lower] {
			return nil, fmt.Errorf("Statsig source has more than one custom field named %q; rename one in Statsig, then rerun", f.Key)
		}
		seen[lower] = true
		expr, err := customFieldExpr(f, whType)
		if err != nil {
			return nil, err
		}
		cols[i] = expr + " AS " + f.Key
	}
	return cols, nil
}

func customFieldExpr(f CustomField, whType string) (string, error) {
	if !reColumnName.MatchString(f.Key) {
		return "", fmt.Errorf("Statsig custom field %q: a column name must be letters, digits, and underscores; rename the field in Statsig, then rerun", f.Key)
	}
	formula, lineComment, err := selectExpression(fmt.Sprintf("Statsig custom field %q", f.Key), "formula", f.Formula, whType)
	if err != nil {
		return "", err
	}
	if lineComment {
		return "(\n" + formula + "\n)", nil
	}
	return formula, nil
}

// selectExpression checks that text can sit in a SELECT list, naming it label in
// errors. lineComment reports a "--" comment, which must not run into what follows.
func selectExpression(label, noun, text, whType string) (expr string, lineComment bool, err error) {
	expr = strings.TrimSpace(text)
	scan, err := scanSQL(expr, whType)
	if err != nil {
		return "", false, fmt.Errorf("%s: could not read its %s: %w", label, noun, err)
	}
	if len(scan.significant) == 0 {
		return "", false, fmt.Errorf("%s has no %s; set one in Statsig, then rerun", label, noun)
	}
	if m := reStatsigMacro.FindAllString(scan.code, -1); len(m) > 0 {
		return "", false, fmt.Errorf("%s: its %s uses Statsig macros %v, which LaunchDarkly does not expand; replace them in Statsig, then rerun", label, noun, uniqueStrings(m))
	}
	if len(scan.semicolons) > 0 {
		return "", false, fmt.Errorf("%s: its %s must be a single expression; fix it in Statsig, then rerun", label, noun)
	}
	return expr, scan.lineComment, nil
}
