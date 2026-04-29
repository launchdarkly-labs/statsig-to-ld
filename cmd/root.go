package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/term"

	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/converter"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/httputil"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/report"
	"github.com/launchdarkly-labs/statsig-metric-importer-cli/internal/statsig"
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "statsig-metric-importer",
	Short:   "Convert Statsig metric definitions to LaunchDarkly metrics",
	Version: version,
	Long: `A CLI tool for migrating Statsig metric definitions into LaunchDarkly.

Supports both Statsig Cloud and Warehouse Native metrics. Produces a
migration report with per-metric status, warnings, and summary counts.

Re-running the tool is safe — existing LD metrics are detected and skipped.`,
}

var convertCmd = &cobra.Command{
	Use:   "convert",
	Short: "Convert Statsig metrics to LaunchDarkly format",
	Long: `Fetch Statsig metrics via the Console API, convert them to LaunchDarkly
metric definitions, and create them via the LD REST API.

Use --metric to convert a single metric by name, or --all to convert
every metric in the Statsig project.

API Key Security:
  Keys are resolved in order: flag → env var → interactive prompt.
  The interactive prompt is the most secure option for manual use — keys
  are entered with echo disabled and never touch disk, shell history, or
  process listings. For CI/CD, use flags or env vars injected from a
  secrets manager.

Examples:
  # Just run — the tool prompts for keys interactively (most secure)
  statsig-metric-importer convert --all --dry-run

  # Or set env vars for the session (use 'read -rs' to avoid shell history)
  read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY
  read -rs LD_API_KEY && export LD_API_KEY
  statsig-metric-importer convert --all --ld-project my-project

  # Convert a single metric
  statsig-metric-importer convert --metric purchase_revenue --ld-project my-project

  # Bulk convert all metrics with CSV output
  statsig-metric-importer convert --all --format csv --ld-project my-project

  # Convert only sum and mean metrics, with a default unit
  statsig-metric-importer convert --all --include-types sum,mean \
    --default-unit "$" --ld-project my-project

  # Bulk convert with Warehouse Native data source
  statsig-metric-importer convert --all --ld-project my-project \
    --ld-data-source snowflake-ds`,
	RunE: runConvert,
}

// Flags
var (
	flagMetric          string
	flagAll             bool
	flagDryRun          bool
	flagStatsigKey      string
	flagStatsigURL      string
	flagLDKey           string
	flagLDURL           string
	flagLDProject       string
	flagLDDataSource    string
	flagSourceMapping   string
	flagUnitTypeMapping string
	flagOutput          string
	flagFormat          string
	flagDefaultUnit     string
	flagIncludeTags     string
	flagIncludeTypes    string
	flagConcurrency     int
	flagVerbose         bool
)

func init() {
	// Disable default log timestamp — cleaner CLI output
	log.SetFlags(0)

	rootCmd.AddCommand(convertCmd)

	convertCmd.Flags().StringVar(&flagMetric, "metric", "", "Statsig metric name to convert")
	convertCmd.Flags().BoolVar(&flagAll, "all", false, "Convert all Statsig metrics")
	convertCmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Preview conversion without creating LD metrics")

	convertCmd.Flags().StringVar(&flagStatsigKey, "statsig-key", "", "Statsig Console API key (console-xxx)")
	convertCmd.Flags().StringVar(&flagStatsigURL, "statsig-url", "", "Statsig API base URL including scheme (e.g. https://statsigapi.net/console/v1)")
	convertCmd.Flags().StringVar(&flagLDKey, "ld-key", "", "LaunchDarkly API access token (api-xxx)")
	convertCmd.Flags().StringVar(&flagLDURL, "ld-url", "", "LaunchDarkly API base URL including scheme (e.g. https://app.launchdarkly.com/)")
	convertCmd.Flags().StringVar(&flagLDProject, "ld-project", "", "LaunchDarkly project key (required)")

	convertCmd.Flags().StringVar(&flagLDDataSource, "ld-data-source", "", "LD data source key for Warehouse Native metrics")
	convertCmd.Flags().StringVar(&flagSourceMapping, "source-mapping", "", "JSON file mapping Statsig source names to LD data source keys")
	convertCmd.Flags().StringVar(&flagUnitTypeMapping, "unit-type-mapping", "", "JSON file mapping Statsig unit types to LD context kinds (e.g. {\"companyID\": \"company\"})")

	convertCmd.Flags().StringVar(&flagOutput, "output", "migration-report.json", "Path for migration report output")
	convertCmd.Flags().StringVar(&flagFormat, "format", "json", "Report format: json or csv")
	convertCmd.Flags().StringVar(&flagDefaultUnit, "default-unit", "", "Unit of measure for numeric metrics (e.g. \"$\", \"ms\", \"count\")")

	convertCmd.Flags().StringVar(&flagIncludeTags, "include-tags", "", "Only convert metrics with these Statsig tags (comma-separated)")
	convertCmd.Flags().StringVar(&flagIncludeTypes, "include-types", "", "Only convert metrics of these Statsig types (comma-separated)")
	convertCmd.Flags().IntVar(&flagConcurrency, "concurrency", 10, "Max concurrent LD API requests for bulk conversion")
	convertCmd.Flags().BoolVarP(&flagVerbose, "verbose", "v", false, "Show detailed per-metric progress (status, name, key, errors)")
}

