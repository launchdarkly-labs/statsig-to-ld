package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/converter"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/httputil"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/report"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
	"github.com/spf13/cobra"
)

var convertCmd = &cobra.Command{
	Use:   "convert",
	Short: "Convert Statsig metrics to LaunchDarkly format",
	Long: `Fetch Statsig metrics via the Console API, convert them to LaunchDarkly
metric definitions, and create them via the LD REST API.

Use --metric to convert a single metric by name, or --all to convert
every metric in the Statsig project. Not sure of the names? Run --list
first to print them.

API Key Security:
  Keys are resolved in order: flag → env var → interactive prompt.
  The interactive prompt is the most secure option for manual use — keys
  are entered with echo disabled and never touch disk, shell history, or
  process listings. For CI/CD, use flags or env vars injected from a
  secrets manager. --ld-project also reads LD_PROJECT from the environment.

Examples:
  # See the available metric names/types (only the Statsig key is needed)
  statsig-to-ld metrics convert --list

  # Export every metric's raw Statsig JSON for debugging (Statsig key only)
  statsig-to-ld metrics convert --dump-raw statsig-metrics-raw.json

  # Just run — the tool prompts for keys interactively (most secure)
  statsig-to-ld metrics convert --all --dry-run

  # Or set env vars for the session (use 'read -rs' to avoid shell history)
  read -rs STATSIG_CONSOLE_KEY && export STATSIG_CONSOLE_KEY
  read -rs LD_API_KEY && export LD_API_KEY
  export LD_PROJECT=my-project
  statsig-to-ld metrics convert --all

  # Convert a single metric
  statsig-to-ld metrics convert --metric purchase_revenue --ld-project my-project

  # Bulk convert all metrics with CSV output
  statsig-to-ld metrics convert --all --format csv --ld-project my-project

  # Convert only sum and mean metrics, with a default unit
  statsig-to-ld metrics convert --all --include-types sum,mean \
    --default-unit "$" --ld-project my-project

  # Bulk convert with Warehouse Native data source
  statsig-to-ld metrics convert --all --ld-project my-project \
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
	flagConvertLossy    bool
	flagList            bool
	flagDumpRaw         string
)

func init() {
	metricsCmd.AddCommand(convertCmd)

	convertCmd.Flags().StringVar(&flagMetric, "metric", "", "Statsig metric name to convert")
	convertCmd.Flags().BoolVar(&flagAll, "all", false, "Convert all Statsig metrics")
	convertCmd.Flags().BoolVar(&flagList, "list", false, "List the available Statsig metrics (name, type, id) and exit — no conversion, only the Statsig key needed")
	convertCmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Preview conversion without creating LD metrics")
	convertCmd.Flags().StringVar(&flagDumpRaw, "dump-raw", "", "Write every Statsig metric's raw JSON (verbatim, all fields) to this file, then continue — for debugging conversion, especially warehouse-native metrics. Needs only the Statsig key.")

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
	convertCmd.Flags().StringVar(&flagDefaultUnit, "default-unit", "", "Unit of measure for numeric metrics (e.g. \"$\", \"ms\", \"count\"). Defaults to \"units\" if unset.")

	convertCmd.Flags().StringVar(&flagIncludeTags, "include-tags", "", "Only convert metrics with these Statsig tags (comma-separated)")
	convertCmd.Flags().StringVar(&flagIncludeTypes, "include-types", "", "Only convert metrics of these Statsig types (comma-separated)")
	convertCmd.Flags().IntVar(&flagConcurrency, "concurrency", 4, "Max concurrent LD API requests for bulk conversion (kept low to stay under LaunchDarkly's rate limiter; raise if your project's limits allow)")
	convertCmd.Flags().BoolVarP(&flagVerbose, "verbose", "v", false, "Show detailed per-metric progress (status, name, key, errors)")
	convertCmd.Flags().BoolVar(&flagConvertLossy, "convert-lossy", false, "Convert metrics whose conversion is lossy (a Statsig feature is dropped or approximated). By default these are skipped as \"incompatible - lossy\".")
}

func runConvert(cmd *cobra.Command, args []string) error {
	// Propagate build version to httputil User-Agent
	httputil.SetVersion(version)

	// Resolve API keys and project from flag → env. The interactive key prompt
	// happens further down — AFTER the cheap offline validation below — so a
	// missing or mistyped flag fails fast instead of after you've already typed
	// two secret keys at a prompt. Flags/env are for CI/CD; the prompt (echo
	// disabled) is the most secure path for manual use.
	if flagStatsigKey == "" {
		flagStatsigKey = os.Getenv("STATSIG_CONSOLE_KEY")
	}
	if flagLDKey == "" {
		flagLDKey = os.Getenv("LD_API_KEY")
	}
	if flagLDProject == "" {
		flagLDProject = os.Getenv("LD_PROJECT")
	}

	// --- Offline validation first: no network, no interactive prompts. ---
	// willConvert: the run will actually convert metrics (vs. only listing or
	// dumping raw JSON). willCreate: it will also write them to LaunchDarkly.
	willConvert := flagAll || flagMetric != ""
	willCreate := willConvert && !flagDryRun

	if flagList && willConvert {
		return fmt.Errorf("--list cannot be combined with --all or --metric — it just lists the available metrics")
	}
	if flagAll && flagMetric != "" {
		return fmt.Errorf("--metric and --all are mutually exclusive")
	}
	if !willConvert && !flagList && flagDumpRaw == "" {
		return fmt.Errorf("either --metric <name> or --all is required (or --list to see the available metric names, or --dump-raw <file> to export raw definitions)")
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
	// --ld-project is only needed when we actually create metrics in LD.
	if flagLDProject == "" && willCreate {
		return fmt.Errorf("--ld-project is required — specify the LaunchDarkly project key to create metrics in (or set LD_PROJECT)")
	}

	// --- Statsig key: prompt only if not supplied by flag or env. ---
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

	// --- LD key: only needed when creating metrics (not for --dry-run, --list, or --dump-raw). ---
	if willCreate {
		if flagLDKey == "" {
			key, err := promptForKey("LaunchDarkly API access token (api-xxx)")
			if err != nil {
				return fmt.Errorf("reading LaunchDarkly key: %w", err)
			}
			flagLDKey = key
		}
		if flagLDKey == "" {
			return fmt.Errorf("LaunchDarkly API key is required (set LD_API_KEY env, use --ld-key, or enter at prompt)")
		}
	}
	if flagLDKey != "" && !strings.HasPrefix(flagLDKey, "api-") {
		return fmt.Errorf("LaunchDarkly key should start with \"api-\" — this is an API access token")
	}

	// Fix output extension to match format
	if flagFormat == "csv" && strings.HasSuffix(flagOutput, ".json") {
		flagOutput = strings.TrimSuffix(flagOutput, ".json") + ".csv"
	}

	ctx := cmd.Context()
	sgClient := statsig.NewClient(flagStatsigKey, flagStatsigURL)

	// --dump-raw: export the verbatim Statsig JSON (Statsig key only), then
	// continue with whatever else was requested.
	if flagDumpRaw != "" {
		if err := dumpRawMetrics(ctx, sgClient, flagDumpRaw); err != nil {
			return err
		}
	}

	// --list: print the available metrics and exit (Statsig key only).
	if flagList {
		return listMetrics(ctx, sgClient, os.Stdout)
	}

	// If the run was only a raw dump (no --all/--metric), there's nothing to convert.
	if !willConvert {
		return nil
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

	convOpts := converter.Options{
		LDDataSource:    flagLDDataSource,
		SourceMapping:   sourceMapping,
		DefaultUnit:     flagDefaultUnit,
		UnitTypeMapping: unitTypeMapping,
	}

	var ldClient *launchdarkly.Client
	if !flagDryRun {
		ldClient = launchdarkly.NewClient(flagLDKey, flagLDProject, flagLDURL)
	}

	if flagDryRun {
		fmt.Fprintln(os.Stderr, "DRY RUN — preview only, no metrics will be created in LaunchDarkly.")
	}

	// Fetch metrics
	var metrics []statsig.Metric
	var err error

	if flagAll {
		log.Printf("Fetching all Statsig metrics...")
		metrics, err = sgClient.ListAllMetrics(ctx)
		if err != nil {
			return annotateStatsigAuthErr(fmt.Errorf("fetching Statsig metrics: %w", err))
		}
		log.Printf("Fetched %d Statsig metrics", len(metrics))
	} else {
		log.Printf("Fetching Statsig metric %q...", flagMetric)
		m, err := sgClient.GetMetricByName(ctx, flagMetric)
		if err != nil {
			return annotateStatsigAuthErr(fmt.Errorf("fetching Statsig metric %q: %w", flagMetric, err))
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
	rpt.DryRun = flagDryRun
	total := len(metrics)

	if total > 0 && !flagVerbose {
		fmt.Fprintf(os.Stderr, "Processing %d metrics [.=ok  S=incompatible  L=lossy  E=exists  X=fail]: ", total)
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

	// Point the reader at where the per-metric detail lives, but only when
	// there's something worth opening the report for and they didn't already
	// ask for verbose per-metric output.
	if !flagVerbose && (rpt.Failed > 0 || rpt.ConvertedWithWarn > 0 || rpt.SkippedLossy > 0) {
		fmt.Printf("Per-metric warnings and reasons are in %s — or re-run with --verbose to see them inline.\n", flagOutput)
	}

	return nil
}

// listMetrics prints the available Statsig metrics (name, type, id) to w and
// returns. It's the discovery path for --metric: run this first to learn the
// exact names. Only the Statsig key is needed — no LaunchDarkly credentials.
func listMetrics(ctx context.Context, sgClient *statsig.Client, w io.Writer) error {
	log.Printf("Fetching all Statsig metrics...")
	metrics, err := sgClient.ListAllMetrics(ctx)
	if err != nil {
		return annotateStatsigAuthErr(fmt.Errorf("fetching Statsig metrics: %w", err))
	}
	if len(metrics) == 0 {
		log.Printf("WARNING: Statsig returned 0 metrics — verify your --statsig-key and Statsig project configuration")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tID")
	for _, m := range metrics {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", m.Name, m.Type, m.ID)
	}
	tw.Flush()
	fmt.Fprintf(w, "%d metrics\n", len(metrics))
	return nil
}

// dumpRawMetrics writes every Statsig metric's raw JSON to path, verbatim from
// the Console API (including fields the converter doesn't model). This is the
// artifact we ask a customer to capture when debugging warehouse-native
// conversion, since the tool's whole view of a metric comes from this response.
func dumpRawMetrics(ctx context.Context, sgClient *statsig.Client, path string) error {
	log.Printf("Fetching raw Statsig metric definitions for --dump-raw...")
	raw, err := sgClient.ListAllMetricsRaw(ctx)
	if err != nil {
		return annotateStatsigAuthErr(fmt.Errorf("fetching raw Statsig metrics: %w", err))
	}

	// Pretty-print the array of raw metric objects for readability.
	compact, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encoding raw metrics: %w", err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, compact, "", "  "); err != nil {
		pretty.Write(compact) // fall back to compact form if indenting fails
	}
	if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing raw dump to %s: %w", path, err)
	}

	log.Printf("Wrote %d raw Statsig metric definitions to %s", len(raw), path)
	fmt.Fprintf(os.Stderr, "Raw Statsig metric JSON written to %s — review and redact before sharing (it may contain warehouse table/column names).\n", path)
	return nil
}

// annotateStatsigAuthErr adds a plain-language hint when a Statsig request fails
// with an auth status, so a first-time user connects "HTTP 401" to "my key".
func annotateStatsigAuthErr(err error) error {
	if err == nil {
		return nil
	}
	if msg := err.Error(); strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403") {
		return fmt.Errorf("%w\n  hint: this looks like an authentication failure — check that your Statsig Console API key (console-…) is valid and active", err)
	}
	return err
}

// processMetric handles a single metric: convert → create → record in report.
// In verbose mode, prints detailed per-metric lines. In non-verbose mode,
// prints a single character per metric: . (ok), S (incompatible), L (lossy),
// E (already exists), X (fail).
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

	// Lossy conversions (a Statsig feature dropped or approximated) are skipped
	// by default; --convert-lossy opts into the imperfect conversion.
	if result.IsLossy() && !flagConvertLossy {
		rpt.AddSkippedLossy(sg.Name, sg.Type, sg.ID, result.LossyReasons)
		if flagVerbose {
			log.Printf("%s LOSSY  %-45s  skipped (lossy): %s", progress, sg.Name, strings.Join(result.LossyReasons, "; "))
		} else {
			fmt.Fprint(os.Stderr, "L")
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

	// Atomic write: write to a temp file in the same directory and rename
	// into place. Prevents readers from seeing a half-written report if the
	// process is killed mid-write — important when the report is the only
	// record of what was created in LaunchDarkly.
	if err := atomicWriteFile(flagOutput, data, 0600); err != nil {
		return fmt.Errorf("writing report to %s: %w", flagOutput, err)
	}

	return nil
}

// atomicWriteFile writes data to path via a sibling temp file and a rename,
// so concurrent readers and crash-during-write don't observe a truncated file.
// The temp file is removed on any error path before rename.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
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
