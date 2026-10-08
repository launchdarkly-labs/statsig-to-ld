package cmd

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/output"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

// --update-mappings moves an existing data source's timestamp, value, and context
// mappings to the Statsig export's, leaving its query, key column, and columns alone.

const noteMappingsUpdated = "mappings_updated"

// mappingPlan is the --update-mappings part of a plan for an existing data source.
type mappingPlan struct {
	target    warehouse.ExportMappings
	keepValue bool
	ops       []launchdarkly.JSONPatchOp
	// changes read "field: old → new". With --overwrite they are an estimate until
	// the preview gives the new column list.
	changes []string
}

// mappingDiff moves mappings to an export's within cols; ops[i] makes changes[i].
type mappingDiff struct {
	ops     []launchdarkly.JSONPatchOp
	changes []string
	// missing names export columns that cols lacks; those mappings stay as they are.
	missing      []string
	removedKinds []string
	removesValue bool
	// The mappings after ops.
	timestamp, value string
	contexts         map[string]string
}

func diffMappings(old map[string]any, t warehouse.ExportMappings, cols columnSet, keepValue bool) mappingDiff {
	d := mappingDiff{
		timestamp: jsonutil.GetStr(old, "timestampColumn"),
		value:     jsonutil.GetStr(old, "valueColumn"),
		contexts:  stringMap(old["contexts"]),
	}
	// "add" where the field is absent: LaunchDarkly omits an unset valueColumn.
	put := func(ops *[]launchdarkly.JSONPatchOp, changes *[]string, field, label, from string, had bool, to string) {
		op := "replace"
		if !had {
			op = "add"
		}
		*ops = append(*ops, launchdarkly.JSONPatchOp{Op: op, Path: "/columnMappings/" + field, Value: to})
		*changes = append(*changes, fmt.Sprintf("%s: %s → %s", label, orNone(from), to))
	}
	remove := func(ops *[]launchdarkly.JSONPatchOp, changes *[]string, field, label, from string) {
		*ops = append(*ops, launchdarkly.JSONPatchOp{Op: "remove", Path: "/columnMappings/" + field})
		*changes = append(*changes, fmt.Sprintf("%s: %s → (none)", label, from))
	}

	if t.Timestamp != "" {
		if real, ok := cols.find(t.Timestamp); !ok {
			d.missing = append(d.missing, fmt.Sprintf("timestamp (%q)", t.Timestamp))
		} else if real != d.timestamp {
			put(&d.ops, &d.changes, "timestampColumn", "timestamp", d.timestamp, d.timestamp != "", real)
			d.timestamp = real
		}
	}

	switch {
	case t.Value != "":
		if real, ok := cols.find(t.Value); !ok {
			d.missing = append(d.missing, fmt.Sprintf("value (%q)", t.Value))
		} else if real != d.value {
			put(&d.ops, &d.changes, "valueColumn", "value column", d.value, d.value != "", real)
			d.value = real
		}
	case d.value != "":
		d.removesValue = true
		if !keepValue {
			remove(&d.ops, &d.changes, "valueColumn", "value column", d.value)
			d.value = ""
		}
	}

	if t.Contexts == nil {
		return d
	}
	final := maps.Clone(d.contexts)
	var ops []launchdarkly.JSONPatchOp
	var changes, removed []string
	for _, kind := range slices.Sorted(maps.Keys(union(d.contexts, t.Contexts))) {
		field := "contexts/" + launchdarkly.EscapeJSONPointer(kind)
		from, had := d.contexts[kind]
		col, wanted := t.Contexts[kind]
		if !wanted {
			remove(&ops, &changes, field, "context "+kind, from)
			removed = append(removed, kind)
			delete(final, kind)
			continue
		}
		real, ok := cols.find(col)
		if !ok {
			d.missing = append(d.missing, fmt.Sprintf("context %s (%q)", kind, col))
			continue
		}
		if !had || real != from {
			put(&ops, &changes, field, "context "+kind, from, had, real)
			final[kind] = real
		}
	}
	// LaunchDarkly requires a context; leave them all when none of the export's resolves.
	if len(final) == 0 {
		return d
	}
	d.ops = append(d.ops, ops...)
	d.changes = append(d.changes, changes...)
	d.removedKinds, d.contexts = removed, final
	return d
}

func union(a, b map[string]string) map[string]string {
	out := maps.Clone(a)
	maps.Copy(out, b)
	return out
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// approxColumns stands in for an --overwrite's new column list in the plan: the
// existing mapped columns, then the export's that match none of them.
func approxColumns(old map[string]any, t warehouse.ExportMappings) columnSet {
	var cols []warehouse.FallbackColumn
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[strings.ToLower(name)] {
			seen[strings.ToLower(name)] = true
			cols = append(cols, warehouse.FallbackColumn{Name: name})
		}
	}
	oldCtx := stringMap(old["contexts"])
	add(jsonutil.GetStr(old, "timestampColumn"))
	add(jsonutil.GetStr(old, "valueColumn"))
	for _, kind := range slices.Sorted(maps.Keys(oldCtx)) {
		add(oldCtx[kind])
	}
	add(t.Timestamp)
	add(t.Value)
	for _, kind := range slices.Sorted(maps.Keys(t.Contexts)) {
		add(t.Contexts[kind])
	}
	return newColumnSet(cols)
}

