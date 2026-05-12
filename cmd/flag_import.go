package cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/converter"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/httputil"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
)

// flagImportCmd ports the Statsig → LD flag-import logic from
// launchdarkly/goaltender/lambda_handlers/flag_import_worker (PRs #825, #828,
// #829) into the CLI. Mirrors the `convert` (metrics) command in flag shape:
// API keys via --flag → env var → prompt; --dry-run produces a report
// without writing to LD; re-runs are safe (existing LD flags are detected
// and skipped).
var flagImportCmd = &cobra.Command{
	Use:   "flag-import",
	Short: "Import Statsig feature gates or dynamic configs into LaunchDarkly as flags",
	Long: `Fetch Statsig feature gates or dynamic configs via the Console API,
convert them into LaunchDarkly flag definitions, and create them via the LD
REST API. Per-environment targeting (rules + percentage rollouts + user
overrides) is applied as a JSON Patch after flag creation.

Use --kind feature-gates to import gates as boolean flags, or
--kind dynamic-configs to import dynamic configs as JSON multi-variate flags.

API Key Security:
  Keys are resolved in order: flag → env var → interactive prompt.
  The interactive prompt is the most secure option for manual use — keys
  are entered with echo disabled and never touch disk, shell history, or
  process listings. For CI/CD, use flags or env vars injected from a
  secrets manager.

Examples:
  # Import all feature gates with targeting
  statsig-to-ld flag-import --kind feature-gates --ld-project my-project

  # Dry-run import of dynamic configs — preview only, no LD writes
  statsig-to-ld flag-import --kind dynamic-configs --dry-run

  # Import only gates with a specific Statsig tag, applying an LD tag
  statsig-to-ld flag-import --kind feature-gates \
    --include-tag mobile --tag statsig-import --ld-project my-project

  # Import gates without applying per-env targeting (shells only)
  statsig-to-ld flag-import --kind feature-gates --no-targeting \
    --ld-project my-project`,
	RunE: runFlagImport,
}

var (
	flagFIKind            string
	flagFIStatsigKey      string
	flagFIStatsigURL      string
	flagFILDKey           string
	flagFILDURL           string
	flagFILDProject       string
	flagFIIncludeTag      string
	flagFITag             string
	flagFIMaintainerID    string
	flagFINoTargeting     bool
	flagFIDryRun          bool
	flagFIOutput          string
	flagFIFormat          string
	flagFIVerbose         bool
	flagFIOverrideWorkers int
)

func init() {
	rootCmd.AddCommand(flagImportCmd)

	flagImportCmd.Flags().StringVar(&flagFIKind, "kind", "", "What to import: feature-gates or dynamic-configs (required)")

	flagImportCmd.Flags().StringVar(&flagFIStatsigKey, "statsig-key", "", "Statsig Console API key (console-xxx)")
	flagImportCmd.Flags().StringVar(&flagFIStatsigURL, "statsig-url", "", "Statsig API base URL including scheme (e.g. https://statsigapi.net/console/v1)")
	flagImportCmd.Flags().StringVar(&flagFILDKey, "ld-key", "", "LaunchDarkly API access token (api-xxx)")
	flagImportCmd.Flags().StringVar(&flagFILDURL, "ld-url", "", "LaunchDarkly API base URL including scheme (e.g. https://app.launchdarkly.com)")
	flagImportCmd.Flags().StringVar(&flagFILDProject, "ld-project", "", "LaunchDarkly project key (required for non-dry-run)")

	flagImportCmd.Flags().StringVar(&flagFIIncludeTag, "include-tag", "", "Only import Statsig gates/configs with this tag")
	flagImportCmd.Flags().StringVar(&flagFITag, "tag", "", "Tag to apply to all imported LD flags (and auto-created environments)")
	flagImportCmd.Flags().StringVar(&flagFIMaintainerID, "maintainer-id", "", "LD member ID to set as the flag maintainer (optional)")

	flagImportCmd.Flags().BoolVar(&flagFINoTargeting, "no-targeting", false, "Skip per-env targeting (create flag shells only; no rules/targets/rollouts)")
	flagImportCmd.Flags().BoolVar(&flagFIDryRun, "dry-run", false, "Preview the import without writing to LaunchDarkly")
	flagImportCmd.Flags().IntVar(&flagFIOverrideWorkers, "override-workers", 10, "Concurrent workers fetching Statsig overrides")

	flagImportCmd.Flags().StringVar(&flagFIOutput, "output", "flag-import-report.json", "Path for migration report output")
	flagImportCmd.Flags().StringVar(&flagFIFormat, "format", "json", "Report format: json or csv")
	flagImportCmd.Flags().BoolVarP(&flagFIVerbose, "verbose", "v", false, "Show detailed per-flag progress")
}

