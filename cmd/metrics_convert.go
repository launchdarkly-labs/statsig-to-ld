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
	Long: `Fetch Statsig metrics via the Console API, convert them to LaunchDarkly metric
definitions, and create them via the LD REST API.

Use --metric for one metric or --all for the whole project. Run --list first if
you need the names, and --dry-run to preview without creating anything.

API keys resolve in order: flag, then env var, then an interactive prompt. The
prompt is the safest for manual use, since input is hidden and never reaches
disk or shell history. Set LD_PROJECT to avoid repeating --ld-project.

Full flag reference, key handling, and the Warehouse Native workflow:
docs/cli-reference.md`,
	Example: `  # See what metrics exist (only the Statsig key is needed)
  statsig-to-ld metrics convert --list

  # Preview everything without creating anything
  statsig-to-ld metrics convert --all --dry-run

  # Convert one metric
  statsig-to-ld metrics convert --metric purchase_revenue --ld-project my-project

  # Convert everything, binding warehouse-native metrics to a data source
  statsig-to-ld metrics convert --all --ld-project my-project \
    --ld-data-source snowflake-ds

  # Dump raw Statsig JSON to debug a conversion (Statsig key only)
  statsig-to-ld metrics convert --dump-raw statsig-metrics-raw.json`,
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

	// Keep every usage string to one short line. Cobra pads the flag-name column,
	// so anything much past ~55 characters wraps in a normal terminal and the whole
	// block becomes hard to scan. Rationale and caveats belong in
	// docs/cli-reference.md, which the Long description points at.
	convertCmd.Flags().StringVar(&flagMetric, "metric", "", "Statsig metric name to convert")
	convertCmd.Flags().BoolVar(&flagAll, "all", false, "Convert all Statsig metrics")
	convertCmd.Flags().BoolVar(&flagList, "list", false, "List available Statsig metrics and exit")
	convertCmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Preview conversion without creating LD metrics")
	convertCmd.Flags().StringVar(&flagDumpRaw, "dump-raw", "", "Write raw Statsig JSON to this file, then continue")

	convertCmd.Flags().StringVar(&flagStatsigKey, "statsig-key", "", "Statsig Console API key (console-xxx)")
	convertCmd.Flags().StringVar(&flagStatsigURL, "statsig-url", "", "Statsig API base URL (include the scheme)")
	convertCmd.Flags().StringVar(&flagLDKey, "ld-key", "", "LaunchDarkly API access token (api-xxx)")
	convertCmd.Flags().StringVar(&flagLDURL, "ld-url", "", "LaunchDarkly API base URL (include the scheme)")
	convertCmd.Flags().StringVar(&flagLDProject, "ld-project", "", "LaunchDarkly project key (required)")

	convertCmd.Flags().StringVar(&flagLDDataSource, "ld-data-source", "", "LD data source for warehouse-native and ratio metrics")
	convertCmd.Flags().StringVar(&flagSourceMapping, "source-mapping", "", "JSON file mapping Statsig sources to LD data sources")
	convertCmd.Flags().StringVar(&flagUnitTypeMapping, "unit-type-mapping", "", "JSON file mapping unit types to LD context kinds")

	convertCmd.Flags().StringVar(&flagOutput, "output", "migration-report.json", "Migration report path")
	convertCmd.Flags().StringVar(&flagFormat, "format", "json", "Report format: json or csv")
	convertCmd.Flags().StringVar(&flagDefaultUnit, "default-unit", "", "Unit of measure for numeric metrics (default \"units\")")

	convertCmd.Flags().StringVar(&flagIncludeTags, "include-tags", "", "Only convert these Statsig tags (comma-separated)")
	convertCmd.Flags().StringVar(&flagIncludeTypes, "include-types", "", "Only convert these Statsig types (comma-separated)")
	convertCmd.Flags().IntVar(&flagConcurrency, "concurrency", 4, "Max concurrent LD API requests")
	convertCmd.Flags().BoolVarP(&flagVerbose, "verbose", "v", false, "Show detailed per-metric progress")
	convertCmd.Flags().BoolVar(&flagConvertLossy, "convert-lossy", false, "Convert lossy metrics instead of skipping them")
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

	// Warehouse-native metrics often omit unitTypes; their analysis unit lives on
	// the metric source. When any such metric is present, fetch the source
	// configs once and map each source to its declared unit types so the
	// converter resolves the real unit instead of defaulting to "user".
	// Best-effort: if the lookup fails, conversion proceeds with the default.
	if needsSourceUnitTypes(metrics) {
		log.Printf("Some warehouse-native metrics have no unitTypes; fetching metric sources to resolve their analysis unit...")
		sources, srcErr := sgClient.ListAllMetricSources(ctx)
		if srcErr != nil {
			log.Printf("WARNING: could not fetch Statsig metric sources (%v); warehouse-native metrics without unitTypes will default to the \"user\" analysis unit", srcErr)
		} else {
			convOpts.SourceUnitTypes = buildSourceUnitTypes(sources)
			log.Printf("Loaded analysis-unit id-type mappings for %d metric sources", len(convOpts.SourceUnitTypes))
		}
	}

	// Pre-flight: warn about potential key collisions
	warnKeyCollisions(metrics)

	// Convert and optionally create
	rpt := report.New()
	rpt.DryRun = flagDryRun
	total := len(metrics)
	var needsDataSource int64 // converted WHN/ratio metrics with no data source bound

	if total > 0 && !flagVerbose {
		fmt.Fprintf(os.Stderr, "Processing %d metrics [.=ok  S=incompatible  L=lossy  E=exists  X=fail]: ", total)
	}

	if flagDryRun || total <= 1 {
		// Sequential for dry-run, single metric, or empty
		for i, sg := range metrics {
			processMetric(ctx, sg, convOpts, ldClient, rpt, flagLDProject, flagDryRun, i+1, total, &needsDataSource)
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
				processMetric(ctx, sg, convOpts, ldClient, rpt, flagLDProject, false, n, total, &needsDataSource)
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

	// Warehouse-native and ratio metrics need a LaunchDarkly data source. Call
	// this out prominently: on a dry run the report shows them as converted, but
	// a real run rejects ratio metrics (HTTP 400) and creates the rest unbound.
	if n := atomic.LoadInt64(&needsDataSource); n > 0 {
		fmt.Printf("\n⚠  %d converted metric(s) resolved no LaunchDarkly data source.\n", n)
		fmt.Println("   Warehouse-native metrics need one to collect data, and ratio metrics are")
		fmt.Println("   rejected without it (HTTP 400). Bind them with --ld-data-source <key> or")
		fmt.Println("   --source-mapping <file> (see `warehouse` for creating the data source).")
	}

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

// needsSourceUnitTypes reports whether any metric is warehouse-native with no
// unitTypes of its own — the case where the analysis unit must be resolved from
// the metric source's id-type mapping.
func needsSourceUnitTypes(metrics []statsig.Metric) bool {
	for i := range metrics {
		if len(metrics[i].UnitTypes) == 0 && metrics[i].IsWarehouseNative() {
			return true
		}
	}
	return false
}

// buildSourceUnitTypes maps each metric source name to the Statsig unit IDs it
// declares (from its id-type mapping), for the converter's analysis-unit
// fallback. Sources with no name or no mapping are skipped.
func buildSourceUnitTypes(sources []statsig.MetricSourceConfig) map[string][]string {
	out := make(map[string][]string, len(sources))
	for _, s := range sources {
		var ids []string
		for _, m := range s.IDTypeMapping {
			if m.StatsigUnitID != "" {
				ids = append(ids, m.StatsigUnitID)
			}
		}
		if s.Name != "" && len(ids) > 0 {
			out[s.Name] = ids
		}
	}
	return out
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
	needsDataSourceCount *int64,
) {
	progress := fmt.Sprintf("[%d/%d]", current, total)

	// Record the effective type (a warehouse-native metric's aggregation, e.g.
	// "sum"/"percentile", rather than the "user_warehouse" wrapper) so the
	// report groups metrics by what actually determines the outcome.
	effType := sg.EffectiveType()

	result, convErr := converter.Convert(&sg, convOpts)
	if convErr != nil {
		if converter.IsIncompatible(convErr) {
			rpt.AddSkippedIncompatible(sg.Name, effType, sg.ID, convErr.Error())
			if flagVerbose {
				log.Printf("%s SKIP   %-45s  %s", progress, sg.Name, convErr.Error())
			} else {
				fmt.Fprint(os.Stderr, "S")
			}
			return
		}
		rpt.AddFailed(sg.Name, effType, sg.ID, convErr.Error())
		if flagVerbose {
			log.Printf("%s FAIL   %-45s  %s", progress, sg.Name, convErr.Error())
		} else {
			fmt.Fprint(os.Stderr, "X")
		}
		return
	}

	diag := buildDiagnostics(sg, result)

	// Lossy conversions (a Statsig feature dropped or approximated) are skipped
	// by default; --convert-lossy opts into the imperfect conversion.
	if result.IsLossy() && !flagConvertLossy {
		// Pass the FULL warning list, not just the lossy reasons: a skipped metric
		// is the one most likely to need triage, so it should keep its advisory
		// warnings too.
		rpt.AddSkippedLossy(sg.Name, effType, sg.ID, result.Warnings, diag)
		if flagVerbose {
			log.Printf("%s LOSSY  %-45s  skipped (lossy): %s", progress, sg.Name, strings.Join(result.LossyReasons, "; "))
		} else {
			fmt.Fprint(os.Stderr, "L")
		}
		return
	}

	// A converted warehouse-native or ratio metric with no data source resolved
	// still needs one bound in LaunchDarkly: ratio metrics are rejected without
	// it (HTTP 400), others are created but collect no data. Count it so the
	// summary can call it out (the dry-run report otherwise looks all-clear).
	needsDataSource := result.LDMetric.DataSource == nil &&
		(sg.IsWarehouseNative() || effType == "ratio")

	if dryRun {
		rpt.AddConverted(sg.Name, effType, sg.ID, result.LDMetric.Key, ldProject, result.Warnings, diag)
		if needsDataSource {
			atomic.AddInt64(needsDataSourceCount, 1)
		}
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
			rpt.AddSkippedExisting(sg.Name, effType, sg.ID, result.LDMetric.Key, ldProject)
			if flagVerbose {
				log.Printf("%s EXIST  %-45s  → %s (already exists)", progress, sg.Name, result.LDMetric.Key)
			} else {
				fmt.Fprint(os.Stderr, "E")
			}
			return
		}
		rpt.AddFailed(sg.Name, effType, sg.ID, createErr.Error())
		if flagVerbose {
			log.Printf("%s FAIL   %-45s  %s", progress, sg.Name, createErr.Error())
		} else {
			fmt.Fprint(os.Stderr, "X")
		}
		return
	}

	rpt.AddConverted(sg.Name, effType, sg.ID, result.LDMetric.Key, ldProject, result.Warnings, diag)
	if needsDataSource {
		atomic.AddInt64(needsDataSourceCount, 1)
	}
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

// buildDiagnostics collects the machine-readable fields for one metric's report
// entry. Kept in one place so the lossy-skip and converted paths cannot drift
// apart on what they record.
func buildDiagnostics(sg statsig.Metric, result *converter.Result) report.Diagnostics {
	diag := report.Diagnostics{
		WarningCodes:            result.WarningCodes,
		LossyReasons:            result.LossyReasons,
		LossyCodes:              result.LossyCodes,
		AnalysisUnits:           result.LDMetric.RandomizationUnits,
		StatsigRollupTimeWindow: sg.EffectiveRollupTimeWindow(),
		StatsigSourceName:       sg.NumeratorSourceName(),
	}
	if result.LDMetric.DataSource != nil {
		diag.LDDataSource = result.LDMetric.DataSource.Key
	}
	for _, f := range result.FilterOutcomes {
		diag.Filters = append(diag.Filters, report.FilterOutcome{
			Term:             f.Term,
			Criteria:         f.Criteria,
			Applied:          f.Applied,
			BlockedBy:        f.BlockedBy,
			BlockedCondition: f.BlockedCondition,
		})
	}
	return diag
}