// ExecuteContext runs the root command with the given context.
func ExecuteContext(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
}

func runConvert(cmd *cobra.Command, args []string) error {
	// Propagate build version to httputil User-Agent
	httputil.SetVersion(version)

	// Resolve API keys: flag → env var → interactive prompt (with echo disabled).
	// The interactive prompt is the most secure path for manual use — the key
	// never touches disk, shell history, environment, or process listings.
	// Flags and env vars are supported for CI/CD where stdin is unavailable.
	if flagStatsigKey == "" {
		flagStatsigKey = os.Getenv("STATSIG_CONSOLE_KEY")
	}
	if flagLDKey == "" {
		flagLDKey = os.Getenv("LD_API_KEY")
	}

	// Validate flags
	if !flagAll && flagMetric == "" {
		return fmt.Errorf("either --metric <name> or --all is required")
	}
	if flagAll && flagMetric != "" {
		return fmt.Errorf("--metric and --all are mutually exclusive")
	}

	// Prompt for Statsig key if not provided via flag or env
	if flagStatsigKey == "" {
		key, err := promptForKey("Statsig Console API key (console-xxx)")
		if err != nil {
			return fmt.Errorf("reading Statsig key: %w", err)
		}
		flagStatsigKey = key
	}
	if flagStatsigKey == "" {
		return fmt.Errorf("Statsig Console API key is required (set STATSIG_CONSOLE_KEY env, use --statsig-key, or enter at prompt)")
	}
	if !strings.HasPrefix(flagStatsigKey, "console-") {
		return fmt.Errorf("Statsig key should start with \"console-\" — this is a Console API key, not a server secret key")
	}

	// Prompt for LD key if not provided via flag or env (skip for dry-run)
	if flagLDKey == "" && !flagDryRun {
		key, err := promptForKey("LaunchDarkly API access token (api-xxx)")
		if err != nil {
			return fmt.Errorf("reading LaunchDarkly key: %w", err)
		}
		flagLDKey = key
	}
	if flagLDKey == "" && !flagDryRun {
		return fmt.Errorf("LaunchDarkly API key is required (set LD_API_KEY env, use --ld-key, or enter at prompt)")
	}
	if flagLDKey != "" && !strings.HasPrefix(flagLDKey, "api-") {
		return fmt.Errorf("LaunchDarkly key should start with \"api-\" — this is an API access token")
	}
	if flagLDProject == "" && !flagDryRun {
		return fmt.Errorf("--ld-project is required — specify the LaunchDarkly project key to create metrics in")
	}
	if flagFormat != "json" && flagFormat != "csv" {
		return fmt.Errorf("--format must be \"json\" or \"csv\" (got %q)", flagFormat)
	}
	if flagConcurrency < 1 {
		return fmt.Errorf("--concurrency must be at least 1")
	}
	flagLDURL = strings.TrimRight(flagLDURL, "/")
	flagStatsigURL = strings.TrimRight(flagStatsigURL, "/")
	if flagLDURL != "" && !strings.HasPrefix(flagLDURL, "https://") && !strings.HasPrefix(flagLDURL, "http://") {
		return fmt.Errorf("--ld-url must include the scheme (e.g. https://%s)", flagLDURL)
	}
	if flagStatsigURL != "" && !strings.HasPrefix(flagStatsigURL, "https://") && !strings.HasPrefix(flagStatsigURL, "http://") {
		return fmt.Errorf("--statsig-url must include the scheme (e.g. https://%s)", flagStatsigURL)
	}

	// Fix output extension to match format
	if flagFormat == "csv" && strings.HasSuffix(flagOutput, ".json") {
		flagOutput = strings.TrimSuffix(flagOutput, ".json") + ".csv"
	}

	// Load source mapping file if provided
	var sourceMapping map[string]string
	if flagSourceMapping != "" {
		data, err := os.ReadFile(flagSourceMapping)
		if err != nil {
			return fmt.Errorf("reading source mapping file: %w", err)
		}
		if err := json.Unmarshal(data, &sourceMapping); err != nil {
			return fmt.Errorf("parsing source mapping file: %w", err)
		}
	}

	// Load unit type mapping file if provided
	var unitTypeMapping map[string]string
	if flagUnitTypeMapping != "" {
		data, err := os.ReadFile(flagUnitTypeMapping)
		if err != nil {
			return fmt.Errorf("reading unit type mapping file: %w", err)
		}
		if err := json.Unmarshal(data, &unitTypeMapping); err != nil {
			return fmt.Errorf("parsing unit type mapping file: %w", err)
		}
	}

	// Parse filter flags
	includeTags := parseCommaSeparated(flagIncludeTags)
	includeTypes := parseCommaSeparated(flagIncludeTypes)

	// Build converter options
	convOpts := converter.Options{
		LDDataSource:    flagLDDataSource,
		SourceMapping:   sourceMapping,
		DefaultUnit:     flagDefaultUnit,
		UnitTypeMapping: unitTypeMapping,
	}

	// Initialize clients
	ctx := cmd.Context()
	sgClient := statsig.NewClient(flagStatsigKey, flagStatsigURL)
	var ldClient *launchdarkly.Client
	if !flagDryRun {
		ldClient = launchdarkly.NewClient(flagLDKey, flagLDProject, flagLDURL)
	}

	// Fetch metrics
	var metrics []statsig.Metric
	var err error

	if flagAll {
		log.Printf("Fetching all Statsig metrics...")
		metrics, err = sgClient.ListAllMetrics(ctx)
		if err != nil {
			return fmt.Errorf("fetching Statsig metrics: %w", err)
		}
		log.Printf("Fetched %d Statsig metrics", len(metrics))
	} else {
		log.Printf("Fetching Statsig metric %q...", flagMetric)
		m, err := sgClient.GetMetricByName(ctx, flagMetric)
		if err != nil {
			return fmt.Errorf("fetching Statsig metric %q: %w", flagMetric, err)
		}
		metrics = []statsig.Metric{*m}
	}

	if len(metrics) == 0 {
		log.Printf("WARNING: Statsig returned 0 metrics — verify your --statsig-key and Statsig project configuration")
	}

	// Apply filters
	if len(includeTags) > 0 || len(includeTypes) > 0 {
		before := len(metrics)
		metrics = filterMetrics(metrics, includeTags, includeTypes)
		log.Printf("Filtered %d → %d metrics (tags=%v, types=%v)", before, len(metrics), includeTags, includeTypes)
	}

	// Pre-flight: warn about potential key collisions
	warnKeyCollisions(metrics)

	// Convert and optionally create
	rpt := report.New()
	total := len(metrics)

	if total > 0 && !flagVerbose {
		fmt.Fprintf(os.Stderr, "Processing %d metrics [.=ok S=skip X=fail E=exists]: ", total)
	}

	if flagDryRun || total <= 1 {
		// Sequential for dry-run, single metric, or empty
		for i, sg := range metrics {
			processMetric(ctx, sg, convOpts, ldClient, rpt, flagLDProject, flagDryRun, i+1, total)
		}
	} else {
		// Parallel for bulk non-dry-run
		var wg sync.WaitGroup
		sem := make(chan struct{}, flagConcurrency)
		var processed int64

	loop:
		for _, sg := range metrics {
			wg.Add(1)
			// Acquire semaphore with context check so Ctrl+C doesn't block
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Done()
				break loop
			}
			go func(sg statsig.Metric) {
				defer wg.Done()
				defer func() { <-sem }() // release
				n := int(atomic.AddInt64(&processed, 1))
				processMetric(ctx, sg, convOpts, ldClient, rpt, flagLDProject, false, n, total)
			}(sg)
		}
		wg.Wait()
	}

	// End the progress dot line if in non-verbose mode
	if total > 0 && !flagVerbose {
		fmt.Fprintln(os.Stderr)
	}

	// Finalize and write report
	rpt.Finalize(total)

	if err := writeReport(rpt); err != nil {
		return err
	}

	// Print summary table to stdout
	rpt.PrintSummaryTable(os.Stdout)
	fmt.Printf("Report written to %s\n", flagOutput)

	return nil
}

