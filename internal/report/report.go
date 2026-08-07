// Package report generates migration reports for Statsig-to-LD conversions.
package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// Status constants for individual metric entries.
const (
	StatusConverted           = "converted"
	StatusSkippedExisting     = "skipped_existing"
	StatusSkippedIncompatible = "skipped_incompatible"
	StatusSkippedLossy        = "skipped_lossy"
	StatusFailed              = "failed"
)

// Report is the top-level migration report.
type Report struct {
	mu sync.Mutex

	Timestamp           string `json:"timestamp"`
	DryRun              bool   `json:"dry_run"`
	StatsigMetricsTotal int    `json:"statsig_metrics_total"`
	Converted           int    `json:"converted"`
	ConvertedWithWarn   int    `json:"converted_with_warnings"`
	SkippedExisting     int    `json:"skipped_existing"`
	SkippedIncompatible int    `json:"skipped_incompatible"`
	SkippedLossy        int    `json:"skipped_lossy"`
	Failed              int    `json:"failed"`

	// ByType breaks the same outcome counts down per effective Statsig metric
	// type (e.g. "sum", "ratio", "percentile"), so a reader can see which types
	// convert cleanly and which drive the incompatible/failed buckets. Keyed by
	// the effective type recorded on each MetricEntry.
	ByType map[string]*TypeBreakdown `json:"by_type"`

	Metrics []MetricEntry `json:"metrics"`
}

// TypeBreakdown tallies conversion outcomes for a single Statsig metric type.
// The counters mirror the report's top-level summary so each type's row
// reconciles against the whole.
type TypeBreakdown struct {
	Total               int `json:"total"`
	Converted           int `json:"converted"`
	ConvertedWithWarn   int `json:"converted_with_warnings"`
	SkippedExisting     int `json:"skipped_existing"`
	SkippedIncompatible int `json:"skipped_incompatible"`
	SkippedLossy        int `json:"skipped_lossy"`
	Failed              int `json:"failed"`
}