func runFlagImport(cmd *cobra.Command, args []string) error {
	httputil.SetVersion(version)
	ctx := cmd.Context()

	// --- 1. Validate flags ---
	kind, err := parseImportKind(flagFIKind)
	if err != nil {
		return err
	}
	if flagFIFormat != "json" && flagFIFormat != "csv" {
		return fmt.Errorf("--format must be \"json\" or \"csv\" (got %q)", flagFIFormat)
	}
	if flagFIOverrideWorkers < 1 {
		return fmt.Errorf("--override-workers must be at least 1")
	}

	// --- 2. Resolve credentials ---
	if flagFIStatsigKey == "" {
		flagFIStatsigKey = os.Getenv("STATSIG_CONSOLE_KEY")
	}
	if flagFILDKey == "" {
		flagFILDKey = os.Getenv("LD_API_KEY")
	}
	if flagFIStatsigKey == "" {
		key, err := promptForKey("Statsig Console API key (console-xxx)")
		if err != nil {
			return fmt.Errorf("reading Statsig key: %w", err)
		}
		flagFIStatsigKey = key
	}
	if flagFIStatsigKey == "" {
		return fmt.Errorf("Statsig Console API key is required (set STATSIG_CONSOLE_KEY env, use --statsig-key, or enter at prompt)")
	}
	if !strings.HasPrefix(flagFIStatsigKey, "console-") {
		return fmt.Errorf("Statsig key should start with \"console-\" — this is a Console API key, not a server secret key")
	}
	if flagFILDKey == "" && !flagFIDryRun {
		key, err := promptForKey("LaunchDarkly API access token (api-xxx)")
		if err != nil {
			return fmt.Errorf("reading LaunchDarkly key: %w", err)
		}
		flagFILDKey = key
	}
	if flagFILDKey == "" && !flagFIDryRun {
		return fmt.Errorf("LaunchDarkly API key is required (set LD_API_KEY env, use --ld-key, or enter at prompt)")
	}
	if flagFILDKey != "" && !strings.HasPrefix(flagFILDKey, "api-") {
		return fmt.Errorf("LaunchDarkly key should start with \"api-\" — this is an API access token")
	}
	if flagFILDProject == "" && !flagFIDryRun {
		return fmt.Errorf("--ld-project is required — specify the LaunchDarkly project key to create flags in")
	}
	flagFILDURL = strings.TrimRight(flagFILDURL, "/")
	flagFIStatsigURL = strings.TrimRight(flagFIStatsigURL, "/")

	// Fix output extension to match format
	if flagFIFormat == "csv" && strings.HasSuffix(flagFIOutput, ".json") {
		flagFIOutput = strings.TrimSuffix(flagFIOutput, ".json") + ".csv"
	}

	// --- 3. Initialize clients ---
	sgClient := statsig.NewClient(flagFIStatsigKey, flagFIStatsigURL)
	var ldClient *launchdarkly.Client
	if !flagFIDryRun {
		ldClient = launchdarkly.NewClient(flagFILDKey, flagFILDProject, flagFILDURL)
	}

	// --- 4. Fetch from Statsig ---
	var gates []statsig.Gate
	var configs []statsig.DynamicConfig
	switch kind {
	case converter.ImportKindGates:
		log.Printf("Fetching Statsig feature gates...")
		gates, err = sgClient.ListGates(ctx)
		if err != nil {
			return fmt.Errorf("fetching Statsig gates: %w", err)
		}
		log.Printf("Fetched %d Statsig gates", len(gates))
	case converter.ImportKindDynamicConfigs:
		log.Printf("Fetching Statsig dynamic configs...")
		configs, err = sgClient.ListDynamicConfigs(ctx)
		if err != nil {
			return fmt.Errorf("fetching Statsig dynamic configs: %w", err)
		}
		log.Printf("Fetched %d Statsig dynamic configs", len(configs))
	}

	// --- 5. Filter by tag ---
	if flagFIIncludeTag != "" {
		switch kind {
		case converter.ImportKindGates:
			before := len(gates)
			gates = statsig.FilterGatesByTag(gates, flagFIIncludeTag)
			log.Printf("Filtered %d → %d gates with tag %q", before, len(gates), flagFIIncludeTag)
		case converter.ImportKindDynamicConfigs:
			before := len(configs)
			configs = statsig.FilterDynamicConfigsByTag(configs, flagFIIncludeTag)
			log.Printf("Filtered %d → %d dynamic configs with tag %q", before, len(configs), flagFIIncludeTag)
		}
	}

	totalSource := len(gates) + len(configs)
	if totalSource == 0 {
		log.Printf("WARNING: Statsig returned 0 %s — verify your --statsig-key and Statsig project configuration", kind)
	}

	// --- 6. Convert (pure) ---
	var flagDrafts []converter.FlagResult
	switch kind {
	case converter.ImportKindGates:
		flagDrafts = converter.ConvertGates(gates, flagFITag, flagFIMaintainerID)
	case converter.ImportKindDynamicConfigs:
		flagDrafts = converter.ConvertDynamicConfigs(configs, flagFITag, flagFIMaintainerID)
	}

	// Pre-flight: warn about flag key collisions
	warnFlagKeyCollisions(flagDrafts)

	report := newFlagReport()
	report.Kind = string(kind)
	report.SourceTotal = totalSource

	// --- 7. Dry-run early exit ---
	if flagFIDryRun {
		for _, f := range flagDrafts {
			report.addEntry(flagReportEntry{
				FlagKey:    f.Flag.Key,
				FlagName:   f.Flag.Name,
				Status:     "dry_run",
				LDProject:  flagFILDProject,
				Variations: countVariations(f.Flag),
				Temporary:  f.Flag.Temporary,
			})
			if flagFIVerbose {
				log.Printf("DRY-RUN  %-40s  %d variations  temporary=%v", f.Flag.Key, countVariations(f.Flag), f.Flag.Temporary)
			}
		}
		report.finalize()
		if err := writeFlagReport(report); err != nil {
			return err
		}
		report.printSummary(os.Stdout)
		fmt.Printf("Report written to %s\n", flagFIOutput)
		return nil
	}

	// --- 8. Dedup against existing LD flags ---
	log.Printf("Listing existing LD flags in project %q to dedup...", flagFILDProject)
	existingFlags, err := ldClient.ListFlags(ctx)
	if err != nil {
		return fmt.Errorf("listing existing LD flags: %w", err)
	}
	existingByKey := make(map[string]struct{}, len(existingFlags))
	for _, f := range existingFlags {
		existingByKey[f.Key] = struct{}{}
	}
	log.Printf("Found %d existing LD flags", len(existingFlags))

	var toCreate []converter.FlagResult
	for _, f := range flagDrafts {
		if _, exists := existingByKey[f.Flag.Key]; exists {
			report.addEntry(flagReportEntry{
				FlagKey:   f.Flag.Key,
				FlagName:  f.Flag.Name,
				Status:    "skipped_existing",
				LDProject: flagFILDProject,
			})
			continue
		}
		toCreate = append(toCreate, f)
	}

	// --- 9. Build targeting plan (env reconcile + override fetch) ---
	var plan *converter.TargetingPlan
	if !flagFINoTargeting && len(toCreate) > 0 {
		log.Printf("Reconciling environments and fetching overrides...")
		// Pass the FILTERED gates/configs so we don't fetch overrides for
		// already-existing flags we won't patch.
		var planGates []statsig.Gate
		var planConfigs []statsig.DynamicConfig
		if kind == converter.ImportKindGates {
			toCreateByKey := keyedFlagSet(toCreate)
			for _, g := range gates {
				if _, want := toCreateByKey[converter.SanitizeFlagKey(g.ID)]; want {
					planGates = append(planGates, g)
				}
			}
		} else {
			toCreateByKey := keyedFlagSet(toCreate)
			for _, c := range configs {
				if _, want := toCreateByKey[converter.SanitizeFlagKey(c.ID)]; want {
					planConfigs = append(planConfigs, c)
				}
			}
		}
		plan, err = converter.BuildTargetingPlan(ctx, sgClient, ldClient, kind, flagFITag, planGates, planConfigs)
		if err != nil {
			return fmt.Errorf("building targeting plan: %w", err)
		}
		for _, f := range plan.Reconciler.Failures() {
			report.addNote(f)
		}
		log.Printf("Reconciled %d LD environment(s)", len(plan.Reconciler.AllReachableLDEnvKeys()))
	}

	// --- 10. Create flag shells ---
	log.Printf("Creating %d new flag(s)...", len(toCreate))
	var createdFlags []launchdarkly.Flag
	for i, f := range toCreate {
		progress := fmt.Sprintf("[%d/%d]", i+1, len(toCreate))
		created, createErr := ldClient.CreateFlag(ctx, f.Flag)
		if createErr != nil {
			if launchdarkly.IsFlagConflict(createErr) {
				report.addEntry(flagReportEntry{
					FlagKey:   f.Flag.Key,
					FlagName:  f.Flag.Name,
					Status:    "skipped_existing",
					LDProject: flagFILDProject,
				})
				if flagFIVerbose {
					log.Printf("%s EXIST  %-40s (race: appeared after list)", progress, f.Flag.Key)
				}
				continue
			}
			report.addEntry(flagReportEntry{
				FlagKey:   f.Flag.Key,
				FlagName:  f.Flag.Name,
				Status:    "failed",
				LDProject: flagFILDProject,
				Reason:    createErr.Error(),
			})
			if flagFIVerbose {
				log.Printf("%s FAIL   %-40s %s", progress, f.Flag.Key, createErr.Error())
			}
			continue
		}
		// LD's response may not include Variations; fall back to the draft we
		// submitted so downstream code (targeting patch) has them.
		responseFlag := *created
		if len(responseFlag.Variations) == 0 {
			responseFlag.Variations = f.Flag.Variations
		}
		createdFlags = append(createdFlags, responseFlag)
		report.addEntry(flagReportEntry{
			FlagKey:    f.Flag.Key,
			FlagName:   f.Flag.Name,
			Status:     "created",
			LDProject:  flagFILDProject,
			Variations: countVariations(f.Flag),
			Temporary:  f.Flag.Temporary,
		})
		if flagFIVerbose {
			log.Printf("%s OK     %-40s  %d variations", progress, f.Flag.Key, countVariations(f.Flag))
		}
	}

	// --- 11. Apply targeting ---
	if plan != nil && len(createdFlags) > 0 {
		log.Printf("Applying per-environment targeting to %d created flag(s)...", len(createdFlags))
		notes := converter.ApplyTargeting(ctx, ldClient, plan, createdFlags)
		for _, n := range notes {
			report.addNote(n)
		}
	}

	// --- 12. Report ---
	report.finalize()
	if err := writeFlagReport(report); err != nil {
		return err
	}
	report.printSummary(os.Stdout)
	fmt.Printf("Report written to %s\n", flagFIOutput)
	return nil
}

