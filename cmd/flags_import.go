package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/analyze"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/flag"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/httputil"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/report"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
	"github.com/spf13/cobra"
)

const (
	importTypeGates          = "gates"
	importTypeDynamicConfigs = "dynamic-configs"
	importTypeBoth           = "both"

	defaultLDImportTag = "imported-from-statsig"
)

var flagsImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import Statsig feature gates and dynamic configs as LaunchDarkly flag shells",
	Long: `Fetch Statsig gates and/or dynamic configs and create LaunchDarkly flag
shells for them. Per-environment targeting rules are NOT applied here —
run "targeting import" next.

By default both gates and dynamic configs are imported. Use --import-type
to scope to one or the other. Use --include-tag to filter the Statsig
source by tag.

The flag shells are tagged "imported-from-statsig" by default so the
"targeting import" pass and any re-runs can find them. The tag is
configurable via --ld-tag.

Idempotency: existing flags are matched by sanitized LD key (decision D6).
If the Statsig source is renamed but its ID stays stable, re-running
will not create duplicates.

D8 note: this command creates flag shells regardless of whether the
source has lossy targeting. The report annotates each entry with the
lossy-targeting features that will require --accept-data-loss when you
run "targeting import" next.

Examples:
  # Dry-run an import of all gates and configs
  statsig-to-ld flags import --all --dry-run \
    --statsig-key console-... --ld-key api-... --ld-project my-project

  # Only import feature gates, tagged "p0" in Statsig
  statsig-to-ld flags import --all --import-type gates --include-tag p0 \
    --statsig-key console-... --ld-key api-... --ld-project my-project

  # Use a custom LD tag for traceability
  statsig-to-ld flags import --all --ld-tag from-statsig-2026-may \
    --statsig-key console-... --ld-key api-... --ld-project my-project`,
	RunE: runFlagsImport,
}

var (
	flagFIAll           bool
	flagFIDryRun        bool
	flagFIImportType    string
	flagFIIncludeTag    string
	flagFILDTag         string
	flagFILDMaintainer  string
	flagFIStatsigKey    string
	flagFIStatsigURL    string
	flagFILDKey         string
	flagFILDURL         string
	flagFILDProject     string
	flagFIOutput        string
	flagFIFormat        string
	flagFIConcurrency   int
	flagFIVerbose       bool
)

func init() {
	flagsCmd.AddCommand(flagsImportCmd)

	flagsImportCmd.Flags().BoolVar(&flagFIAll, "all", false, "Import all gates and/or dynamic configs (per --import-type)")
	flagsImportCmd.Flags().BoolVar(&flagFIDryRun, "dry-run", false, "Preview without creating any LD flags")
	flagsImportCmd.Flags().StringVar(&flagFIImportType, "import-type", importTypeBoth, "What to import: gates | dynamic-configs | both")
	flagsImportCmd.Flags().StringVar(&flagFIIncludeTag, "include-tag", "", "Only import Statsig gates/dynamic configs with this tag (single value)")
	flagsImportCmd.Flags().StringVar(&flagFILDTag, "ld-tag", defaultLDImportTag, "Tag applied to every created LD flag")
	flagsImportCmd.Flags().StringVar(&flagFILDMaintainer, "ld-maintainer", "", "LD user ID to set as maintainer on every created flag (optional)")

	flagsImportCmd.Flags().StringVar(&flagFIStatsigKey, "statsig-key", "", "Statsig Console API key (console-xxx)")
	flagsImportCmd.Flags().StringVar(&flagFIStatsigURL, "statsig-url", "", "Statsig API base URL override")
	flagsImportCmd.Flags().StringVar(&flagFILDKey, "ld-key", "", "LaunchDarkly API access token (api-xxx)")
	flagsImportCmd.Flags().StringVar(&flagFILDURL, "ld-url", "", "LaunchDarkly API base URL override")
	flagsImportCmd.Flags().StringVar(&flagFILDProject, "ld-project", "", "LaunchDarkly project key (required unless --dry-run)")

	flagsImportCmd.Flags().StringVar(&flagFIOutput, "output", "flag-import-report.json", "Path for the migration report output")
	flagsImportCmd.Flags().StringVar(&flagFIFormat, "format", "json", "Report format: json or csv")
	flagsImportCmd.Flags().IntVar(&flagFIConcurrency, "concurrency", 10, "Max concurrent LD API requests for bulk creation")
	flagsImportCmd.Flags().BoolVarP(&flagFIVerbose, "verbose", "v", false, "Show detailed per-flag progress")
}