// MetricEntry records the conversion outcome for a single Statsig metric.
type MetricEntry struct {
	StatsigName string   `json:"statsig_name"`
	StatsigType string   `json:"statsig_type"`
	StatsigID   string   `json:"statsig_id"`
	Status      string   `json:"status"`
	LDKey       string   `json:"ld_key,omitempty"`
	LDProject   string   `json:"ld_project,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	Reason      string   `json:"reason,omitempty"`

	Diagnostics
}

// Diagnostics are the machine-readable per-metric fields. They exist because
// answering questions about a run (how many metrics were bound to a data source,
// which filter conditions blocked conversion, what analysis unit was resolved)
// previously meant regex-parsing the prose in Warnings. Codes and counts also
// survive the redaction customers apply before sharing a report, where free text
// frequently does not.
type Diagnostics struct {
	// WarningCodes runs parallel to Warnings: WarningCodes[i] identifies
	// Warnings[i]. Aggregate on these rather than on message text, which changes.
	WarningCodes []string `json:"warning_codes,omitempty"`

	// LossyReasons is the subset of warnings that made the conversion lossy, and
	// LossyCodes their codes. Recorded separately from Warnings so a lossy metric
	// keeps its advisory warnings too: previously a skipped-lossy entry stored only
	// the lossy reasons, which silently discarded everything else about it.
	LossyReasons []string `json:"lossy_reasons,omitempty"`
	LossyCodes   []string `json:"lossy_codes,omitempty"`

	// LDDataSource is the LaunchDarkly data source the metric resolved to, empty
	// if none. This is the main thing gating filter and window conversion, so it
	// is worth being able to count directly.
	LDDataSource string `json:"ld_data_source,omitempty"`

	// AnalysisUnits is the resolved LaunchDarkly analysis (randomization) units.
	AnalysisUnits []string `json:"analysis_units,omitempty"`

	// StatsigRollupTimeWindow is the metric's effective Statsig rollup window. It
	// distinguishes a daily-participation rate (lossy) from a one-time or windowed
	// unit count (clean), which is otherwise not visible in a shared report.
	StatsigRollupTimeWindow string `json:"statsig_rollup_time_window,omitempty"`

	// StatsigSourceName is the Statsig metric source the metric reads from, which
	// is what a --source-mapping entry has to match.
	StatsigSourceName string `json:"statsig_source_name,omitempty"`

	// Filters records one entry per metric term that carried filter criteria.
	Filters []FilterOutcome `json:"filters,omitempty"`
}

// FilterOutcome is what happened to one metric term's Statsig filter criteria.
// A ratio metric produces one entry per term.
type FilterOutcome struct {
	Term             string `json:"term"`
	Criteria         int    `json:"criteria"`
	Applied          bool   `json:"applied"`
	BlockedBy        string `json:"blocked_by,omitempty"`
	BlockedCondition string `json:"blocked_condition,omitempty"`
}

// New creates an empty report.
func New() *Report {
	return &Report{
		Metrics: []MetricEntry{},
	}
}

// AddConverted records a successfully converted metric. Thread-safe.
func (r *Report) AddConverted(name, typ, id, ldKey, ldProject string, warnings []string, diag Diagnostics) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Metrics = append(r.Metrics, MetricEntry{
		StatsigName: name,
		StatsigType: typ,
		StatsigID:   id,
		Status:      StatusConverted,
		LDKey:       ldKey,
		LDProject:   ldProject,
		Warnings:    warnings,
		Diagnostics: diag,
	})
}

// AddSkippedExisting records a metric that already exists in LD. Thread-safe.
func (r *Report) AddSkippedExisting(name, typ, id, ldKey, ldProject string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Metrics = append(r.Metrics, MetricEntry{
		StatsigName: name,
		StatsigType: typ,
		StatsigID:   id,
		Status:      StatusSkippedExisting,
		LDKey:       ldKey,
		LDProject:   ldProject,
	})
}

// AddSkippedIncompatible records a metric whose Statsig type has no LD equivalent. Thread-safe.
func (r *Report) AddSkippedIncompatible(name, typ, id, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Metrics = append(r.Metrics, MetricEntry{
		StatsigName: name,
		StatsigType: typ,
		StatsigID:   id,
		Status:      StatusSkippedIncompatible,
		Reason:      reason,
	})
}

// AddSkippedLossy records a metric skipped because its conversion would be lossy
// (a Statsig feature dropped or approximated) and --convert-lossy was not set.
//
// warnings is the metric's FULL warning list and diag.LossyReasons the subset
// that caused the skip. Both are kept: recording only the lossy reasons used to
// discard every advisory warning on the metric, which made questions like "what
// analysis unit did this resolve to" unanswerable for exactly the metrics most
// likely to need triage. Thread-safe.
func (r *Report) AddSkippedLossy(name, typ, id string, warnings []string, diag Diagnostics) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Metrics = append(r.Metrics, MetricEntry{
		StatsigName: name,
		StatsigType: typ,
		StatsigID:   id,
		Status:      StatusSkippedLossy,
		Reason:      "lossy conversion — skipped; re-run with --convert-lossy to convert it anyway",
		Warnings:    warnings,
		Diagnostics: diag,
	})
}

// AddFailed records a metric that failed during conversion or creation. Thread-safe.
func (r *Report) AddFailed(name, typ, id, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Metrics = append(r.Metrics, MetricEntry{
		StatsigName: name,
		StatsigType: typ,
		StatsigID:   id,
		Status:      StatusFailed,
		Reason:      reason,
	})
}

// Finalize sets the timestamp and computes summary counts from the metrics list.
func (r *Report) Finalize(totalMetrics int) {
	r.Timestamp = time.Now().UTC().Format(time.RFC3339)
	r.StatsigMetricsTotal = totalMetrics

	r.Converted = 0
	r.ConvertedWithWarn = 0
	r.SkippedExisting = 0
	r.SkippedIncompatible = 0
	r.SkippedLossy = 0
	r.Failed = 0
	r.ByType = map[string]*TypeBreakdown{}

	for _, m := range r.Metrics {
		bt := r.ByType[m.StatsigType]
		if bt == nil {
			bt = &TypeBreakdown{}
			r.ByType[m.StatsigType] = bt
		}
		bt.Total++

		switch m.Status {
		case StatusConverted:
			r.Converted++
			bt.Converted++
			if len(m.Warnings) > 0 {
				r.ConvertedWithWarn++
				bt.ConvertedWithWarn++
			}
		case StatusSkippedExisting:
			r.SkippedExisting++
			bt.SkippedExisting++
		case StatusSkippedIncompatible:
			r.SkippedIncompatible++
			bt.SkippedIncompatible++
		case StatusSkippedLossy:
			r.SkippedLossy++
			bt.SkippedLossy++
		case StatusFailed:
			r.Failed++
			bt.Failed++
		}
	}
}

// printByTypeTable writes the per-effective-type outcome breakdown as a compact
// table, types sorted by name for stable output. Skipped when nothing ran.
func (r *Report) printByTypeTable(w io.Writer) {
	if len(r.ByType) == 0 {
		return
	}

	types := make([]string, 0, len(r.ByType))
	for typ := range r.ByType {
		types = append(types, typ)
	}
	sort.Strings(types)

	fmt.Fprintln(w, "\nBy metric type")
	fmt.Fprintln(w, "─────────────────────────────────────")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  TYPE\tTOTAL\tCONVERTED\t+WARN\tEXISTING\tINCOMPAT\tLOSSY\tFAILED")
	for _, typ := range types {
		b := r.ByType[typ]
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
			typ, b.Total, b.Converted, b.ConvertedWithWarn,
			b.SkippedExisting, b.SkippedIncompatible, b.SkippedLossy, b.Failed)
	}
	tw.Flush()
	fmt.Fprintln(w, "─────────────────────────────────────")
}

// WriteCSV writes the report metrics as CSV to the given writer.
func (r *Report) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// CSV carries the flat diagnostics. The nested per-term filter detail is
	// JSON-only; here it collapses to applied/blocked counts, which is what a
	// spreadsheet reader wants anyway.
	header := []string{
		"statsig_name", "statsig_type", "statsig_id", "status", "ld_key", "ld_project", "warnings", "reason",
		"warning_codes", "lossy_codes", "ld_data_source", "analysis_units",
		"statsig_rollup_time_window", "statsig_source_name", "filters_applied", "filters_blocked",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, m := range r.Metrics {
		warnings := strings.Join(m.Warnings, "; ")
		var applied, blocked int
		for _, f := range m.Filters {
			if f.Applied {
				applied++
			} else {
				blocked++
			}
		}
		row := []string{m.StatsigName, m.StatsigType, m.StatsigID, m.Status, m.LDKey, m.LDProject, warnings, m.Reason,
			strings.Join(m.WarningCodes, " "), strings.Join(m.LossyCodes, " "), m.LDDataSource,
			strings.Join(m.AnalysisUnits, " "), m.StatsigRollupTimeWindow, m.StatsigSourceName,
			strconv.Itoa(applied), strconv.Itoa(blocked)}
		if err := cw.Write(row); err != nil {
			return err
		}
	}

	return cw.Error()
}

// PrintSummaryTable writes a formatted summary table to the given writer.
func (r *Report) PrintSummaryTable(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	title := "Migration Summary"
	convertedLabel := "  Converted:"
	if r.DryRun {
		title = "Migration Summary (dry run — no metrics created)"
		convertedLabel = "  Would convert:"
	}
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, title)
	fmt.Fprintln(tw, "─────────────────────────────────────")
	fmt.Fprintf(tw, "  Total Statsig metrics:\t%d\n", r.StatsigMetricsTotal)
	fmt.Fprintf(tw, "%s\t%d\n", convertedLabel, r.Converted)
	if r.ConvertedWithWarn > 0 {
		fmt.Fprintf(tw, "    with warnings:\t%d\n", r.ConvertedWithWarn)
	}
	fmt.Fprintf(tw, "  Already existing (skipped):\t%d\n", r.SkippedExisting)
	fmt.Fprintf(tw, "  Incompatible (skipped):\t%d\n", r.SkippedIncompatible)
	fmt.Fprintf(tw, "  Lossy (skipped; use --convert-lossy):\t%d\n", r.SkippedLossy)
	fmt.Fprintf(tw, "  Failed:\t%d\n", r.Failed)
	fmt.Fprintln(tw, "─────────────────────────────────────")
	tw.Flush()

	r.printByTypeTable(w)

	// When metrics failed, name them inline so the reader doesn't have to open
	// the report file to learn what broke.
	if r.Failed > 0 {
		fmt.Fprintln(w, "\nFailed metrics:")
		for _, m := range r.Metrics {
			if m.Status == StatusFailed {
				fmt.Fprintf(w, "  - %s (%s): %s\n", m.StatsigName, m.StatsigType, m.Reason)
			}
		}
	}
}
