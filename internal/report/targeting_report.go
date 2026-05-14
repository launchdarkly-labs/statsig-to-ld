package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"sync"
	"text/tabwriter"
	"time"
)

const (
	TargetingStatusApplied        = "applied"
	TargetingStatusSkippedLossy   = "skipped_lossy"
	TargetingStatusSkippedNoSrc   = "skipped_no_source"
	TargetingStatusSkippedDryRun  = "skipped_dry_run"
	TargetingStatusFailed         = "failed"
)

// TargetingReport is the top-level report for `targeting import`.
type TargetingReport struct {
	mu sync.Mutex

	Timestamp        string `json:"timestamp"`
	FlagsConsidered  int    `json:"flags_considered"`
	Applied          int    `json:"applied"`
	SkippedLossy     int    `json:"skipped_lossy"`
	SkippedNoSource  int    `json:"skipped_no_source"`
	SkippedDryRun    int    `json:"skipped_dry_run"`
	Failed           int    `json:"failed"`

	Flags []TargetingEntry `json:"flags"`

	// Notes is the global notes list (env reconciler outcomes, override fetch
	// failures). Flag-specific notes live inline on each TargetingEntry.
	Notes []TargetingNote `json:"notes,omitempty"`
}

// TargetingEntry records the per-flag outcome.
type TargetingEntry struct {
	FlagKey        string          `json:"flag_key"`
	Status         string          `json:"status"`
	LossyFeatures  []string        `json:"lossy_features,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Notes          []TargetingNote `json:"notes,omitempty"`
}

// TargetingNote mirrors the targeting package's Note type with JSON-safe
// fields (no internal types leak into the report).
type TargetingNote struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func NewTargetingReport() *TargetingReport {
	return &TargetingReport{Flags: []TargetingEntry{}}
}

func (r *TargetingReport) AddApplied(flagKey string, notes []TargetingNote) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Flags = append(r.Flags, TargetingEntry{
		FlagKey: flagKey,
		Status:  TargetingStatusApplied,
		Notes:   notes,
	})
}

func (r *TargetingReport) AddSkippedLossy(flagKey string, lossy []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Flags = append(r.Flags, TargetingEntry{
		FlagKey:       flagKey,
		Status:        TargetingStatusSkippedLossy,
		LossyFeatures: lossy,
		Reason:        "Targeting would lose information without --accept-data-loss",
	})
}

func (r *TargetingReport) AddSkippedNoSource(flagKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Flags = append(r.Flags, TargetingEntry{
		FlagKey: flagKey,
		Status:  TargetingStatusSkippedNoSrc,
		Reason:  "Flag is tagged but no matching Statsig source was found",
	})
}

func (r *TargetingReport) AddSkippedDryRun(flagKey string, notes []TargetingNote) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Flags = append(r.Flags, TargetingEntry{
		FlagKey: flagKey,
		Status:  TargetingStatusSkippedDryRun,
		Notes:   notes,
	})
}

func (r *TargetingReport) AddFailed(flagKey, reason string, notes []TargetingNote) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Flags = append(r.Flags, TargetingEntry{
		FlagKey: flagKey,
		Status:  TargetingStatusFailed,
		Reason:  reason,
		Notes:   notes,
	})
}

func (r *TargetingReport) AddGlobalNote(severity, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Notes = append(r.Notes, TargetingNote{Severity: severity, Message: message})
}

func (r *TargetingReport) Finalize(flagsConsidered int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Timestamp = time.Now().UTC().Format(time.RFC3339)
	r.FlagsConsidered = flagsConsidered
	r.Applied = 0
	r.SkippedLossy = 0
	r.SkippedNoSource = 0
	r.SkippedDryRun = 0
	r.Failed = 0
	for _, e := range r.Flags {
		switch e.Status {
		case TargetingStatusApplied:
			r.Applied++
		case TargetingStatusSkippedLossy:
			r.SkippedLossy++
		case TargetingStatusSkippedNoSrc:
			r.SkippedNoSource++
		case TargetingStatusSkippedDryRun:
			r.SkippedDryRun++
		case TargetingStatusFailed:
			r.Failed++
		}
	}
}

func (r *TargetingReport) PrintSummaryTable(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Targeting Import Summary")
	fmt.Fprintln(tw, "─────────────────────────────────────")
	fmt.Fprintf(tw, "  Flags considered:\t%d\n", r.FlagsConsidered)
	fmt.Fprintf(tw, "  Applied:\t%d\n", r.Applied)
	if r.SkippedDryRun > 0 {
		fmt.Fprintf(tw, "  Skipped (dry-run):\t%d\n", r.SkippedDryRun)
	}
	if r.SkippedLossy > 0 {
		fmt.Fprintf(tw, "  Skipped (lossy, see D8):\t%d   (re-run with --accept-data-loss to import)\n", r.SkippedLossy)
	}
	if r.SkippedNoSource > 0 {
		fmt.Fprintf(tw, "  Skipped (no matching source):\t%d\n", r.SkippedNoSource)
	}
	if r.Failed > 0 {
		fmt.Fprintf(tw, "  Failed:\t%d\n", r.Failed)
	}
	if len(r.Notes) > 0 {
		fmt.Fprintf(tw, "  Global notes:\t%d (see JSON report for detail)\n", len(r.Notes))
	}
	fmt.Fprintln(tw, "─────────────────────────────────────")
	_ = tw.Flush()
}

func (r *TargetingReport) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{"flag_key", "status", "lossy_features", "reason", "note_count"}); err != nil {
		return err
	}
	for _, e := range r.Flags {
		lossy := ""
		for i, l := range e.LossyFeatures {
			if i > 0 {
				lossy += ";"
			}
			lossy += l
		}
		if err := cw.Write([]string{
			e.FlagKey,
			e.Status,
			lossy,
			e.Reason,
			fmt.Sprintf("%d", len(e.Notes)),
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}
