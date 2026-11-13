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

// CustomFields lists source's custom fields, skipping entries with neither key nor formula.
func CustomFields(source map[string]any) []CustomField {
	var out []CustomField
	for _, raw := range j.GetSlice(source, "customFieldMapping") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		f := CustomField{Key: j.GetStr(m, "key"), Formula: j.GetStr(m, "formula")}
		if f.Key != "" || f.Formula != "" {
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
	inner, err := PrepareSourceSQL(sql, whType)
	if err != nil {
		return "", err
	}
	cols := make([]string, len(fields))
	for i, f := range fields {
		expr, err := customFieldExpr(f, whType)
		if err != nil {
			return "", err
		}
		cols[i] = expr + " AS " + f.Key
	}
	return fmt.Sprintf("SELECT *, %s FROM (\n%s\n) AS %s", strings.Join(cols, ", "), inner, customFieldsAlias), nil
}

func customFieldExpr(f CustomField, whType string) (string, error) {
	if f.Key == "" {
		return "", fmt.Errorf("a Statsig custom field (formula %q) has no name", f.Formula)
	}
	if !reColumnName.MatchString(f.Key) {
		return "", fmt.Errorf("Statsig custom field %q: a column name must be letters, digits, and underscores; rename the field in Statsig", f.Key)
	}
	formula := strings.TrimSpace(f.Formula)
	scan, err := scanSQL(formula, whType)
	if err != nil {
		return "", fmt.Errorf("Statsig custom field %q: could not read its formula: %w", f.Key, err)
	}
	if len(scan.significant) == 0 {
		return "", fmt.Errorf("Statsig custom field %q has no formula", f.Key)
	}
	if m := reStatsigMacro.FindAllString(scan.code, -1); len(m) > 0 {
		return "", fmt.Errorf("Statsig custom field %q: its formula uses Statsig macros %v, which LaunchDarkly does not expand; replace them in Statsig, or add the column to the data source's query by hand", f.Key, uniqueStrings(m))
	}
	if len(scan.semicolons) > 0 {
		return "", fmt.Errorf("Statsig custom field %q: its formula must be a single expression", f.Key)
	}
	if scan.lineComment {
		return "(\n" + formula + "\n)", nil
	}
	return formula, nil
}
