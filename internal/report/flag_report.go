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

// Flag-specific status constants. Status names align with the metric report
// where the concept is the same (existing → skipped_existing, failed →
// failed). Created/Lossy are flag-specific.
const (
	FlagStatusCreated         = "created"
	FlagStatusSkippedExisting = "skipped_existing"
	FlagStatusFailed          = "failed"
)

// FlagReport is the top-level migration report for `flags import`.
type FlagReport struct {
	mu sync.Mutex

	Timestamp       string `json:"timestamp"`
	StatsigSrcTotal int    `json:"statsig_source_total"`
	Created         int    `json:"created"`
	SkippedExisting int    `json:"skipped_existing"`
	Failed          int    `json:"failed"`

	// LossyTargetingPreview counts source items whose targeting will be
	// lossy under D8 when `targeting import` runs. Informational — flag
	// shells are still created in this PR. See decision D8 for the list.
	LossyTargetingPreview int `json:"lossy_targeting_preview"`

	Flags []FlagEntry `json:"flags"`
}

// FlagEntry records the outcome for a single Statsig gate or dynamic config.
type FlagEntry struct {
	StatsigName string `json:"statsig_name"`
	// StatsigKind is "gate" or "dynamic_config".
	StatsigKind string   `json:"statsig_kind"`
	StatsigID   string   `json:"statsig_id"`
	Status      string   `json:"status"`
	LDKey       string   `json:"ld_key,omitempty"`
	LDProject   string   `json:"ld_project,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	// LossyTargeting lists D8 feature names this source will fail-closed on
	// when `targeting import` runs (e.g. "segments", "prerequisites",
	// "custom_unit_id"). Empty when the targeting is fully importable.
	LossyTargeting []string `json:"lossy_targeting,omitempty"`
}

// NewFlagReport creates an empty FlagReport.
func NewFlagReport() *FlagReport {
	return &FlagReport{Flags: []FlagEntry{}}
}

// AddCreated records a successfully-created flag. Thread-safe.
func (r *FlagReport) AddCreated(name, kind, id, ldKey, ldProject string, lossy []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Flags = append(r.Flags, FlagEntry{
		StatsigName:    name,
		StatsigKind:    kind,
		StatsigID:      id,
		Status:         FlagStatusCreated,
		LDKey:          ldKey,
		LDProject:      ldProject,
		LossyTargeting: lossy,
	})
}

// AddSkippedExisting records a flag that already exists in LD. Thread-safe.
func (r *FlagReport) AddSkippedExisting(name, kind, id, ldKey, ldProject string, lossy []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Flags = append(r.Flags, FlagEntry{
		StatsigName:    name,
		StatsigKind:    kind,
		StatsigID:      id,
		Status:         FlagStatusSkippedExisting,
		LDKey:          ldKey,
		LDProject:      ldProject,
		LossyTargeting: lossy,
	})
}

// AddFailed records a flag whose creation failed. Thread-safe.
func (r *FlagReport) AddFailed(name, kind, id, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Flags = append(r.Flags, FlagEntry{
		StatsigName: name,
		StatsigKind: kind,
		StatsigID:   id,
		Status:      FlagStatusFailed,
		Reason:      reason,
	})
}

// Finalize computes the summary counters from the per-flag entries.
func (r *FlagReport) Finalize(srcTotal int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Timestamp = time.Now().UTC().Format(time.RFC3339)
	r.StatsigSrcTotal = srcTotal
	r.Created = 0
	r.SkippedExisting = 0
	r.Failed = 0
	r.LossyTargetingPreview = 0
	for _, e := range r.Flags {
		switch e.Status {
		case FlagStatusCreated:
			r.Created++
		case FlagStatusSkippedExisting:
			r.SkippedExisting++
		case FlagStatusFailed:
			r.Failed++
		}
		if len(e.LossyTargeting) > 0 {
			r.LossyTargetingPreview++
		}
	}
}

// PrintSummaryTable writes the human-readable summary to w.
func (r *FlagReport) PrintSummaryTable(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Flag Import Summary")
	fmt.Fprintln(tw, "─────────────────────────────────────")
	fmt.Fprintf(tw, "  Statsig sources:\t%d\n", r.StatsigSrcTotal)
	fmt.Fprintf(tw, "  Created:\t%d\n", r.Created)
	fmt.Fprintf(tw, "  Already existing (skipped):\t%d\n", r.SkippedExisting)
	if r.Failed > 0 {
		fmt.Fprintf(tw, "  Failed:\t%d\n", r.Failed)
	}
	if r.LossyTargetingPreview > 0 {
		fmt.Fprintf(tw, "  Lossy targeting expected:\t%d   (run `targeting import --accept-data-loss` after)\n", r.LossyTargetingPreview)
	}
	fmt.Fprintln(tw, "─────────────────────────────────────")
	_ = tw.Flush()
}

// WriteCSV writes the flag entries as CSV to w.
func (r *FlagReport) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{"statsig_name", "statsig_kind", "statsig_id", "status", "ld_key", "ld_project", "lossy_targeting", "reason"}); err != nil {
		return err
	}
	for _, e := range r.Flags {
		if err := cw.Write([]string{
			e.StatsigName,
			e.StatsigKind,
			e.StatsigID,
			e.Status,
			e.LDKey,
			e.LDProject,
			strings.Join(e.LossyTargeting, ";"),
			e.Reason,
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}