func parseImportKind(s string) (converter.ImportKind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "feature-gates", "gates", "gate":
		return converter.ImportKindGates, nil
	case "dynamic-configs", "dynamic-config", "configs", "dc":
		return converter.ImportKindDynamicConfigs, nil
	case "":
		return "", fmt.Errorf("--kind is required (feature-gates or dynamic-configs)")
	default:
		return "", fmt.Errorf("--kind must be \"feature-gates\" or \"dynamic-configs\" (got %q)", s)
	}
}

// keyedFlagSet returns a set of LD flag keys from a slice of FlagResult.
func keyedFlagSet(flags []converter.FlagResult) map[string]struct{} {
	out := make(map[string]struct{}, len(flags))
	for _, f := range flags {
		out[f.Flag.Key] = struct{}{}
	}
	return out
}

// countVariations returns the number of variations on a flag. Defensive
// against an empty Variations slice (which would mean the flag wouldn't be
// creatable, but for reporting we want a number not a panic).
func countVariations(f launchdarkly.Flag) int {
	return len(f.Variations)
}

// warnFlagKeyCollisions warns when two Statsig source items map to the same
// LD flag key after sanitization. Statsig IDs are plain strings; the sanitizer
// can collapse distinct IDs into the same LD key.
func warnFlagKeyCollisions(flags []converter.FlagResult) {
	seen := make(map[string]string, len(flags))
	for _, f := range flags {
		if first, exists := seen[f.Flag.Key]; exists {
			log.Printf("WARNING: flag key collision — %q and %q both sanitize to LD key %q. Only the first will be created.", first, f.Flag.Name, f.Flag.Key)
		} else {
			seen[f.Flag.Key] = f.Flag.Name
		}
	}
}

