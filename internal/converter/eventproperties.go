package converter

import (
	"fmt"
	"slices"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

// DefaultMaxEventProperties is LaunchDarkly's default limit on a metric's event properties.
const DefaultMaxEventProperties = 10

// eventPropertyColumns picks the Statsig dimension columns that become LaunchDarkly event
// properties, in Statsig's order, spelled as the data source spells them. LaunchDarkly
// rejects the whole metric for one invalid column, so the rest are dropped with a warning.
func eventPropertyColumns(result *Result, wn *statsig.WarehouseNative, dsKey string, opts Options) []string {
	if wn == nil {
		return nil
	}
	var statsigCols []string
	for _, col := range wn.MetricDimensionColumns {
		if strings.TrimSpace(col) != "" {
			statsigCols = appendUnique(statsigCols, col)
		}
	}
	if len(statsigCols) == 0 {
		return nil
	}
	if dsKey == "" {
		result.addWarning(WarnEventPropertyNoDataSource,
			"Statsig dimension columns %s not carried over: LaunchDarkly slices results by event properties only on metrics with a data source. Bind one with --ld-data-source or --source-mapping",
			quoteList(statsigCols))
		return nil
	}

	dsCols, known := opts.DataSourceEventPropertyColumns[dsKey]
	var kept, missing, unsupported []string
	for _, col := range statsigCols {
		if !known {
			kept = append(kept, col)
			continue
		}
		c, ok := findEventPropertyColumn(dsCols, col)
		switch {
		case !ok:
			missing = append(missing, col)
		case c.Unsupported != "":
			unsupported = appendUnique(unsupported, fmt.Sprintf("%q (%s)", c.Name, c.Unsupported))
		default:
			kept = appendUnique(kept, c.Name)
		}
	}
	if !known {
		result.addNote(WarnColumnUnverified)
	}
	if len(unsupported) > 0 {
		result.addWarning(WarnEventPropertyUnsupported,
			"Statsig dimension columns not carried over, because LaunchDarkly slices results only by string, boolean and integer columns other than the timestamp and context key columns: %s",
			strings.Join(unsupported, ", "))
	}
	if len(missing) > 0 {
		result.addWarning(WarnEventPropertyNotInDataSource,
			"Statsig dimension columns %s not carried over: data source %q has no such columns. Check that its query selects them and that its column list in LaunchDarkly is current",
			quoteList(missing), dsKey)
	}

	limit := opts.MaxEventProperties
	if limit <= 0 {
		limit = DefaultMaxEventProperties
	}
	if len(kept) > limit {
		result.addWarning(WarnEventPropertyLimit,
			"Statsig dimension columns %s not carried over: LaunchDarkly takes at most %d event properties per metric, so the first %d were kept. If LaunchDarkly raised the limit for this account, pass --max-event-properties",
			quoteList(kept[limit:]), limit, limit)
		kept = kept[:limit]
	}
	return kept
}

// findEventPropertyColumn finds col among a data source's columns: the exact spelling,
// or else one that differs only in case, preferring one LaunchDarkly accepts.
func findEventPropertyColumn(cols []warehouse.EventPropertyColumn, col string) (warehouse.EventPropertyColumn, bool) {
	if i := slices.IndexFunc(cols, func(c warehouse.EventPropertyColumn) bool { return c.Name == col }); i >= 0 {
		return cols[i], true
	}
	for _, accepted := range []bool{true, false} {
		for _, c := range cols {
			if (c.Unsupported == "") == accepted && strings.EqualFold(c.Name, col) {
				return c, true
			}
		}
	}
	return warehouse.EventPropertyColumn{}, false
}

// DropEventPropertiesNotEnabled removes the metric's event properties, for an account
// where LaunchDarkly has not enabled them, and records why.
func (r *Result) DropEventPropertiesNotEnabled() {
	cols := r.LDMetric.SlicedEventPropertyColumns
	if len(cols) == 0 {
		return
	}
	r.LDMetric.SlicedEventPropertyColumns = nil
	r.addWarning(WarnEventPropertiesNotEnabled,
		"LaunchDarkly has not enabled event properties for this account, so the metric is created without its Statsig dimension columns %s. Once they are enabled, delete the metric and run again to add them",
		quoteList(cols))
}

// DropEventPropertiesRejected removes the metric's event properties after LaunchDarkly
// rejected them for the given reason, and records why.
func (r *Result) DropEventPropertiesRejected(reason string) {
	cols := r.LDMetric.SlicedEventPropertyColumns
	if len(cols) == 0 {
		return
	}
	r.LDMetric.SlicedEventPropertyColumns = nil
	r.addWarning(WarnEventPropertiesRejected,
		"LaunchDarkly rejected the Statsig dimension columns %s (%s), so the metric is created without them. Once the cause is fixed, delete the metric and run again to add them",
		quoteList(cols), reason)
}

func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}
