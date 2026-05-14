package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/output"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/state"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/warehouse"
)

// Warehouse command flags
var (
	whFlagStatsigKey        string
	whFlagStatsigURL        string
	whFlagStatsigExportFile string
	whFlagLDKey             string
	whFlagLDURL             string
	whFlagLDProject         string
	whFlagLDEnvironment     string
	whFlagDryRun            bool
	whFlagResume            bool
	whFlagSkipWarehouse     bool
	whFlagOnly              string
	whFlagOverwrite         bool
	whFlagVerbose           bool
	whFlagNoColor           bool
)

var warehouseCmd = &cobra.Command{
	Use:   "warehouse",
	Short: "Migrate Statsig warehouse-native experimentation to LaunchDarkly",
	Long: `Set up warehouse connections, migrate data sources, and create
warehouse-native metrics in LaunchDarkly from a Statsig export.

This command runs a 3-phase migration:
  Phase 1: Export/load Statsig warehouse config, metric sources, and metrics
  Phase 2: Set up data export and experimentation integrations in LD
  Phase 3: Create metric data sources and metrics in LD

Examples:
  # Full migration from live Statsig API
  statsig-to-ld warehouse \
    --statsig-key console-XXX --ld-key api-XXX \
    --ld-project my-project --ld-environment production

  # Migration from a previously exported JSON file
  statsig-to-ld warehouse \
    --ld-key api-XXX --ld-project my-project --ld-environment production \
    --statsig-export-file statsig_export.json

  # Dry run (export only, no LD changes)
  statsig-to-ld warehouse \
    --statsig-key console-XXX --dry-run

  # Resume a failed migration
  statsig-to-ld warehouse \
    --ld-key api-XXX --ld-project my-project --ld-environment production \
    --statsig-export-file statsig_export.json --resume`,
	RunE: runWarehouse,
}

func init() {
	rootCmd.AddCommand(warehouseCmd)

	warehouseCmd.Flags().StringVar(&whFlagStatsigKey, "statsig-key", "", "Statsig Console API key (console-xxx)")
	warehouseCmd.Flags().StringVar(&whFlagStatsigURL, "statsig-url", "", "Statsig API base URL")
	warehouseCmd.Flags().StringVar(&whFlagStatsigExportFile, "statsig-export-file", "", "Load Statsig data from JSON export file instead of live API")
	warehouseCmd.Flags().StringVar(&whFlagLDKey, "ld-key", "", "LaunchDarkly API access token (api-xxx)")
	warehouseCmd.Flags().StringVar(&whFlagLDURL, "ld-url", "", "LaunchDarkly API base URL")
	warehouseCmd.Flags().StringVar(&whFlagLDProject, "ld-project", "", "LaunchDarkly project key (required)")
	warehouseCmd.Flags().StringVar(&whFlagLDEnvironment, "ld-environment", "", "LaunchDarkly environment key (required)")
	warehouseCmd.Flags().BoolVar(&whFlagDryRun, "dry-run", false, "Export and show mapping without writing to LD")
	warehouseCmd.Flags().BoolVar(&whFlagResume, "resume", false, "Resume from migration_state.json")
	warehouseCmd.Flags().BoolVar(&whFlagSkipWarehouse, "skip-warehouse", false, "Skip warehouse connection setup")
	warehouseCmd.Flags().StringVar(&whFlagOnly, "only", "", "Migrate only 'data-sources' or 'metrics'")
	warehouseCmd.Flags().BoolVar(&whFlagOverwrite, "overwrite", false, "Overwrite existing entities in LD")
	warehouseCmd.Flags().BoolVar(&whFlagVerbose, "verbose", false, "Show detailed API request/response info")
	warehouseCmd.Flags().BoolVar(&whFlagNoColor, "no-color", false, "Disable colored output")
}

// -- Report types --

