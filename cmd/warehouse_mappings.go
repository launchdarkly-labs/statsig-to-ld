package cmd

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/output"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

// --update-mappings moves an existing data source's timestamp, value, and context
// mappings to the Statsig export's, leaving its query, key column, and columns alone.

const (
	noteMappingsUpdated        = "mappings_updated"
	noteValueColumnKept        = "value_column_kept"
	noteExportColumnNotInQuery = "export_column_not_in_query"
	noteExportColumnWrongType  = "export_column_wrong_type"
	noteContextKindNotInExport = "context_kind_not_in_export"
)

// mappingPlan is the --update-mappings part of a plan for an existing data source.
type mappingPlan struct {
	target warehouse.ExportMappings
	// readers are bound metrics that read the value column the export does not map,
	// so it is kept.
	readers []string
	ops     []launchdarkly.JSONPatchOp
	// changes read "field: old → new". With --overwrite, they and issues are an
	// estimate until the preview gives the new column list.
	changes []string
	issues  []sourceIssue
}

// sourceIssue is a per-source outcome the run prints once per code.
type sourceIssue struct {
	code, key, detail string
}

// mappingDiff moves mappings to an export's within cols.
type mappingDiff struct {
	ops     []launchdarkly.JSONPatchOp
	changes []string
	// missing and wrongType name export mappings left as they are, as `timestamp "TS"`.
	missing, wrongType []string
	// keptKinds are context kinds the export does not map; they are never removed.
	keptKinds []string
	// dropsValue: the export maps no value column, and the data source has one.
	dropsValue bool
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
	put := func(ops *[]launchdarkly.JSONPatchOp, field, label, from string, had bool, to string) {
		op := "replace"
		if !had {
			op = "add"
		}
		*ops = append(*ops, launchdarkly.JSONPatchOp{Op: op, Path: "/columnMappings/" + field, Value: to})
		d.changes = append(d.changes, mappingChange(label, from, to))
	}
	resolve := func(label, col string, typeOK func(string) bool) (string, bool) {
		real, ok := cols.find(col)
		if !ok {
			d.missing = append(d.missing, fmt.Sprintf("%s %q", label, col))
			return "", false
		}
		if typ := cols.typeOf(real); typeOK != nil && typ != "" && !typeOK(typ) {
			d.wrongType = append(d.wrongType, fmt.Sprintf("%s %q (%s)", label, real, typ))
			return "", false
		}
		return real, true
	}

	if t.Timestamp != "" {
		if real, ok := resolve("timestamp", t.Timestamp, warehouse.IsTimestampType); ok && real != d.timestamp {
			put(&d.ops, "timestampColumn", "timestamp", d.timestamp, d.timestamp != "", real)
			d.timestamp = real
		}
	}

	switch {
	case t.Value != "":
		if real, ok := resolve("value", t.Value, warehouse.IsNumericType); ok && real != d.value {
			put(&d.ops, "valueColumn", "value column", d.value, d.value != "", real)
			d.value = real
		}
	case d.value != "":
		d.dropsValue = true
		if !keepValue {
			d.ops = append(d.ops, launchdarkly.JSONPatchOp{Op: "remove", Path: "/columnMappings/valueColumn"})
			d.changes = append(d.changes, mappingChange("value column", d.value, ""))
			d.value = ""
		}
	}

	if t.Contexts == nil {
		return d
	}
	// Kinds only gain or move columns: one the export does not name may have been fixed by hand.
	final, n := maps.Clone(d.contexts), len(d.missing)
	for _, kind := range slices.Sorted(maps.Keys(d.contexts)) {
		if col, wanted := t.Contexts[kind]; !wanted {
			d.keptKinds = append(d.keptKinds, kind)
		} else if real, ok := resolve("context "+kind, col, nil); ok {
			final[kind] = real
		}
	}
	for _, kind := range slices.Sorted(maps.Keys(t.Contexts)) {
		if _, had := d.contexts[kind]; had {
			continue
		}
		// Added even when another kind maps the same column: metrics analyze by the export's kind name.
		if real, ok := resolve("context "+kind, t.Contexts[kind], nil); ok {
			final[kind] = real
		}
	}
	slices.Sort(d.missing[n:])
	var ctxOps []launchdarkly.JSONPatchOp
	for _, kind := range slices.Sorted(maps.Keys(final)) {
		if from, had := d.contexts[kind]; !had || final[kind] != from {
			put(&ctxOps, "contexts/"+launchdarkly.EscapeJSONPointer(kind), "context "+kind, from, had, final[kind])
		}
	}
	// A per-kind add needs the contexts object to exist.
	if _, isObject := old["contexts"].(map[string]any); !isObject && len(ctxOps) > 0 {
		ctxOps = []launchdarkly.JSONPatchOp{{Op: "add", Path: "/columnMappings/contexts", Value: final}}
	}
	d.ops = append(d.ops, ctxOps...)
	d.contexts = final
	return d
}