func runFlagsImport(cmd *cobra.Command, args []string) error {
	httputil.SetVersion(version)

	if flagFIStatsigKey == "" {
		flagFIStatsigKey = os.Getenv("STATSIG_CONSOLE_KEY")
	}
	if flagFILDKey == "" {
		flagFILDKey = os.Getenv("LD_API_KEY")
	}

	if !flagFIAll {
		return errors.New("--all is required (per-flag selection by name is not supported in v1)")
	}
	switch flagFIImportType {
	case importTypeGates, importTypeDynamicConfigs, importTypeBoth:
	default:
		return fmt.Errorf("--import-type must be one of %q, %q, %q (got %q)",
			importTypeGates, importTypeDynamicConfigs, importTypeBoth, flagFIImportType)
	}
	if flagFIFormat != "json" && flagFIFormat != "csv" {
		return fmt.Errorf(`--format must be "json" or "csv" (got %q)`, flagFIFormat)
	}
	if flagFIConcurrency < 1 {
		return errors.New("--concurrency must be at least 1")
	}

	if flagFIStatsigKey == "" {
		key, err := promptForKey("Statsig Console API key (console-xxx)")
		if err != nil {
			return fmt.Errorf("reading Statsig key: %w", err)
		}
		flagFIStatsigKey = key
	}
	if flagFIStatsigKey == "" {
		return errors.New("Statsig Console API key is required (set STATSIG_CONSOLE_KEY env, use --statsig-key, or enter at prompt)")
	}
	if !strings.HasPrefix(flagFIStatsigKey, "console-") {
		return errors.New(`Statsig key should start with "console-" — this is a Console API key, not a server secret key`)
	}

	if flagFILDKey == "" && !flagFIDryRun {
		key, err := promptForKey("LaunchDarkly API access token (api-xxx)")
		if err != nil {
			return fmt.Errorf("reading LD key: %w", err)
		}
		flagFILDKey = key
	}
	if flagFILDKey == "" && !flagFIDryRun {
		return errors.New("LaunchDarkly API key is required (set LD_API_KEY env, use --ld-key, or enter at prompt)")
	}
	if flagFILDKey != "" && !strings.HasPrefix(flagFILDKey, "api-") {
		return errors.New(`LaunchDarkly key should start with "api-" — this is an API access token`)
	}
	if flagFILDProject == "" && !flagFIDryRun {
		return errors.New("--ld-project is required (LaunchDarkly project key)")
	}

	flagFIStatsigURL = strings.TrimRight(flagFIStatsigURL, "/")
	flagFILDURL = strings.TrimRight(flagFILDURL, "/")

	if flagFIFormat == "csv" && strings.HasSuffix(flagFIOutput, ".json") {
		flagFIOutput = strings.TrimSuffix(flagFIOutput, ".json") + ".csv"
	}

	ctx := cmd.Context()
	sg := statsig.NewClient(flagFIStatsigKey, flagFIStatsigURL)

	var ld *launchdarkly.Client
	if !flagFIDryRun {
		ld = launchdarkly.NewClient(flagFILDKey, flagFILDProject, flagFILDURL)
	}

	// Fetch source from Statsig
	var (
		gates []statsig.Gate
		dcs   []statsig.DynamicConfig
	)
	if flagFIImportType == importTypeGates || flagFIImportType == importTypeBoth {
		log.Println("Fetching Statsig gates...")
		var err error
		gates, err = sg.ListGates(ctx)
		if err != nil {
			return fmt.Errorf("listing gates: %w", err)
		}
		if flagFIIncludeTag != "" {
			gates = statsig.FilterGates(gates, flagFIIncludeTag)
		}
		log.Printf("Fetched %d gates", len(gates))
	}
	if flagFIImportType == importTypeDynamicConfigs || flagFIImportType == importTypeBoth {
		log.Println("Fetching Statsig dynamic configs...")
		var err error
		dcs, err = sg.ListDynamicConfigs(ctx)
		if err != nil {
			return fmt.Errorf("listing dynamic configs: %w", err)
		}
		if flagFIIncludeTag != "" {
			dcs = statsig.FilterDynamicConfigs(dcs, flagFIIncludeTag)
		}
		log.Printf("Fetched %d dynamic configs", len(dcs))
	}

	// Build LD flag shells from sources
	gateFlags, gateFailures := flag.NewFlagsFromGates(gates, flagFILDTag, flagFILDMaintainer)
	dcFlags, dcFailures := flag.NewFlagsFromDynamicConfigs(dcs, flagFILDTag, flagFILDMaintainer)

	// Map ldKey → (statsigName, statsigKind, statsigID, lossy[]) for report annotation
	type sourceMeta struct {
		Name  string
		Kind  string
		ID    string
		Lossy []string
	}
	meta := make(map[string]sourceMeta, len(gates)+len(dcs))
	for _, g := range gates {
		ldKey := flag.SanitizeKey(g.ID)
		meta[ldKey] = sourceMeta{Name: g.Name, Kind: "gate", ID: g.ID, Lossy: analyze.LossyTargetingFeatures(g)}
	}
	for _, c := range dcs {
		ldKey := flag.SanitizeKey(c.ID)
		meta[ldKey] = sourceMeta{Name: c.Name, Kind: "dynamic_config", ID: c.ID, Lossy: analyze.LossyDCTargetingFeatures(c)}
	}

	allFlags := append([]launchdarkly.Flag{}, gateFlags...)
	allFlags = append(allFlags, dcFlags...)

	// Idempotent dedupe (D6): fetch existing LD flags tagged with the import
	// tag, and filter our new flags by sanitized key.
	rpt := report.NewFlagReport()

	// Record source-construction failures (rare; from DC parsing edge cases)
	for _, ff := range gateFailures {
		rpt.AddFailed(ff.Name, "gate", ff.Name, ff.Error)
	}
	for _, ff := range dcFailures {
		rpt.AddFailed(ff.Name, "dynamic_config", ff.Name, ff.Error)
	}

	var toCreate []launchdarkly.Flag
	if !flagFIDryRun {
		log.Println("Fetching existing LaunchDarkly flags (for dedupe)...")
		existing, err := ld.ListAllFlags(ctx, flagFILDTag)
		if err != nil {
			return fmt.Errorf("listing existing LD flags: %w", err)
		}
		log.Printf("Found %d existing flags tagged %q", len(existing), flagFILDTag)

		// Record skipped-existing entries
		existingByKey := make(map[string]struct{}, len(existing))
		for _, e := range existing {
			existingByKey[e.Key] = struct{}{}
		}
		for _, f := range allFlags {
			if _, dup := existingByKey[f.Key]; dup {
				m := meta[f.Key]
				rpt.AddSkippedExisting(m.Name, m.Kind, m.ID, f.Key, flagFILDProject, m.Lossy)
			}
		}
		toCreate = flag.FilterNewFlags(allFlags, existing)
	} else {
		toCreate = allFlags
	}

	total := len(toCreate)
	if total > 0 && !flagFIVerbose {
		fmt.Fprintf(os.Stderr, "Creating %d flag shells [.=ok E=exists X=fail]: ", total)
	}

	createOne := func(ctx context.Context, f launchdarkly.Flag, idx, totalN int) {
		progress := fmt.Sprintf("[%d/%d]", idx, totalN)
		m := meta[f.Key]
		if flagFIDryRun {
			rpt.AddCreated(m.Name, m.Kind, m.ID, f.Key, flagFILDProject, m.Lossy)
			if flagFIVerbose {
				log.Printf("%s OK     %-45s  → %s", progress, m.Name, f.Key)
			} else {
				fmt.Fprint(os.Stderr, ".")
			}
			return
		}
		_, err := ld.CreateFlag(ctx, f)
		if err != nil {
			if launchdarkly.IsConflict(err) {
				rpt.AddSkippedExisting(m.Name, m.Kind, m.ID, f.Key, flagFILDProject, m.Lossy)
				if flagFIVerbose {
					log.Printf("%s EXIST  %-45s  → %s (race: dedupe missed it)", progress, m.Name, f.Key)
				} else {
					fmt.Fprint(os.Stderr, "E")
				}
				return
			}
			rpt.AddFailed(m.Name, m.Kind, m.ID, err.Error())
			if flagFIVerbose {
				log.Printf("%s FAIL   %-45s  %s", progress, m.Name, err.Error())
			} else {
				fmt.Fprint(os.Stderr, "X")
			}
			return
		}
		rpt.AddCreated(m.Name, m.Kind, m.ID, f.Key, flagFILDProject, m.Lossy)
		if flagFIVerbose {
			log.Printf("%s OK     %-45s  → %s", progress, m.Name, f.Key)
		} else {
			fmt.Fprint(os.Stderr, ".")
		}
	}

	if flagFIDryRun || total <= 1 {
		for i, f := range toCreate {
			createOne(ctx, f, i+1, total)
		}
	} else {
		var wg sync.WaitGroup
		sem := make(chan struct{}, flagFIConcurrency)
		var processed int64
	loop:
		for _, f := range toCreate {
			wg.Add(1)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Done()
				break loop
			}
			go func(f launchdarkly.Flag) {
				defer wg.Done()
				defer func() { <-sem }()
				n := int(atomic.AddInt64(&processed, 1))
				createOne(ctx, f, n, total)
			}(f)
		}
		wg.Wait()
	}
	if total > 0 && !flagFIVerbose {
		fmt.Fprintln(os.Stderr)
	}

	rpt.Finalize(len(allFlags))

	if err := writeFlagReport(rpt); err != nil {
		return err
	}
	rpt.PrintSummaryTable(os.Stdout)
	fmt.Printf("Report written to %s\n", flagFIOutput)
	return nil
}

func writeFlagReport(rpt *report.FlagReport) error {
	var data []byte
	if flagFIFormat == "csv" {
		var buf strings.Builder
		if err := rpt.WriteCSV(&buf); err != nil {
			return fmt.Errorf("generating CSV report: %w", err)
		}
		data = []byte(buf.String())
	} else {
		j, err := json.MarshalIndent(rpt, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling JSON report: %w", err)
		}
		data = j
	}
	if err := os.WriteFile(flagFIOutput, data, 0600); err != nil {
		return fmt.Errorf("writing report to %s: %w", flagFIOutput, err)
	}
	return nil
}