// --- Flag-import report (parallel to internal/report which is metric-scoped) ---

type flagReport struct {
	Timestamp   string                    `json:"timestamp"`
	Kind        string                    `json:"kind"`
	SourceTotal int                       `json:"source_total"`
	Created     int                       `json:"created"`
	Skipped     int                       `json:"skipped_existing"`
	Failed      int                       `json:"failed"`
	DryRun      int                       `json:"dry_run,omitempty"`
	Flags       []flagReportEntry         `json:"flags"`
	Notes       []launchdarkly.FailedFlag `json:"notes,omitempty"`
}

type flagReportEntry struct {
	FlagKey    string `json:"flag_key"`
	FlagName   string `json:"flag_name"`
	Status     string `json:"status"` // created | skipped_existing | failed | dry_run
	LDProject  string `json:"ld_project,omitempty"`
	Variations int    `json:"variations,omitempty"`
	Temporary  bool   `json:"temporary,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func newFlagReport() *flagReport {
	return &flagReport{Flags: []flagReportEntry{}}
}

func (r *flagReport) addEntry(e flagReportEntry) {
	r.Flags = append(r.Flags, e)
}

func (r *flagReport) addNote(n launchdarkly.FailedFlag) {
	r.Notes = append(r.Notes, n)
}

func (r *flagReport) finalize() {
	r.Timestamp = time.Now().UTC().Format(time.RFC3339)
	for _, f := range r.Flags {
		switch f.Status {
		case "created":
			r.Created++
		case "skipped_existing":
			r.Skipped++
		case "failed":
			r.Failed++
		case "dry_run":
			r.DryRun++
		}
	}
}

func (r *flagReport) printSummary(w *os.File) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flag Import Summary")
	fmt.Fprintln(w, "─────────────────────────────────────")
	fmt.Fprintf(w, "  Kind:                 %s\n", r.Kind)
	fmt.Fprintf(w, "  Source items:         %d\n", r.SourceTotal)
	if r.DryRun > 0 {
		fmt.Fprintf(w, "  Dry-run entries:      %d\n", r.DryRun)
	}
	if r.Created > 0 {
		fmt.Fprintf(w, "  Created in LD:        %d\n", r.Created)
	}
	if r.Skipped > 0 {
		fmt.Fprintf(w, "  Already existed:      %d\n", r.Skipped)
	}
	if r.Failed > 0 {
		fmt.Fprintf(w, "  Failed:               %d\n", r.Failed)
	}
	if len(r.Notes) > 0 {
		fmt.Fprintf(w, "  Targeting notes:      %d\n", len(r.Notes))
	}
	fmt.Fprintln(w, "─────────────────────────────────────")
}

func writeFlagReport(r *flagReport) error {
	var data []byte
	var err error
	if flagFIFormat == "csv" {
		var sb strings.Builder
		sb.WriteString("flag_key,flag_name,status,ld_project,variations,temporary,reason\n")
		for _, f := range r.Flags {
			sb.WriteString(fmt.Sprintf("%s,%s,%s,%s,%d,%v,%q\n",
				f.FlagKey, csvEscape(f.FlagName), f.Status, f.LDProject, f.Variations, f.Temporary, f.Reason))
		}
		data = []byte(sb.String())
	} else {
		data, err = json.MarshalIndent(r, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling flag report: %w", err)
		}
	}
	if err := os.WriteFile(flagFIOutput, data, 0600); err != nil {
		return fmt.Errorf("writing flag report to %s: %w", flagFIOutput, err)
	}
	return nil
}

func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

