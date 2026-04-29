// Package report generates migration reports for Statsig-to-LD conversions.
package report

import (
	"encoding/csv"
	"fmt"
	"io"
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
	StatusFailed              = "failed"
)

// Report is the top-level migration report.
type Report struct {
	mu sync.Mutex

	Timestamp           string        `json:"timestamp"`
	StatsigMetricsTotal int           `json:"statsig_metrics_total"`
	Converted           int           `json:"converted"`
	ConvertedWithWarn   int           `json:"converted_with_warnings"`
	SkippedExisting     int           `json:"skipped_existing"`
	SkippedIncompatible int           `json:"skipped_incompatible"`
	Failed              int           `json:"failed"`
	Metrics             []MetricEntry `json:"metrics"`
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
}

// New creates an empty report.
func New() *Report {
	return &Report{
		Metrics: []MetricEntry{},
	}
}

// AddConverted records a successfully converted metric. Thread-safe.
func (r *Report) AddConverted(name, typ, id, ldKey, ldProject string, warnings []string) {
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
	r.Failed = 0

	for _, m := range r.Metrics {
		switch m.Status {
		case StatusConverted:
			r.Converted++
			if len(m.Warnings) > 0 {
				r.ConvertedWithWarn++
			}
		case StatusSkippedExisting:
			r.SkippedExisting++
		case StatusSkippedIncompatible:
			r.SkippedIncompatible++
		case StatusFailed:
			r.Failed++
		}
	}
}

// WriteCSV writes the report metrics as CSV to the given writer.
func (r *Report) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{"statsig_name", "statsig_type", "statsig_id", "status", "ld_key", "ld_project", "warnings", "reason"}
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, m := range r.Metrics {
		warnings := strings.Join(m.Warnings, "; ")
		row := []string{m.StatsigName, m.StatsigType, m.StatsigID, m.Status, m.LDKey, m.LDProject, warnings, m.Reason}
		if err := cw.Write(row); err != nil {
			return err
		}
	}

	return cw.Error()
}

// PrintSummaryTable writes a formatted summary table to the given writer.
func (r *Report) PrintSummaryTable(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "Migration Summary")
	fmt.Fprintln(tw, "─────────────────────────────────────")
	fmt.Fprintf(tw, "  Total Statsig metrics:\t%d\n", r.StatsigMetricsTotal)
	fmt.Fprintf(tw, "  Converted:\t%d\n", r.Converted)
	if r.ConvertedWithWarn > 0 {
		fmt.Fprintf(tw, "    with warnings:\t%d\n", r.ConvertedWithWarn)
	}
	fmt.Fprintf(tw, "  Already existing (skipped):\t%d\n", r.SkippedExisting)
	fmt.Fprintf(tw, "  Incompatible (skipped):\t%d\n", r.SkippedIncompatible)
	fmt.Fprintf(tw, "  Failed:\t%d\n", r.Failed)
	fmt.Fprintln(tw, "─────────────────────────────────────")
	tw.Flush()
}