// planMappings turns the plan for an existing data source into a mapping update.
// Only bound metrics can stop one: a context kind they use, or a value column they read.
func (e *migrationEngine) planMappings(plan dataSourcePlan, ds, source map[string]any) dataSourcePlan {
	if !e.updateMappings || ds == nil || (plan.action != planSkip && plan.action != planUpdate) {
		return plan
	}
	key := jsonutil.GetStr(ds, "key")
	old := jsonutil.GetMap(ds, "columnMappings")
	mp := &mappingPlan{target: warehouse.MappingsFromExport(source)}
	overwrite := plan.action == planUpdate
	cols := newColumnSet(old["columns"])
	if overwrite {
		cols = approxColumns(old, mp.target)
	}
	d := diffMappings(old, mp.target, cols, false)

	parts := missingColumns(d.missing)
	if len(d.removedKinds) > 0 || d.removesValue {
		metrics, err := e.projectMetrics()
		if err != nil {
			return dataSourcePlan{
				action: planRefuse,
				reason: "could not check bound metrics",
				err:    fmt.Errorf("not updated, because its bound metrics could not be checked: %w. Rerun to try again", err),
			}
		}
		if users := kindUsers(metrics, key, d.removedKinds); len(users) > 0 {
			return dataSourcePlan{
				action: planRefuse,
				reason: fmt.Sprintf("removes context kind(s) %s, used by bound metrics: %s", quoteAll(d.removedKinds), strings.Join(users, ", ")),
				err: fmt.Errorf("not updated: the Statsig export no longer maps context kind(s) %s, which bound metrics use as analysis units: %s. Change those metrics' analysis units in LaunchDarkly, or map the kind in the Statsig source, then rerun",
					quoteAll(d.removedKinds), strings.Join(users, ", ")),
			}
		}
		if users := valueUsers(metrics, key); d.removesValue && len(users) > 0 {
			mp.keepValue = true
			d = diffMappings(old, mp.target, cols, true)
			parts = append(parts, fmt.Sprintf("Its value column %q was kept: the Statsig export maps none, but bound numeric metrics without their own value column read it (%s). Set their value column, then rerun to remove it.",
				d.value, strings.Join(users, ", ")))
		}
	}
	mp.changes = d.changes
	plan.mappings = mp
	plan.warning = mappingWarning(plan.warning, key, parts)
	if overwrite {
		return plan
	}
	if len(d.ops) == 0 {
		plan.reason, plan.markDone = "no mapping changes", true
		return plan
	}
	mp.ops = d.ops
	plan.action, plan.reason = planUpdateMappings, ""
	return plan
}

func missingColumns(missing []string) []string {
	if len(missing) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("Its query does not return the columns the Statsig export maps for %s, so those mappings were not taken from the export; correct them in Statsig or add them to its query, then rerun.", strings.Join(missing, ", "))}
}

// mappingWarning adds parts to a data source's warning, keeping one line per source.
func mappingWarning(prior, key string, parts []string) string {
	if len(parts) == 0 {
		return prior
	}
	text := strings.Join(parts, " ")
	if prior != "" {
		return prior + " " + text
	}
	return fmt.Sprintf("Data source %q: %s%s", key, strings.ToLower(text[:1]), text[1:])
}

// boundTerms reports whether m reads dsKey through its numerator or its ratio
// denominator. A denominator without a data source of its own (listed as
// launchdarkly-hosted) reads the numerator's.
func boundTerms(m map[string]any, dsKey string) (num, den bool) {
	num = jsonutil.GetStr(jsonutil.GetMap(m, "dataSource"), "key") == dsKey
	d := jsonutil.GetMap(m, "denominator")
	if d == nil {
		return num, false
	}
	switch jsonutil.GetStr(jsonutil.GetMap(d, "dataSource"), "key") {
	case dsKey:
		den = true
	case "", "launchdarkly-hosted":
		den = num
	}
	return num, den
}

// kindUsers names bound metrics whose analysis units include any of kinds. The list
// gives units per metric, so they cover the numerator and the denominator alike.
func kindUsers(metrics []map[string]any, dsKey string, kinds []string) []string {
	var users []string
	for _, m := range metrics {
		if num, den := boundTerms(m, dsKey); !num && !den {
			continue
		}
		units := append(jsonutil.GetStrSlice(m, "analysisUnits"), jsonutil.GetStrSlice(m, "randomizationUnits")...)
		var used []string
		for _, kind := range kinds {
			if slices.Contains(units, kind) {
				used = append(used, kind)
			}
		}
		if len(used) > 0 {
			users = append(users, fmt.Sprintf("%s (%s)", jsonutil.GetStr(m, "key"), strings.Join(used, ", ")))
		}
	}
	sort.Strings(users)
	return users
}