type reportCounts struct {
	Created int `json:"created"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

type migrationReport struct {
	DataSources reportCounts `json:"data_sources"`
	Metrics     reportCounts `json:"metrics"`
	Warehouse   struct {
		Created bool `json:"created"`
		Skipped bool `json:"skipped"`
	} `json:"warehouse"`
	Warnings []string `json:"warnings"`
	Errors   []string `json:"errors"`
}

// -- Migration Engine --

type migrationEngine struct {
	sg     *statsig.Client
	ld     *launchdarkly.Client
	reader *bufio.Reader
	ctx    context.Context
	state  *state.MigrationState

	projectKey     string
	environmentKey string
	exportFile     string
	dryRun         bool
	skipWarehouse  bool
	only           string
	overwrite      bool
	verbose        bool

	whConnections map[string]any
	whPrefilled   map[string]string
	metricSources []map[string]any
	metrics       []map[string]any

	environmentID string
	projectID     string

	report migrationReport
}

func runWarehouse(cmd *cobra.Command, args []string) error {
	if whFlagNoColor {
		output.SetNoColor(true)
	}

	// Resolve API keys
	if whFlagStatsigKey == "" {
		whFlagStatsigKey = os.Getenv("STATSIG_CONSOLE_KEY")
	}
	if whFlagLDKey == "" {
		whFlagLDKey = os.Getenv("LD_API_KEY")
	}

	// Validate
	if whFlagStatsigExportFile == "" && whFlagStatsigKey == "" {
		return fmt.Errorf("either --statsig-key or --statsig-export-file is required")
	}
	if !whFlagDryRun {
		if whFlagLDKey == "" {
			return fmt.Errorf("--ld-key is required (or set LD_API_KEY)")
		}
		if whFlagLDProject == "" {
			return fmt.Errorf("--ld-project is required")
		}
		if whFlagLDEnvironment == "" {
			return fmt.Errorf("--ld-environment is required")
		}
	}

	var sg *statsig.Client
	if whFlagStatsigKey != "" {
		sg = statsig.NewClient(whFlagStatsigKey, whFlagStatsigURL)
	}

	ld := launchdarkly.NewClient(whFlagLDKey, whFlagLDProject, whFlagLDURL)
	ld.EnvironmentKey = whFlagLDEnvironment

	e := &migrationEngine{
		sg:             sg,
		ld:             ld,
		reader:         bufio.NewReader(cmd.InOrStdin()),
		ctx:            cmd.Context(),
		state:          state.NewMigrationState(whFlagResume),
		projectKey:     whFlagLDProject,
		environmentKey: whFlagLDEnvironment,
		exportFile:     whFlagStatsigExportFile,
		dryRun:         whFlagDryRun,
		skipWarehouse:  whFlagSkipWarehouse,
		only:           whFlagOnly,
		overwrite:      whFlagOverwrite,
		verbose:        whFlagVerbose,
		whPrefilled:    map[string]string{},
	}

	return e.run()
}

func (e *migrationEngine) run() error {
	output.Banner()

	// Phase 1
	if e.exportFile != "" {
		e.phase1LoadFromFile()
	} else {
		if err := e.phase1Export(); err != nil {
			return err
		}
	}

	if e.dryRun {
		e.printDryRunReport()
		return nil
	}

	// Pre-flight
	if err := e.preflightCheck(); err != nil {
		return err
	}

	// Phase 2
	if !e.skipWarehouse && e.only != "metrics" {
		if e.whConnections != nil {
			if err := e.phase2WarehouseSetup(); err != nil {
				return err
			}
		} else {
			output.Warn("No warehouse connection config — skipping warehouse setup")
		}
	}

	// Phase 3
	if e.only != "metrics" {
		e.phase3aMigrateDataSources()
	}
	if e.only != "data-sources" {
		e.phase3bMigrateMetrics()
	}

	e.printReport()
	e.saveReport()
	return nil
}

func (e *migrationEngine) preflightCheck() error {
	fmt.Fprintln(os.Stderr, "Verifying API key access...")

	isOK, msg := e.ld.CheckAPIKeyAccess(e.ctx)
	if !isOK {
		return fmt.Errorf("%s", msg)
	}
	output.Ok(msg)

	projData := e.ld.GetProject(e.ctx)
	if projData != nil {
		e.projectID = jsonutil.GetStr(projData, "_id")
		output.Ok(fmt.Sprintf("Project '%s' resolved (id=%s)", e.projectKey, e.projectID))
	} else {
		output.Warn(fmt.Sprintf("Could not resolve project '%s'", e.projectKey))
	}

	envData := e.ld.GetEnvironment(e.ctx)
	if envData != nil {
		e.environmentID = jsonutil.GetStr(envData, "_id")
		output.Ok(fmt.Sprintf("Environment '%s' resolved (id=%s)", e.environmentKey, e.environmentID))
	} else {
		output.Warn(fmt.Sprintf("Could not resolve environment '%s'", e.environmentKey))
	}

	role, name := e.ld.CheckAPIKeyRole(e.ctx)
	if role != "" {
		tokenDesc := "token"
		if name != "" {
			tokenDesc = fmt.Sprintf("\"%s\"", name)
		}
		if strings.EqualFold(role, "reader") {
			return fmt.Errorf("API key %s has role \"%s\" — read-only", tokenDesc, role)
		}
		output.Ok(fmt.Sprintf("API key %s has role \"%s\"", tokenDesc, role))
	}
	return nil
}

func (e *migrationEngine) phase1Export() error {
	output.Phase(1, "Exporting from Statsig...")

	fmt.Fprint(os.Stderr, "  Fetching warehouse connection config... ")
	e.whConnections = e.sg.GetWarehouseConnection(e.ctx)
	whConfig := map[string]string{}
	if e.whConnections != nil {
		whConfig = warehouse.ExtractFromWHConnections(e.whConnections)
		wt := whConfig["warehouse_type"]
		fmt.Fprintf(os.Stderr, "OK (type=%s)\n", wt)
	} else {
		fmt.Fprintln(os.Stderr, "not available")
	}

	fmt.Fprint(os.Stderr, "  Fetching metric sources... ")
	var err error
	e.metricSources, err = e.sg.ListMetricSources(e.ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "found %d metric sources\n", len(e.metricSources))

	// Fetch detailed metric source info
	detailed := make([]map[string]any, 0, len(e.metricSources))
	for _, src := range e.metricSources {
		name := jsonutil.GetStr(src, "name")
		if name != "" {
			detail, err := e.sg.GetMetricSource(e.ctx, name)
			if err != nil {
				detailed = append(detailed, src)
			} else {
				detailed = append(detailed, detail)
			}
		} else {
			detailed = append(detailed, src)
		}
	}
	e.metricSources = detailed

	sqlConfig := warehouse.ExtractFromSQLParsing(e.metricSources)
	e.whPrefilled = warehouse.MergeConfigs(sqlConfig, whConfig)

	fmt.Fprint(os.Stderr, "  Fetching metrics... ")
	e.metrics, err = e.sg.ListAllMetricsRaw(e.ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "found %d metrics\n", len(e.metrics))

	// Save export
	ts := time.Now().Format("2006-01-02_150405")
	exportPath := fmt.Sprintf("statsig_export_%s.json", ts)
	exportData := map[string]any{
		"exported_at":                time.Now().UTC().Format(time.RFC3339),
		"warehouse_connection":       e.whConnections,
		"warehouse_config_prefilled": e.whPrefilled,
		"metric_sources":             e.metricSources,
		"metrics":                    e.metrics,
	}
	raw, _ := json.MarshalIndent(exportData, "", "  ")
	_ = os.WriteFile(exportPath, raw, 0o644)
	output.Info(fmt.Sprintf("Saved export to %s", exportPath))
	return nil
}

func (e *migrationEngine) phase1LoadFromFile() {
	output.Phase(1, fmt.Sprintf("Loading from export file: %s", e.exportFile))

	raw, err := os.ReadFile(e.exportFile)
	if err != nil {
		output.ErrMsg(fmt.Sprintf("Failed to read export file: %v", err))
		return
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		output.ErrMsg(fmt.Sprintf("Failed to parse export file: %v", err))
		return
	}

	e.whConnections = jsonutil.GetMap(data, "warehouse_connection")
	e.metricSources = jsonutil.ExtractMapSlice(data, "metric_sources")
	e.metrics = jsonutil.ExtractMapSlice(data, "metrics")

	whConfig := map[string]string{}
	if e.whConnections != nil {
		whConfig = warehouse.ExtractFromWHConnections(e.whConnections)
	}
	sqlConfig := warehouse.ExtractFromSQLParsing(e.metricSources)
	e.whPrefilled = warehouse.MergeConfigs(sqlConfig, whConfig)

	output.Ok(fmt.Sprintf("Loaded %d metric sources", len(e.metricSources)))
	output.Ok(fmt.Sprintf("Loaded %d metrics", len(e.metrics)))
	if e.whConnections != nil {
		output.Ok("Loaded warehouse connection config")
	}
}

func (e *migrationEngine) phase2WarehouseSetup() error {
	output.Phase(2, "Setting up warehouse connection in LaunchDarkly...")

	if e.state.IsWarehouseDone() {
		output.Ok("Warehouse already set up (from previous run), skipping")
		e.report.Warehouse.Skipped = true
		return nil
	}

	detected := warehouse.DetectWarehouseType(e.whConnections, e.metricSources)

	e.whPrefilled["_env_id"] = e.environmentID
	e.whPrefilled["_project_id"] = e.projectID

	// Check data export first — no prompt needed
	exportExists := e.checkDataExportExists(detected)

	// Check experimentation — no prompt needed
	expExists := e.checkExperimentationExists(detected)

	if exportExists && expExists {
		e.report.Warehouse.Skipped = true
		e.state.SetWarehouseDone()
		return nil
	}

	// Only prompt for warehouse type if we actually need to create something
	whType := detected
	if whType == "" || (!exportExists && !expExists) {
		whType = warehouse.PromptWarehouseType(e.reader, detected)
	}

	if !exportExists {
		if err := e.phase2aDataExport(whType); err != nil {
			return err
		}
	}
	if !expExists {
		if err := e.phase2bExperimentation(whType); err != nil {
			return err
		}
	}

	e.state.SetWarehouseDone()
	return nil
}

// checkDataExportExists checks if a data export destination exists for the environment.
// It first checks via ListDestinations, then does a lightweight probe via the setup
// endpoint (which returns "already exists" when a destination is configured).
func (e *migrationEngine) checkDataExportExists(whType string) bool {
	output.Info("Checking data export destination...")

	// Try listing first
	dests := e.ld.ListDestinations(e.ctx)
	if len(dests) > 0 {
		kind := jsonutil.GetStr(dests[0], "kind")
		if kind == "" {
			kind = "unknown"
		}
		output.Ok(fmt.Sprintf("Data export destination exists (%s), skipping", kind))
		return true
	}

	// ListDestinations can return empty even when a destination exists.
	// Probe the setup endpoint with the known host — if it returns "already exists",
	// the destination is there.
	if whType != "" {
		exportKind := warehouse.DataExportTypes[whType]
		if exportKind != "" {
			host := e.whPrefilled["snowflake_host"]
			if host != "" && !strings.HasPrefix(host, "http") {
				host = "https://" + host
			}
			if host != "" {
				_, err := e.ld.GenerateDataExportSetup(e.ctx, exportKind, map[string]any{
					"snowflakeHostAddress": host,
				})
				if err != nil && strings.Contains(err.Error(), "already exists") {
					output.Ok("Data export destination exists, skipping")
					return true
				}
			}
		}
	}

	return false
}

// checkExperimentationExists checks all warehouse integration types for the environment.
// Returns true if an integration exists for the current environment.
func (e *migrationEngine) checkExperimentationExists(whType string) bool {
	output.Info("Checking experimentation integration...")
	for _, integrationKey := range warehouse.WarehouseTypes {
		for _, cfg := range e.ld.ListIntegrationConfigs(e.ctx, integrationKey) {
			cv := jsonutil.GetMap(cfg, "configValues")
			env := jsonutil.GetMap(cv, "selectedEnv")
			if jsonutil.GetStr(env, "environmentKey") == e.environmentKey {
				output.Ok(fmt.Sprintf("Experimentation integration exists for env '%s' (%s), skipping", e.environmentKey, integrationKey))
				return true
			}
		}
	}
	return false
}

func (e *migrationEngine) phase2aDataExport(whType string) error {
	output.Warn(fmt.Sprintf("No data export destination for env '%s'", e.environmentKey))
	output.Info("Setting up data export (prerequisite for native experimentation)...")

	var err error
	if whType == "snowflake" {
		err = warehouse.SetupDataExportSnowflake(e.ctx, e.reader, e.ld, e.whPrefilled)
	} else {
		err = warehouse.SetupDataExportGeneric(e.reader, whType)
	}
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			output.Warn("Data export destination already exists, continuing")
			return nil
		}
		return fmt.Errorf("data export setup failed: %w", err)
	}
	return nil
}

func (e *migrationEngine) phase2bExperimentation(whType string) error {
	output.Info("Setting up experimentation integration...")

	type setupFunc func(context.Context, *bufio.Reader, *launchdarkly.Client, string, string, map[string]string) error
	setupFuncs := map[string]setupFunc{
		"snowflake":  warehouse.SetupSnowflake,
		"bigquery":   warehouse.SetupBigQuery,
		"databricks": warehouse.SetupDatabricks,
		"redshift":   warehouse.SetupRedshift,
	}
	fn := setupFuncs[whType]
	if err := fn(e.ctx, e.reader, e.ld, e.projectKey, e.environmentKey, e.whPrefilled); err != nil {
		if strings.Contains(err.Error(), "already") {
			output.Warn("Experimentation integration already exists, continuing")
			e.report.Warehouse.Skipped = true
			return nil
		}
		return fmt.Errorf("warehouse setup failed: %w", err)
	}
	e.report.Warehouse.Created = true
	return nil
}

func (e *migrationEngine) getActiveIntegration() (string, string) {
	for _, integrationKey := range warehouse.WarehouseTypes {
		configs := e.ld.ListIntegrationConfigs(e.ctx, integrationKey)
		for _, cfg := range configs {
			cv := jsonutil.GetMap(cfg, "configValues")
			env := jsonutil.GetMap(cv, "selectedEnv")
			if jsonutil.GetStr(env, "projectKey") == e.projectKey &&
				jsonutil.GetStr(env, "environmentKey") == e.environmentKey {
				configID := jsonutil.GetStr(cfg, "_id")
				if configID == "" {
					configID = jsonutil.GetStr(cfg, "id")
				}
				return integrationKey, configID
			}
		}
	}
	for _, integrationKey := range warehouse.WarehouseTypes {
		configs := e.ld.ListIntegrationConfigs(e.ctx, integrationKey)
		if len(configs) > 0 {
			configID := jsonutil.GetStr(configs[0], "_id")
			if configID == "" {
				configID = jsonutil.GetStr(configs[0], "id")
			}
			return integrationKey, configID
		}
	}
	return "snowflake-experimentation", ""
}

func (e *migrationEngine) phase3aMigrateDataSources() {
	output.Phase(3, "Migrating metric data sources...")

	if len(e.metricSources) == 0 {
		output.Info("No metric sources to migrate.")
		return
	}

	integrationKey, integrationConfigID := e.getActiveIntegration()
	if integrationConfigID != "" {
		output.Info(fmt.Sprintf("Using integration config: %s (%s)", integrationKey, integrationConfigID))
	}

	existingKeys := map[string]bool{}
	if !e.overwrite {
		for _, ds := range e.ld.ListMetricDataSources(e.ctx) {
			existingKeys[jsonutil.GetStr(ds, "key")] = true
		}
	}

	total := len(e.metricSources)
	for i, source := range e.metricSources {
		body := warehouse.MapMetricSourceToDataSource(source, e.environmentKey, integrationKey)
		key := jsonutil.GetStr(body, "key")
		name := jsonutil.GetStr(body, "name")

		output.Progress(i+1, total, name, "")

		if e.state.IsDataSourceDone(key) {
			output.Skip("already migrated")
			e.report.DataSources.Skipped++
			continue
		}
		if existingKeys[key] && !e.overwrite {
			output.Skip("already exists in LD")
			e.state.MarkDataSourceDone(key)
			e.report.DataSources.Skipped++
			continue
		}

		// Use preview to get real columns from the warehouse
		if integrationConfigID != "" {
			previewSQL := warehouse.BuildPreviewSQL(source)
			if previewSQL != "" {
				preview, err := e.ld.PreviewDataSource(e.ctx, integrationConfigID, previewSQL)
				if err == nil {
					realColumns := warehouse.ExtractPreviewColumns(preview)
					if len(realColumns) > 0 {
						cm := body["columnMappings"].(map[string]any)
						cm["columns"] = realColumns
						warehouse.ReconcileColumnMappings(cm, preview, realColumns)
					}
				} else if e.verbose {
					output.Warn(fmt.Sprintf("Preview failed for %s: %v", name, err))
				}
			}
		}

		_, err := e.ld.CreateMetricDataSource(e.ctx, body)
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "409") || strings.Contains(errStr, "onflict") || strings.Contains(errStr, "uplicate") {
				output.Skip("already exists in LD")
				e.state.MarkDataSourceDone(key)
				e.report.DataSources.Skipped++
			} else {
				output.Fail(jsonutil.Truncate(errStr, 80))
				e.state.AddError("data_source", key, errStr)
				e.report.DataSources.Failed++
				e.report.Errors = append(e.report.Errors, fmt.Sprintf("Data source \"%s\": %v", name, err))
			}
		} else {
			output.Done()
			e.state.MarkDataSourceDone(key)
			e.report.DataSources.Created++
		}
	}
}

func (e *migrationEngine) phase3bMigrateMetrics() {
	fmt.Fprintln(os.Stderr)
	output.Info("Migrating metrics...")

	if len(e.metrics) == 0 {
		output.Info("No metrics to migrate.")
		return
	}

	dsKeyLookup := map[string]string{}
	for _, source := range e.metricSources {
		name := jsonutil.GetStr(source, "name")
		dsKeyLookup[name] = warehouse.SanitizeKey(name)
	}

	existingKeys := map[string]bool{}
	if !e.overwrite {
		for _, m := range e.ld.ListMetricsRaw(e.ctx) {
			existingKeys[jsonutil.GetStr(m, "key")] = true
		}
	}

	total := len(e.metrics)
	for i, statsigMetric := range e.metrics {
		metricType := strings.ToLower(jsonutil.GetStr(statsigMetric, "type"))
		if metricType == "" {
			metricType = "custom"
		}
		name := jsonutil.GetStr(statsigMetric, "name")
		if name == "" {
			name = fmt.Sprintf("metric-%d", i+1)
		}

		body, warning := warehouse.MapMetric(statsigMetric, dsKeyLookup)
		if warning != "" {
			e.report.Warnings = append(e.report.Warnings, warning)
		}

		if body == nil {
			output.Progress(i+1, total, name, metricType)
			output.Skip(fmt.Sprintf("unsupported type: %s", metricType))
			e.report.Metrics.Skipped++
			continue
		}

		key := jsonutil.GetStr(body, "key")
		output.Progress(i+1, total, name, metricType)

		if e.state.IsMetricDone(key) {
			output.Skip("already migrated")
			e.report.Metrics.Skipped++
			continue
		}
		if existingKeys[key] && !e.overwrite {
			output.Skip("already exists in LD")
			e.state.MarkMetricDone(key)
			e.report.Metrics.Skipped++
			continue
		}

		_, err := e.ld.CreateMetricRaw(e.ctx, body)
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "409") || strings.Contains(errStr, "onflict") || strings.Contains(errStr, "uplicate") {
				output.Skip("already exists in LD")
				e.state.MarkMetricDone(key)
				e.report.Metrics.Skipped++
				continue
			}
			output.Fail(jsonutil.Truncate(errStr, 80))
			e.state.AddError("metric", key, errStr)
			e.report.Metrics.Failed++
			e.report.Errors = append(e.report.Errors, fmt.Sprintf("Metric \"%s\": %v", name, err))
		} else {
			output.Done()
			e.state.MarkMetricDone(key)
			e.report.Metrics.Created++
		}
	}
}

func (e *migrationEngine) printDryRunReport() {
	fmt.Fprintf(os.Stderr, "\n%s\n", strings.Repeat("=", 60))
	fmt.Fprintln(os.Stderr, "  DRY RUN -- No changes were made to LaunchDarkly")
	fmt.Fprintf(os.Stderr, "%s\n\n", strings.Repeat("=", 60))

	detected := warehouse.DetectWarehouseType(e.whConnections, e.metricSources)
	if detected == "" {
		detected = "unknown"
	}
	fmt.Fprintf(os.Stderr, "  Detected warehouse type: %s\n", detected)

	fmt.Fprintf(os.Stderr, "\n  Metric data sources that would be created: %d\n", len(e.metricSources))
	for _, source := range e.metricSources {
		name := jsonutil.GetStr(source, "name")
		st := jsonutil.GetStr(source, "sourceType")
		if name == "" {
			name = "unnamed"
		}
		if st == "" {
			st = "query"
		}
		fmt.Fprintf(os.Stderr, "    - %s (%s)\n", name, st)
	}

	var supported, unsupported int
	for _, m := range e.metrics {
		mt := strings.ToLower(jsonutil.GetStr(m, "type"))
		if warehouse.UnsupportedMetricTypes[mt] {
			unsupported++
		} else {
			supported++
		}
	}
	fmt.Fprintf(os.Stderr, "\n  Metrics that would be created: %d\n", supported)
	if unsupported > 0 {
		fmt.Fprintf(os.Stderr, "  Metrics that would be SKIPPED (unsupported type): %d\n", unsupported)
	}
}

func (e *migrationEngine) printReport() {
	ds := e.report.DataSources
	mt := e.report.Metrics

	fmt.Fprintf(os.Stderr, "\n%s\n", strings.Repeat("=", 60))
	fmt.Fprintln(os.Stderr, "  Migration Complete")
	fmt.Fprintf(os.Stderr, "%s\n\n", strings.Repeat("=", 60))

	whStatus := "not attempted"
	if e.report.Warehouse.Created {
		whStatus = "created"
	} else if e.report.Warehouse.Skipped {
		whStatus = "skipped (already exists)"
	}
	fmt.Fprintf(os.Stderr, "  Warehouse Connection:  %s\n", whStatus)
	fmt.Fprintf(os.Stderr, "  Metric Data Sources:   %d created, %d skipped, %d failed\n", ds.Created, ds.Skipped, ds.Failed)
	fmt.Fprintf(os.Stderr, "  Metrics:               %d created, %d skipped, %d failed\n", mt.Created, mt.Skipped, mt.Failed)

	if len(e.report.Warnings) > 0 {
		fmt.Fprintf(os.Stderr, "\n  Warnings:\n")
		for _, w := range e.report.Warnings {
			fmt.Fprintf(os.Stderr, "    - %s\n", w)
		}
	}
	if len(e.report.Errors) > 0 {
		fmt.Fprintf(os.Stderr, "\n  Errors:\n")
		for _, er := range e.report.Errors {
			fmt.Fprintf(os.Stderr, "    - %s\n", er)
		}
	}
}

func (e *migrationEngine) saveReport() {
	ts := time.Now().Format("2006-01-02_150405")
	path := fmt.Sprintf("migration_report_%s.json", ts)
	raw, _ := json.MarshalIndent(e.report, "", "  ")
	_ = os.WriteFile(path, raw, 0o644)
	fmt.Fprintf(os.Stderr, "\n  Full report: %s\n", path)
}