// processMetric handles a single metric: convert → create → record in report.
// In verbose mode, prints detailed per-metric lines. In non-verbose mode,
// prints a single character per metric: . (ok), S (skip), X (fail), E (exists).
func processMetric(
	ctx context.Context,
	sg statsig.Metric,
	convOpts converter.Options,
	ldClient *launchdarkly.Client,
	rpt *report.Report,
	ldProject string,
	dryRun bool,
	current, total int,
) {
	progress := fmt.Sprintf("[%d/%d]", current, total)

	result, convErr := converter.Convert(&sg, convOpts)
	if convErr != nil {
		if converter.IsIncompatible(convErr) {
			rpt.AddSkippedIncompatible(sg.Name, sg.Type, sg.ID, convErr.Error())
			if flagVerbose {
				log.Printf("%s SKIP   %-45s  %s", progress, sg.Name, convErr.Error())
			} else {
				fmt.Fprint(os.Stderr, "S")
			}
			return
		}
		rpt.AddFailed(sg.Name, sg.Type, sg.ID, convErr.Error())
		if flagVerbose {
			log.Printf("%s FAIL   %-45s  %s", progress, sg.Name, convErr.Error())
		} else {
			fmt.Fprint(os.Stderr, "X")
		}
		return
	}

	if dryRun {
		rpt.AddConverted(sg.Name, sg.Type, sg.ID, result.LDMetric.Key, ldProject, result.Warnings)
		if flagVerbose {
			status := "OK"
			if len(result.Warnings) > 0 {
				status = fmt.Sprintf("OK+%dw", len(result.Warnings))
			}
			log.Printf("%s %-7s %-45s  → %s", progress, status, sg.Name, result.LDMetric.Key)
		} else {
			fmt.Fprint(os.Stderr, ".")
		}
		return
	}

	_, createErr := ldClient.CreateMetric(ctx, result.LDMetric)
	if createErr != nil {
		if launchdarkly.IsConflict(createErr) {
			rpt.AddSkippedExisting(sg.Name, sg.Type, sg.ID, result.LDMetric.Key, ldProject)
			if flagVerbose {
				log.Printf("%s EXIST  %-45s  → %s (already exists)", progress, sg.Name, result.LDMetric.Key)
			} else {
				fmt.Fprint(os.Stderr, "E")
			}
			return
		}
		rpt.AddFailed(sg.Name, sg.Type, sg.ID, createErr.Error())
		if flagVerbose {
			log.Printf("%s FAIL   %-45s  %s", progress, sg.Name, createErr.Error())
		} else {
			fmt.Fprint(os.Stderr, "X")
		}
		return
	}

	rpt.AddConverted(sg.Name, sg.Type, sg.ID, result.LDMetric.Key, ldProject, result.Warnings)
	if flagVerbose {
		status := "OK"
		if len(result.Warnings) > 0 {
			status = fmt.Sprintf("OK+%dw", len(result.Warnings))
		}
		log.Printf("%s %-7s %-45s  → %s", progress, status, sg.Name, result.LDMetric.Key)
	} else {
		fmt.Fprint(os.Stderr, ".")
	}
}

