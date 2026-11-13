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

// ApplyCustomFields adds source's custom fields to a MapMetricSourceToDataSource body's
// query. It reports whether source has any.
func ApplyCustomFields(body, source map[string]any, whType string) (bool, error) {
	fields := CustomFields(source)
	if len(fields) == 0 {
		return false, nil
	}
	sql, err := WithCustomFields(j.GetStr(body, "sqlQuery"), fields, whType)
	if err != nil {
		return true, fmt.Errorf("data source %q: %w", j.GetStr(body, "key"), err)
	}
	body["sqlQuery"] = sql
	return true, nil
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

// DuplicateColumns names, as first listed, the columns listed more than once, ignoring case.
func DuplicateColumns(names []string) []string {
	first := map[string]string{}
	reported := map[string]bool{}
	var dup []string
	for _, n := range names {
		lower := strings.ToLower(n)
		if f, ok := first[lower]; !ok {
			first[lower] = n
		} else if !reported[lower] {
			dup, reported[lower] = append(dup, f), true
		}
	}
	return dup
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
	formula := strings.TrimSpace(f.Formula)
	scan, err := scanSQL(formula, whType)
	if err != nil {
		return "", fmt.Errorf("Statsig custom field %q: could not read its formula: %w", f.Key, err)
	}
	if len(scan.significant) == 0 {
		return "", fmt.Errorf("Statsig custom field %q has no formula; set one in Statsig, then rerun", f.Key)
	}
	if m := reStatsigMacro.FindAllString(scan.code, -1); len(m) > 0 {
		return "", fmt.Errorf("Statsig custom field %q: its formula uses Statsig macros %v, which LaunchDarkly does not expand; replace them in Statsig, then rerun", f.Key, uniqueStrings(m))
	}
	if len(scan.semicolons) > 0 {
		return "", fmt.Errorf("Statsig custom field %q: its formula must be a single expression; fix it in Statsig, then rerun", f.Key)
	}
	if scan.lineComment {
		return "(\n" + formula + "\n)", nil
	}
	return formula, nil
}