func mappingChange(label, from, to string) string {
	return fmt.Sprintf("%s: %s → %s", label, orNone(from), orNone(to))
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func mappingIssues(key string, d mappingDiff, readers []string) []sourceIssue {
	var issues []sourceIssue
	add := func(code string, details []string) {
		if len(details) > 0 {
			issues = append(issues, sourceIssue{code: code, key: key, detail: strings.Join(details, ", ")})
		}
	}
	if d.dropsValue && d.value != "" {
		add(noteValueColumnKept, readers)
	}
	add(noteExportColumnNotInQuery, d.missing)
	add(noteExportColumnWrongType, d.wrongType)
	if len(d.keptKinds) > 0 {
		add(noteContextKindNotInExport, []string{quoteAll(d.keptKinds)})
	}
	return issues
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
	if d.dropsValue {
		bound, err := e.boundMetrics(key)
		if err != nil {
			return plan.refuse("could not check bound metrics", boundMetricsUnchecked(err))
		}
		if mp.readers = valueReaders(bound); len(mp.readers) > 0 {
			d = diffMappings(old, mp.target, cols, true)
		}
	}
	mp.changes = d.changes
	mp.issues = mappingIssues(key, d, mp.readers)
	plan.mappings = mp
	if overwrite {
		return plan
	}
	if len(d.ops) == 0 {
		plan.reason, plan.markDone = "no mapping changes", true
		return plan
	}
	if reason, err := e.guardRemovals(key, old, d.ops, cols); err != nil {
		return plan.refuse(reason, err)
	}
	mp.ops = d.ops
	plan.action, plan.reason = planUpdateMappings, ""
	return plan
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
	e.finishMappings(key, plan.mappings.changes, plan.mappings.issues)
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

func (e *migrationEngine) finishMappings(key string, changes []string, issues []sourceIssue) {
	if len(changes) > 0 {
		e.report.Notes = append(e.report.Notes, reportNote{Code: noteMappingsUpdated, DataSource: key, Changes: changes})
	}
	e.recordIssues(issues)
}

func (e *migrationEngine) recordIssues(issues []sourceIssue) {
	for _, is := range issues {
		e.report.Notes = append(e.report.Notes, reportNote{Code: is.code, DataSource: is.key})
	}
	e.issues = append(e.issues, issues...)
}

// issueSummaries gives each code's issues one line; context_kind_not_in_export is a note only.
func issueSummaries(issues []sourceIssue) []string {
	byCode := map[string][]string{}
	for _, is := range issues {
		byCode[is.code] = append(byCode[is.code], is.detail+" on "+is.key)
	}
	var lines []string
	for _, s := range []struct{ code, text string }{
		{noteValueColumnKept, "%d data source(s) kept a value column the Statsig export does not map, because bound numeric metrics with no value column of their own read it: %s. Set those metrics' value column in LaunchDarkly and refresh them on running experiments, then rerun to remove it."},
		{noteExportColumnNotInQuery, "%d data source(s) did not take some mappings from the Statsig export, because their columns do not include the export's: %s. Correct the columns in Statsig, or add them to the data source's query, then rerun."},
		{noteExportColumnWrongType, "%d data source(s) did not take some mappings from the Statsig export, because the export's column has the wrong type: %s. A timestamp column needs a timestamp or date type, and a value column a numeric one; correct the column in Statsig, or cast it in the query, then rerun."},
	} {
		if entries := byCode[s.code]; len(entries) > 0 {
			lines = append(lines, fmt.Sprintf(s.text, len(entries), strings.Join(entries, "; ")))
		}
	}
	return lines
}

// overwritePatch is dataSourcePatch, with the mappings taken from the export under
// --update-mappings. dataSourcePatch runs on a copy of existing that already holds the
// export's mappings that the new columns resolve, so it falls back only for the rest;
// those are then patched from the real data source.
func (e *migrationEngine) overwritePatch(existing, body map[string]any, plan dataSourcePlan) (overwriteResult, error) {
	old := jsonutil.GetMap(existing, "columnMappings")
	if plan.mappings == nil || old == nil {
		return dataSourcePatch(existing, body, e.constantEventKey)
	}
	d := diffMappings(old, plan.mappings.target, newColumnSet(jsonutil.GetMap(body, "columnMappings")["columns"]), len(plan.mappings.readers) > 0)
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
	res, err := dataSourcePatch(base, body, e.constantEventKey)
	if err != nil {
		return res, err
	}
	if slices.ContainsFunc(res.ops, func(op launchdarkly.JSONPatchOp) bool { return op.Path == "/columnMappings/contexts" }) {
		// The fallback replaced the contexts whole, so the export's context ops no longer apply.
		d.ops = slices.DeleteFunc(d.ops, func(op launchdarkly.JSONPatchOp) bool { return strings.HasPrefix(op.Path, "/columnMappings/contexts") })
		d.changes = slices.DeleteFunc(d.changes, func(c string) bool { return strings.HasPrefix(c, "context ") })
	}
	res.ops = append(res.ops, d.ops...)
	res.mapped = &d
	return res, nil
}

// boundMetric is a project metric with its terms that read a data source: the numerator,
// or a ratio's denominator on it or with no data source of its own (listed as
// launchdarkly-hosted), which reads the numerator's.
type boundMetric struct {
	key string
	// num is the metric and den its denominator, each nil when on another data source.
	num, den map[string]any
	// units are its analysis and randomization units, which both terms use.
	units []string
}

// boundMetrics lists the project's metrics bound to dsKey, by key. Every bound-metric
// check uses it.
func (e *migrationEngine) boundMetrics(dsKey string) ([]boundMetric, error) {
	metrics, err := e.projectMetrics()
	if err != nil {
		return nil, err
	}
	var bound []boundMetric
	for _, m := range metrics {
		b := boundMetric{key: jsonutil.GetStr(m, "key")}
		numDS := jsonutil.GetStr(jsonutil.GetMap(m, "dataSource"), "key")
		if numDS == dsKey {
			b.num = m
		}
		if den := jsonutil.GetMap(m, "denominator"); den != nil {
			denDS := jsonutil.GetStr(jsonutil.GetMap(den, "dataSource"), "key")
			if denDS == "" || denDS == "launchdarkly-hosted" {
				denDS = numDS
			}
			if denDS == dsKey {
				b.den = den
			}
		}
		if b.num != nil || b.den != nil {
			b.units = slices.Concat(jsonutil.GetStrSlice(m, "analysisUnits"), jsonutil.GetStrSlice(m, "randomizationUnits"))
			bound = append(bound, b)
		}
	}
	slices.SortFunc(bound, func(a, b boundMetric) int { return strings.Compare(a.key, b.key) })
	return bound, nil
}

// valueReaders names bound numeric terms with no value column of their own, which read
// the data source's. A plain count_distinct metric counts another column.
func valueReaders(bound []boundMetric) []string {
	reads := func(term map[string]any) bool {
		return term != nil && jsonutil.GetBool(term, "isNumeric") && strings.TrimSpace(jsonutil.GetStr(term, "valueColumn")) == ""
	}
	var readers []string
	for _, b := range bound {
		num := reads(b.num) && (jsonutil.GetMap(b.num, "denominator") != nil || jsonutil.GetStr(b.num, "unitAggregationType") != "count_distinct")
		den := reads(b.den)
		switch {
		case num && den:
			readers = append(readers, b.key+" (numerator and denominator)")
		case num:
			readers = append(readers, b.key)
		case den:
			readers = append(readers, b.key+" (denominator)")
		}
	}
	return readers
}

// kindUsers names bound metrics that use any of kinds, and which of kinds are used.
func kindUsers(bound []boundMetric, kinds []string) (users, used []string) {
	for _, b := range bound {
		var mine []string
		for _, kind := range kinds {
			if slices.Contains(b.units, kind) {
				mine = append(mine, kind)
				if !slices.Contains(used, kind) {
					used = append(used, kind)
				}
			}
		}
		if len(mine) > 0 {
			users = append(users, fmt.Sprintf("%s (%s)", b.key, strings.Join(mine, ", ")))
		}
	}
	slices.Sort(used)
	return users, used
}

// guardRemovals refuses final ops that remove a value column bound numeric metrics read,
// or a context kind bound metrics use; LaunchDarkly checks neither on the PATCH. cols
// are the data source's columns after the update. The string is the dry run's reason.
func (e *migrationEngine) guardRemovals(key string, old map[string]any, ops []launchdarkly.JSONPatchOp, cols columnSet) (string, error) {
	value, contexts := mappingsAfter(old, ops)
	oldValue := jsonutil.GetStr(old, "valueColumn")
	dropsValue := oldValue != "" && value == ""
	var lost []string
	for _, kind := range slices.Sorted(maps.Keys(stringMap(old["contexts"]))) {
		if _, ok := contexts[kind]; !ok {
			lost = append(lost, kind)
		}
	}
	if !dropsValue && len(lost) == 0 {
		return "", nil
	}
	bound, err := e.boundMetrics(key)
	if err != nil {
		return "could not check bound metrics", boundMetricsUnchecked(err)
	}
	var reasons, problems []string
	if readers := valueReaders(bound); dropsValue && len(readers) > 0 {
		reasons = append(reasons, fmt.Sprintf("value column %q, read by bound metrics: %s", oldValue, strings.Join(readers, ", ")))
		fix := "set those metrics' value column in LaunchDarkly and refresh them on running experiments, then rerun"
		if _, inQuery := cols.find(oldValue); inQuery {
			problems = append(problems, fmt.Sprintf("the Statsig export maps no value column, and %d bound numeric metric(s) with no value column of their own read its value column %q: %s. Map it in the Statsig source, or %s",
				len(readers), oldValue, strings.Join(readers, ", "), fix))
		} else {
			problems = append(problems, fmt.Sprintf("its value column %q is not in the updated query, and %d bound numeric metric(s) with no value column of their own read it: %s. Keep that column in the Statsig SQL, or %s",
				oldValue, len(readers), strings.Join(readers, ", "), fix))
		}
	}
	if users, used := kindUsers(bound, lost); len(users) > 0 {
		reasons = append(reasons, fmt.Sprintf("context kind(s) %s, used by bound metrics: %s", quoteAll(used), strings.Join(users, ", ")))
		problems = append(problems, fmt.Sprintf("the updated query does not return the column of its context kind(s) %s, which bound metrics use as analysis or randomization units: %s. Keep the column in the Statsig SQL, or change those metrics' units in LaunchDarkly and refresh them on running experiments, then rerun",
			quoteAll(used), strings.Join(users, ", ")))
	}
	if len(problems) == 0 {
		return "", nil
	}
	return "would remove " + strings.Join(reasons, "; "), fmt.Errorf("not updated: %s", strings.Join(problems, "; "))
}

// mappingsAfter applies ops to old's value column and contexts.
func mappingsAfter(old map[string]any, ops []launchdarkly.JSONPatchOp) (string, map[string]string) {
	const kindPrefix = "/columnMappings/contexts/"
	value, contexts := jsonutil.GetStr(old, "valueColumn"), stringMap(old["contexts"])
	for _, op := range ops {
		col, _ := op.Value.(string)
		switch {
		case op.Op == "test":
		case op.Path == "/columnMappings":
			cm, _ := op.Value.(map[string]any)
			value, contexts = jsonutil.GetStr(cm, "valueColumn"), stringMap(cm["contexts"])
		case op.Path == "/columnMappings/valueColumn":
			value = col
		case op.Path == "/columnMappings/contexts":
			contexts = stringMap(op.Value)
		case strings.HasPrefix(op.Path, kindPrefix):
			kind := strings.NewReplacer("~1", "/", "~0", "~").Replace(strings.TrimPrefix(op.Path, kindPrefix))
			if op.Op == "remove" {
				delete(contexts, kind)
			} else {
				contexts[kind] = col
			}
		}
	}
	return value, contexts
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