// valueUsers names bound numeric terms with no value column of their own. LaunchDarkly
// requires none for a plain count_distinct metric, which counts its own column.
func valueUsers(metrics []map[string]any, dsKey string) []string {
	var users []string
	for _, m := range metrics {
		num, den := boundTerms(m, dsKey)
		d := jsonutil.GetMap(m, "denominator")
		countDistinct := jsonutil.GetStr(m, "unitAggregationType") == "count_distinct" && d == nil
		if num && jsonutil.GetBool(m, "isNumeric") && jsonutil.GetStr(m, "valueColumn") == "" && !countDistinct {
			users = append(users, jsonutil.GetStr(m, "key"))
		}
		if den && jsonutil.GetBool(d, "isNumeric") && jsonutil.GetStr(d, "valueColumn") == "" {
			users = append(users, jsonutil.GetStr(m, "key")+" (denominator)")
		}
	}
	sort.Strings(users)
	return users
}

// patchMappings applies a mapping-only update.
func (e *migrationEngine) patchMappings(existing map[string]any, name string, plan dataSourcePlan) {
	key := jsonutil.GetStr(existing, "key")
	ops := append(patchPreconditions(existing), plan.mappings.ops...)
	if _, err := e.ld.UpdateMetricDataSource(e.ctx, key, ops); err != nil {
		e.failDataSource(key, name, mappingPatchError(err))
		return
	}
	output.Done()
	if !e.state.IsDataSourceDone(key) {
		e.state.MarkDataSourceDone(key)
	}
	e.report.DataSources.Updated++
	e.finishMappings(key, plan.mappings.changes, plan.warning)
}

func mappingPatchError(err error) error {
	switch {
	case errors.Is(err, launchdarkly.ErrPatchNotApplied):
		return fmt.Errorf("not updated: it changed in LaunchDarkly after this run read it; rerun to pick up the change")
	case strings.Contains(err.Error(), "Columns do not match query results"):
		// LaunchDarkly reruns the query on any mapping change and compares the columns exactly.
		return fmt.Errorf("not updated: LaunchDarkly reran its query to check the change, and the warehouse now returns different columns than the data source lists. Run its query preview in LaunchDarkly and save it to refresh the columns, then rerun")
	}
	return err
}

func (e *migrationEngine) finishMappings(key string, changes []string, warning string) {
	if len(changes) > 0 {
		e.report.Notes = append(e.report.Notes, reportNote{Code: noteMappingsUpdated, DataSource: key, Changes: strings.Join(changes, "; ")})
	}
	if warning != "" {
		e.warn(warning)
	}
}

// overwritePatch is dataSourcePatch, with the mappings taken from the export under
// --update-mappings. dataSourcePatch runs on a copy of existing that already holds the
// export's mappings that the new columns resolve, so it falls back only for the rest;
// those are then patched from the real data source.
func (e *migrationEngine) overwritePatch(existing, body map[string]any, plan dataSourcePlan) ([]launchdarkly.JSONPatchOp, []string, mappingDiff, error) {
	old := jsonutil.GetMap(existing, "columnMappings")
	if plan.mappings == nil || old == nil {
		ops, changes, err := dataSourcePatch(existing, body, e.constantEventKey)
		return ops, changes, mappingDiff{}, err
	}
	d := diffMappings(old, plan.mappings.target, newColumnSet(jsonutil.GetMap(body, "columnMappings")["columns"]), plan.mappings.keepValue)
	cm := maps.Clone(old)
	cm["timestampColumn"] = d.timestamp
	cm["contexts"] = d.contexts
	if d.value == "" {
		delete(cm, "valueColumn")
	} else {
		cm["valueColumn"] = d.value
	}
	base := maps.Clone(existing)
	base["columnMappings"] = cm
	ops, changes, err := dataSourcePatch(base, body, e.constantEventKey)
	if err != nil {
		return nil, nil, d, err
	}
	if slices.ContainsFunc(ops, func(op launchdarkly.JSONPatchOp) bool { return op.Path == "/columnMappings/contexts" }) {
		// The fallback replaced the contexts whole, so per-kind ops no longer apply.
		var kept mappingDiff
		for i, op := range d.ops {
			if !strings.HasPrefix(op.Path, "/columnMappings/contexts/") {
				kept.ops = append(kept.ops, op)
				kept.changes = append(kept.changes, d.changes[i])
			}
		}
		d.ops, d.changes = kept.ops, kept.changes
	}
	return append(ops, d.ops...), changes, d, nil
}

// mappingLines lists a dry run's mapping changes under the source's line.
func mappingLines(plan dataSourcePlan) string {
	if plan.mappings == nil || plan.action == planSkip {
		return ""
	}
	var b strings.Builder
	for _, c := range plan.mappings.changes {
		b.WriteString("\n        " + c)
	}
	return b.String()
}