func writeReport(rpt *report.Report) error {
	var data []byte
	var err error

	if flagFormat == "csv" {
		var buf strings.Builder
		if err := rpt.WriteCSV(&buf); err != nil {
			return fmt.Errorf("generating CSV report: %w", err)
		}
		data = []byte(buf.String())
	} else {
		data, err = json.MarshalIndent(rpt, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling JSON report: %w", err)
		}
	}

	if err := os.WriteFile(flagOutput, data, 0600); err != nil {
		return fmt.Errorf("writing report to %s: %w", flagOutput, err)
	}

	return nil
}

// warnKeyCollisions pre-computes sanitized LD keys for all metrics and warns
// about any collisions. Statsig uses plain-string metric IDs (name::type),
// and sanitization may collapse distinct IDs into the same LD key
// (e.g. "revenue (gross)::sum" and "revenue/gross::sum" both become
// "revenue-gross-sum").
func warnKeyCollisions(metrics []statsig.Metric) {
	seen := make(map[string]string, len(metrics)) // LD key → first Statsig ID
	for _, m := range metrics {
		ldKey := converter.SanitizeKey(m.ID)
		if ldKey == "" {
			continue
		}
		if firstID, exists := seen[ldKey]; exists {
			log.Printf("WARNING: key collision — Statsig metrics %q and %q both map to LD key %q. Only the first will be created; the second will be reported as already existing.",
				firstID, m.ID, ldKey)
		} else {
			seen[ldKey] = m.ID
		}
	}
}

func filterMetrics(metrics []statsig.Metric, tags, types []string) []statsig.Metric {
	tagSet := toSet(tags)
	typeSet := toSet(types)

	var filtered []statsig.Metric
	for _, m := range metrics {
		if len(typeSet) > 0 && !typeSet[m.Type] {
			continue
		}
		if len(tagSet) > 0 && !hasAnyTag(m.Tags, tagSet) {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered
}

func hasAnyTag(metricTags []string, wanted map[string]bool) bool {
	for _, t := range metricTags {
		if wanted[t] {
			return true
		}
	}
	return false
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

// promptForKey prompts the user to enter an API key with echo disabled,
// so the key does not appear in terminal output or scrollback.
func promptForKey(label string) (string, error) {
	if !term.IsTerminal(int(syscall.Stdin)) {
		// Non-interactive (piped input, CI) — cannot prompt
		return "", nil
	}
	fmt.Fprintf(os.Stderr, "Enter %s: ", label)
	keyBytes, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr) // newline after hidden input
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(keyBytes)), nil
}

func parseCommaSeparated(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
