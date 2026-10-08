package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
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
	whFlagWarehouseType     string
	whFlagLDMaintainer      string
	whFlagDryRun            bool
	whFlagResume            bool
	whFlagOnly              string
	whFlagOverwrite         bool
	whFlagVerbose           bool
	whFlagNoColor           bool
	whFlagConstantEventKey  bool
	whFlagForceOverwrite    bool
	whFlagUpdateMappings    bool
)

var warehouseCmd = &cobra.Command{
	Use:   "warehouse",
	Short: "Set up LaunchDarkly warehouse integrations and metric data sources from Statsig",
	Long: `Set up LaunchDarkly warehouse integrations and metric data sources from a
Statsig warehouse-native project.

Does NOT migrate metric definitions. After this completes, run
'statsig-to-ld metrics convert --source-mapping source-mapping.json' to migrate
the warehouse-native metrics — the source-mapping.json file is written by this
command and binds each Statsig metric source to the LD data source key it just
created.

This command runs in three phases:
  Phase 1: Export warehouse config and metric sources from Statsig
  Phase 2: Set up data export + experimentation integrations in LD (interactive wizard)
  Phase 3: Create LD metric data sources

Examples:
  # Full flow from live Statsig API
  statsig-to-ld warehouse \
    --statsig-key console-XXX --ld-key api-XXX \
    --ld-project my-project --ld-environment production

  # From a previously exported JSON file
  statsig-to-ld warehouse \
    --ld-key api-XXX --ld-project my-project --ld-environment production \
    --statsig-export-file statsig_export.json

  # Dry run (no LD changes). With --ld-key and --ld-project it also reports
  # which data sources would be created, updated, skipped, or refused.
  statsig-to-ld warehouse \
    --statsig-key console-XXX --dry-run

  # Set up integrations only (skip data source creation)
  statsig-to-ld warehouse \
    --ld-key api-XXX --ld-project my-project --ld-environment production \
    --statsig-export-file statsig_export.json --only warehouse

  # Set up data sources only (skip integrations wizard — assumes integrations exist in LD)
  statsig-to-ld warehouse \
    --ld-key api-XXX --ld-project my-project --ld-environment production \
    --statsig-export-file statsig_export.json --only data-sources

  # Correct existing data sources' timestamp, value, and context mappings from the export
  statsig-to-ld warehouse \
    --ld-key api-XXX --ld-project my-project --ld-environment production \
    --statsig-export-file statsig_export.json --only data-sources --update-mappings

  # Resume a failed run
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
	warehouseCmd.Flags().StringVar(&whFlagWarehouseType, "warehouse-type", "", "Warehouse type: snowflake, bigquery, databricks, or redshift. Set this when Statsig does not expose its warehouse connection, rather than letting the command guess from SQL")
	warehouseCmd.Flags().StringVar(&whFlagLDMaintainer, "ld-maintainer", "", "Maintainer for created data sources: an email, a 24-character member ID, or \"none\" to create them with no maintainer. Defaults to the member who owns the API token")
	warehouseCmd.Flags().BoolVar(&whFlagDryRun, "dry-run", false, "Export and preview data source mapping without writing to LD")
	warehouseCmd.Flags().BoolVar(&whFlagResume, "resume", false, "Resume from migration_state.json")
	warehouseCmd.Flags().StringVar(&whFlagOnly, "only", "", "Run only 'warehouse' (Phase 2) or 'data-sources' (Phase 3)")
	warehouseCmd.Flags().BoolVar(&whFlagOverwrite, "overwrite", false, "Update existing LD metric data sources that lack the constant event key in place: their query, key column, and column list (timestamp, value, and context mappings are kept while the new query returns them). Refuses a data source whose bound metrics use other event keys, whose value column or context kind columns the new query drops while bound metrics use them, or, with --constant-event-key=false, whose key column the new query no longer returns")
	warehouseCmd.Flags().BoolVar(&whFlagForceOverwrite, "force-overwrite", false, "Like --overwrite, but also updates data sources whose bound metrics use other event keys and would match no rows afterwards; the run still lists those metrics")
	warehouseCmd.Flags().BoolVar(&whFlagUpdateMappings, "update-mappings", false, "Correct existing LD metric data sources' timestamp, value, and context mappings from the Statsig export")
	warehouseCmd.Flags().BoolVar(&whFlagVerbose, "verbose", false, "Show detailed API request/response info")
	warehouseCmd.Flags().BoolVar(&whFlagNoColor, "no-color", false, "Disable colored output")
	warehouseCmd.Flags().BoolVar(&whFlagConstantEventKey, "constant-event-key", true, "Wrap each source's SQL to add a constant event-key column (value = the data source key) and use it as the key column, so migrated metrics can use the data source key as their event key")
}

// -- Report types --

type reportCounts struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

type migrationReport struct {
	DataSources reportCounts `json:"data_sources"`
	Warehouse   struct {
		Created bool `json:"created"`
		Skipped bool `json:"skipped"`
	} `json:"warehouse"`
	Warnings []string `json:"warnings"`
	Errors   []string `json:"errors"`
	// Notes record per-source outcomes that need no action; none is printed individually.
	Notes []reportNote `json:"notes,omitempty"`
}

type reportNote struct {
	Code       string `json:"code"`
	DataSource string `json:"data_source"`
	// Changes lists changed fields as "field: old → new".
	Changes []string `json:"changes,omitempty"`
}

const (
	noteEditedConstantKey        = "constant_key_edited_in_launchdarkly"
	noteKeptConstantKey          = "constant_key_kept"
	noteExistsWithoutConstantKey = "exists_without_constant_key"
	noteValueColumnNotInQuery    = "statsig_value_column_not_in_query"
)

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
	only           string
	overwrite      bool
	maintainerID   string
	verbose        bool

	constantEventKey bool
	forceOverwrite   bool
	updateMappings   bool
	ldReadable       bool

	// The project's metrics, read once per run by projectMetrics.
	metrics     []map[string]any
	metricsErr  error
	metricsRead bool

	// unmapped holds keys taken in another environment, left out of source-mapping.json.
	unmapped map[string]bool
	// issues are printed once per code at the end of the phase.
	issues []sourceIssue

	whConnections map[string]any
	whPrefilled   map[string]string
	metricSources []map[string]any

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
	// Check the warehouse type up front. Only some paths read it, so validating
	// it where it is used would let a typo through on the others.
	if err := warehouse.ValidateWarehouseType(whFlagWarehouseType); err != nil {
		return err
	}
	if err := launchdarkly.ValidateMaintainerFlag(whFlagLDMaintainer); err != nil {
		return err
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
		only:           whFlagOnly,
		overwrite:      whFlagOverwrite || whFlagForceOverwrite,
		verbose:        whFlagVerbose,
		whPrefilled:    map[string]string{},

		constantEventKey: whFlagConstantEventKey,
		forceOverwrite:   whFlagForceOverwrite,
		updateMappings:   whFlagUpdateMappings,
		ldReadable:       whFlagLDKey != "" && whFlagLDProject != "",
	}

	return e.run()
}

func (e *migrationEngine) run() error {
	output.Banner()

	switch e.only {
	case "", "warehouse", "data-sources":
		// valid
	default:
		return fmt.Errorf("--only must be 'warehouse' or 'data-sources' (got %q)", e.only)
	}

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
		if e.only != "warehouse" {
			if err := e.writeSourceMapping(); err != nil {
				output.Warn(fmt.Sprintf("Could not write source-mapping.json: %v", err))
			}
		}
		return nil
	}

	// Pre-flight
	if err := e.preflightCheck(); err != nil {
		return err
	}

	// Fatal on failure: LD flags an unmaintained data source as incomplete, so
	// stopping beats creating a hundred of them.
	maintainer, err := e.ld.ResolveMaintainer(e.ctx, whFlagLDMaintainer)
	if err != nil {
		return err
	}
	e.maintainerID = maintainer.MemberID
	logMaintainer(maintainer, "data source")

	// Phase 2 — set up warehouse integrations
	if e.only != "data-sources" {
		if e.whConnections != nil {
			if err := e.phase2WarehouseSetup(); err != nil {
				return err
			}
		} else {
			output.Warn("No warehouse connection config — skipping warehouse setup")
		}
	}

	// Phase 3 — create LD metric data sources
	if e.only != "warehouse" {
		if err := e.phase3aMigrateDataSources(); err != nil {
			return err
		}
	}

	e.printReport()
	e.saveReport()
	if e.only != "warehouse" {
		if err := e.writeSourceMapping(); err != nil {
			output.Warn(fmt.Sprintf("Could not write source-mapping.json: %v", err))
		}
		e.printHandoff()
	}
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

	// Save export
	ts := time.Now().Format("2006-01-02_150405")
	exportPath := fmt.Sprintf("statsig_export_%s.json", ts)
	exportData := map[string]any{
		"exported_at":                time.Now().UTC().Format(time.RFC3339),
		"warehouse_connection":       e.whConnections,
		"warehouse_config_prefilled": e.whPrefilled,
		"metric_sources":             e.metricSources,
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

	whConfig := map[string]string{}
	if e.whConnections != nil {
		whConfig = warehouse.ExtractFromWHConnections(e.whConnections)
	}
	sqlConfig := warehouse.ExtractFromSQLParsing(e.metricSources)
	e.whPrefilled = warehouse.MergeConfigs(sqlConfig, whConfig)

	output.Ok(fmt.Sprintf("Loaded %d metric sources", len(e.metricSources)))
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

	detected, typeSource, err := warehouse.ResolveWarehouseType(whFlagWarehouseType, e.whConnections, e.metricSources)
	if err != nil {
		return err
	}

	e.whPrefilled["_env_id"] = e.environmentID
	e.whPrefilled["_project_id"] = e.projectID

	// These run before the type is confirmed so an already-configured project is
	// not prompted at all. Safe today because neither check can act on a wrong
	// guess: checkExperimentationExists scans every warehouse type regardless of
	// this argument, and checkDataExportExists only uses it for a fallback probe
	// gated on a host that is populated from the Statsig connection config, which
	// is precisely the case where the type is already confident. If either check
	// starts genuinely depending on the type, confirm it before this point.
	exportExists := e.checkDataExportExists(detected)
	expExists := e.checkExperimentationExists(detected)

	if exportExists && expExists {
		e.report.Warehouse.Skipped = true
		e.state.SetWarehouseDone()
		return nil
	}

	// The warehouse type picks the LaunchDarkly integration key, so creating
	// anything on an unconfirmed guess would bind every data source to the wrong
	// warehouse. Confirm it first unless it came from the flag or from Statsig's
	// own connection config.
	whType := detected
	if !typeSource.IsConfident() {
		whType, err = warehouse.PromptWarehouseType(e.reader, detected)
		if err != nil {
			return err
		}
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
			// Match project AND environment: ListIntegrationConfigs is
			// account-wide, so environmentKey alone would false-match another
			// project's same-named env. Mirrors getActiveIntegration.
			if jsonutil.GetStr(env, "projectKey") == e.projectKey &&
				jsonutil.GetStr(env, "environmentKey") == e.environmentKey {
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

func (e *migrationEngine) phase3aMigrateDataSources() error {
	output.Phase(3, "Migrating metric data sources...")

	if len(e.metricSources) == 0 {
		output.Info("No metric sources to migrate.")
		return nil
	}

	integrationKey, integrationConfigID := e.getActiveIntegration()
	if integrationConfigID != "" {
		output.Info(fmt.Sprintf("Using integration config: %s (%s)", integrationKey, integrationConfigID))
	}

	whType := warehouse.WarehouseTypeForIntegration(integrationKey)

	// A failed read must stop the phase, not read as "nothing exists yet".
	list, err := e.ld.ListMetricDataSources(e.ctx)
	if err != nil {
		return fmt.Errorf("could not read the project's existing LaunchDarkly metric data sources, so cannot tell which to create: %w", err)
	}
	existing := indexDataSources(list)
	collisions := keyCollisions(e.metricSources)

	total := len(e.metricSources)
	withoutConstantKey := 0
	var droppedValue []string
	for i, source := range e.metricSources {
		body := warehouse.MapMetricSourceToDataSource(source, e.environmentKey, integrationKey, e.maintainerID)
		key := jsonutil.GetStr(body, "key")
		name := jsonutil.GetStr(body, "name")

		output.Progress(i+1, total, name, "")

		// Planned before the preview so a source that is not written costs no warehouse query.
		plan := e.planSource(source, body, existing, collisions, whType)
		e.recordPlan(key, plan)
		if plan.note == noteExistsWithoutConstantKey {
			withoutConstantKey++
		}
		switch plan.action {
		case planSkip:
			output.Skip(plan.reason)
			if plan.warning != "" {
				e.warn(plan.warning)
			}
			if plan.mappings != nil {
				e.recordIssues(plan.mappings.issues)
			}
			if plan.markDone && !e.state.IsDataSourceDone(key) {
				e.state.MarkDataSourceDone(key)
			}
			e.report.DataSources.Skipped++
			continue
		case planRefuse:
			e.failDataSource(key, name, plan.err)
			if plan.warning != "" {
				e.warn(plan.warning)
			}
			continue
		case planUpdateMappings:
			e.patchMappings(existing[key], name, plan)
			if plan.warning != "" {
				e.warn(plan.warning)
			}
			continue
		}

		statsigValue := jsonutil.GetStr(jsonutil.GetMap(body, "columnMappings"), "valueColumn")

		// Use preview to get real columns from the warehouse
		var sourceColumns []string
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
						for _, c := range realColumns {
							sourceColumns = append(sourceColumns, jsonutil.GetStr(c, "name"))
						}
					}
				} else if e.verbose {
					output.Warn(fmt.Sprintf("Preview failed for %s: %v", name, err))
				}
			}
		}

		if e.constantEventKey {
			if err := e.applyConstantEventKey(body, whType, integrationConfigID, sourceColumns); err != nil {
				e.failDataSource(key, name, err)
				continue
			}
		}

		if statsigValue != "" && jsonutil.GetStr(jsonutil.GetMap(body, "columnMappings"), "valueColumn") == "" {
			droppedValue = append(droppedValue, key)
			e.report.Notes = append(e.report.Notes, reportNote{Code: noteValueColumnNotInQuery, DataSource: key})
		}

		if plan.action == planUpdate {
			e.updateDataSource(existing[key], body, name, plan)
			continue
		}

		_, err := e.ld.CreateMetricDataSource(e.ctx, body)
		if err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "409") || strings.Contains(errStr, "onflict") || strings.Contains(errStr, "uplicate") {
				output.Skip("already exists in LD")
				e.state.MarkDataSourceDone(key)
				e.report.DataSources.Skipped++
			} else {
				e.failDataSource(key, name, err)
			}
		} else {
			output.Done()
			e.state.MarkDataSourceDone(key)
			e.report.DataSources.Created++
		}
	}
	if msg := withoutConstantKeySummary(withoutConstantKey); msg != "" {
		e.warn(msg)
	}
	if len(droppedValue) > 0 {
		e.warn(fmt.Sprintf("%d data source(s) did not get the value column their Statsig source defines, because their query does not return it: %s. Add the column to the Statsig source's query and to the data source's in LaunchDarkly, or set a value column on their numeric metrics.",
			len(droppedValue), strings.Join(droppedValue, ", ")))
	}
	for _, msg := range issueSummaries(e.issues) {
		e.warn(msg)
	}
	return nil
}

func (e *migrationEngine) recordPlan(key string, plan dataSourcePlan) {
	if plan.note != "" {
		e.report.Notes = append(e.report.Notes, reportNote{Code: plan.note, DataSource: key})
	}
	if plan.unmapped {
		if e.unmapped == nil {
			e.unmapped = map[string]bool{}
		}
		e.unmapped[key] = true
	}
}

func withoutConstantKeySummary(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d data source(s) already exist without the constant event key, so metrics converted against them keep Statsig event keys, which usually match no rows. To update them, rerun with --overwrite (it refuses any whose bound metrics use other event keys); leave any built by hand whose key column holds real event names.", n)
}

func indexDataSources(list []map[string]any) map[string]map[string]any {
	existing := map[string]map[string]any{}
	for _, ds := range list {
		existing[jsonutil.GetStr(ds, "key")] = ds
	}
	return existing
}

// keyCollisions returns the LD keys shared by more than one Statsig source, with their names.
func keyCollisions(sources []map[string]any) map[string][]string {
	byKey := map[string][]string{}
	for _, source := range sources {
		name := jsonutil.GetStr(source, "name")
		key := jsonutil.GetStr(warehouse.MapMetricSourceToDataSource(source, "", "", ""), "key")
		byKey[key] = append(byKey[key], name)
	}
	for key, names := range byKey {
		if len(names) < 2 {
			delete(byKey, key)
		}
	}
	return byKey
}

type planAction int

const (
	planCreate planAction = iota
	planUpdate
	planSkip
	planRefuse
	// planUpdateMappings changes only mappings (--update-mappings).
	planUpdateMappings
)

// dataSourcePlan is decided before anything is written; the dry run reports the same plans.
type dataSourcePlan struct {
	action   planAction
	reason   string
	warning  string
	note     string
	err      error
	markDone bool
	// unmapped leaves the source out of source-mapping.json.
	unmapped bool
	// stale names bound metrics left matching no rows; set only with --force-overwrite.
	stale []string
	// mappings is set by --update-mappings.
	mappings *mappingPlan
}

// refuse turns p into a refusal, keeping its note and warning.
func (p dataSourcePlan) refuse(reason string, err error) dataSourcePlan {
	p.action, p.reason, p.err = planRefuse, reason, err
	p.markDone, p.stale, p.mappings = false, nil, nil
	return p
}

// planSource plans one Statsig source; the run and the dry run share it.
func (e *migrationEngine) planSource(source, body map[string]any, existing map[string]map[string]any, collisions map[string][]string, whType string) dataSourcePlan {
	plan := e.planDataSource(body, existing, collisions, whType)
	ds := existing[jsonutil.GetStr(body, "key")]
	takesExport := plan.action == planCreate || plan.action == planUpdate || (e.updateMappings && ds != nil && plan.action == planSkip)
	if conflicts := warehouse.ContextKindConflicts(source); takesExport && len(conflicts) > 0 {
		var kinds []string
		for _, c := range conflicts {
			kinds = append(kinds, c.Kind)
		}
		return plan.refuse("Statsig unit IDs share context kind(s) "+quoteAll(kinds), kindConflictError(conflicts, ds != nil))
	}
	return e.planMappings(plan, ds, source)
}

func kindConflictError(conflicts []warehouse.KindConflict, exists bool) error {
	var parts []string
	for _, c := range conflicts {
		parts = append(parts, fmt.Sprintf("Statsig unit IDs %s all map to context kind %q, with different columns (%s)", quoteAll(c.Units), c.Kind, quoteAll(c.Columns)))
	}
	if exists {
		return fmt.Errorf("not updated: %s. Keep one of them in the Statsig source's id-type mapping and rerun, or correct the data source's contexts in LaunchDarkly by hand", strings.Join(parts, "; "))
	}
	return fmt.Errorf("not created: %s. Keep one of them in the Statsig source's id-type mapping and rerun, or create the data source by hand", strings.Join(parts, "; "))
}

func (e *migrationEngine) planDataSource(body map[string]any, existing map[string]map[string]any, collisions map[string][]string, whType string) dataSourcePlan {
	key := jsonutil.GetStr(body, "key")
	if names, ok := collisions[key]; ok {
		return dataSourcePlan{
			action: planRefuse,
			reason: fmt.Sprintf("key collision: %s", quoteAll(names)),
			err: fmt.Errorf("Statsig sources %s all map to the LaunchDarkly data source key %q, so none of them was created, updated, or written to source-mapping.json. Rename all but one in Statsig, or create their data sources by hand with distinct keys and bind them with metrics convert --source-mapping",
				quoteAll(names), key),
		}
	}

	ds, exists := existing[key]
	if !exists {
		if e.state.IsDataSourceDone(key) {
			return dataSourcePlan{action: planSkip, reason: "already migrated"}
		}
		return dataSourcePlan{action: planCreate}
	}

	// Data source keys are unique per project, not per environment; one in another
	// environment is neither updated nor bound to this environment's metrics.
	if env := jsonutil.GetStr(ds, "environmentKey"); env != "" && env != e.environmentKey {
		return dataSourcePlan{
			action:   planRefuse,
			reason:   fmt.Sprintf("key used in environment %q", env),
			unmapped: true,
			err: fmt.Errorf("not created: the key %q is taken by a data source in LaunchDarkly environment %q, which was left unchanged, and this source was left out of source-mapping.json. Rename the source in Statsig, or run with --ld-environment %s",
				key, env, env),
		}
	}

	wrapper, state := existingConstantKey(ds, whType)
	switch {
	case state == warehouse.ConstantKeyEdited:
		// Someone's edit that still projects the constant: never rewritten, whatever the flags.
		return dataSourcePlan{action: planSkip, reason: "kept: edited in LaunchDarkly", note: noteEditedConstantKey, markDone: true}
	case state == warehouse.ConstantKeyWrapped && e.constantEventKey:
		// Never rewritten, even with --overwrite: a PATCH could only drop mappings edited since.
		plan := dataSourcePlan{action: planSkip, reason: "already has the constant event key", markDone: true}
		if inner, err := warehouse.PrepareSourceSQL(jsonutil.GetStr(body, "sqlQuery"), whType); err == nil && !warehouse.SameSQL(inner, wrapper.Inner) {
			plan.warning = fmt.Sprintf("Data source %q kept its query, but its Statsig SQL has changed since it was created; edit its SQL in LaunchDarkly if the change matters.", key)
		}
		return plan
	case state == warehouse.ConstantKeyWrapped:
		plan := dataSourcePlan{action: planSkip, reason: "already exists in LD", markDone: true}
		if e.overwrite {
			plan.reason = "kept: has the constant event key, which its metrics need"
			plan.note = noteKeptConstantKey
		}
		return plan
	case !e.overwrite:
		plan := dataSourcePlan{action: planSkip, reason: "already exists in LD", markDone: true}
		if e.constantEventKey {
			plan.reason = "exists without the constant event key; --overwrite can update it"
			plan.note = noteExistsWithoutConstantKey
		}
		return plan
	case !e.constantEventKey:
		if e.state.IsDataSourceDone(key) {
			return dataSourcePlan{action: planSkip, reason: "already migrated"}
		}
		return dataSourcePlan{action: planUpdate}
	}

	// After the update the key column holds only the data source key, so check bound metrics first.
	stale, err := e.staleBoundMetrics(key, key)
	if err != nil {
		return dataSourcePlan{action: planRefuse, reason: "could not check bound metrics", err: boundMetricsUnchecked(err)}
	}
	if len(stale) > 0 && !e.forceOverwrite {
		return dataSourcePlan{
			action: planRefuse,
			reason: "bound metrics: " + strings.Join(stale, ", "),
			err: fmt.Errorf("not updated: %d metric(s) bound to it use an event key other than %q and would match no rows after the update: %s. Metrics created by older versions of this tool usually match no rows already. Rerun with --force-overwrite to update it anyway, then set their event keys to %q in LaunchDarkly",
				len(stale), key, strings.Join(stale, ", "), key),
		}
	}
	return dataSourcePlan{action: planUpdate, stale: stale}
}

// existingConstantKey classifies ds's query by its own warehouse type, else whType.
func existingConstantKey(ds map[string]any, whType string) (warehouse.ConstantKeyWrapper, warehouse.ConstantKeyState) {
	if t := warehouse.WarehouseTypeForIntegration(jsonutil.GetStr(ds, "integrationKey")); t != "" {
		whType = t
	}
	keyColumn := jsonutil.GetStr(jsonutil.GetMap(ds, "columnMappings"), "keyColumn")
	return warehouse.ClassifyConstantKey(jsonutil.GetStr(ds, "sqlQuery"), keyColumn, jsonutil.GetStr(ds, "key"), whType)
}

func (e *migrationEngine) projectMetrics() ([]map[string]any, error) {
	if !e.metricsRead {
		e.metrics, e.metricsErr = e.ld.ListMetricsRaw(e.ctx)
		e.metricsRead = true
	}
	return e.metrics, e.metricsErr
}

// staleBoundMetrics names bound metrics whose term on dsKey has an event key other than constKey.
func (e *migrationEngine) staleBoundMetrics(dsKey, constKey string) ([]string, error) {
	bound, err := e.boundMetrics(dsKey)
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, b := range bound {
		var terms []string
		if ek := jsonutil.GetStr(b.num, "eventKey"); b.num != nil && ek != constKey {
			terms = append(terms, fmt.Sprintf("event key %q", ek))
		}
		if ev := jsonutil.GetStr(b.den, "eventName"); b.den != nil && ev != constKey {
			terms = append(terms, fmt.Sprintf("denominator event name %q", ev))
		}
		if len(terms) > 0 {
			stale = append(stale, fmt.Sprintf("%s (%s)", b.key, strings.Join(terms, ", ")))
		}
	}
	return stale, nil
}

func boundMetricsUnchecked(err error) error {
	return fmt.Errorf("not updated, because its bound metrics could not be checked: %w. Rerun to try again", err)
}

// failDataSource prints only the error's first line; the report keeps the full error.
func (e *migrationEngine) failDataSource(key, name string, err error) {
	first, _, _ := strings.Cut(err.Error(), "\n")
	output.Fail(first)
	e.state.AddError("data_source", key, err.Error())
	e.report.DataSources.Failed++
	e.report.Errors = append(e.report.Errors, fmt.Sprintf("Data source \"%s\": %v", name, err))
}

func (e *migrationEngine) warn(msg string) {
	output.Warn(msg)
	e.report.Warnings = append(e.report.Warnings, msg)
}

// updateDataSource patches in place: LaunchDarkly has no delete route for metric data
// sources and keeps keys unique after archiving.
func (e *migrationEngine) updateDataSource(existing, body map[string]any, name string, plan dataSourcePlan) {
	key := jsonutil.GetStr(body, "key")
	cm := jsonutil.GetMap(body, "columnMappings")
	res, err := e.overwritePatch(existing, body, plan)
	if err == nil {
		_, err = e.guardRemovals(key, jsonutil.GetMap(existing, "columnMappings"), res.ops, newColumnSet(cm["columns"]))
	}
	if err != nil {
		e.failDataSource(key, name, err)
		return
	}
	ops := append(patchPreconditions(existing), res.ops...)
	if _, err := e.ld.UpdateMetricDataSource(e.ctx, key, ops); err != nil {
		if errors.Is(err, launchdarkly.ErrPatchNotApplied) {
			err = fmt.Errorf("not updated: it changed in LaunchDarkly after this run read it; rerun to pick up the change")
		}
		e.failDataSource(key, name, err)
		return
	}
	output.Done()
	if !e.state.IsDataSourceDone(key) {
		e.state.MarkDataSourceDone(key)
	}
	e.report.DataSources.Updated++
	if len(res.changes) > 0 {
		e.warn(fmt.Sprintf("Data source %q was updated, but the new query does not return all of its mapped columns: %s. Check its mappings in LaunchDarkly.", key, strings.Join(res.changes, "; ")))
	}
	if len(plan.stale) > 0 {
		e.warn(fmt.Sprintf("Data source %q was updated with --force-overwrite. Until their event key is set to %q in LaunchDarkly, these bound metrics match no rows: %s.", key, key, strings.Join(plan.stale, ", ")))
	}
	d, readers := mappingDiff{}, []string(nil)
	if res.mapped != nil {
		d, readers = *res.mapped, plan.mappings.readers
		if plan.mappings.target.Value != "" && jsonutil.GetStr(cm, "valueColumn") == "" {
			// Already reported as statsig_value_column_not_in_query.
			d.missing = slices.DeleteFunc(d.missing, func(m string) bool { return strings.HasPrefix(m, "value ") })
		}
	}
	for _, w := range res.wrongType {
		if !slices.Contains(d.wrongType, w) {
			d.wrongType = append(d.wrongType, w)
		}
	}
	e.finishMappings(key, d.changes, mappingIssues(key, d, readers))
}

// patchPreconditions returns "test" ops so LaunchDarkly applies the patch only if the
// query field and column mappings still equal what this run listed.
func patchPreconditions(existing map[string]any) []launchdarkly.JSONPatchOp {
	var ops []launchdarkly.JSONPatchOp
	for _, field := range []string{"tableName", "viewName", "sqlQuery"} {
		if v, ok := existing[field]; ok && v != nil {
			ops = append(ops, launchdarkly.JSONPatchOp{Op: "test", Path: "/" + field, Value: v})
		}
	}
	if cm, ok := existing["columnMappings"]; ok && cm != nil {
		ops = append(ops, launchdarkly.JSONPatchOp{Op: "test", Path: "/columnMappings", Value: cm})
	}
	return ops
}

// overwriteResult is an --overwrite patch.
type overwriteResult struct {
	ops []launchdarkly.JSONPatchOp
	// changes describe mappings that changed beyond case, as "field: old → new".
	changes []string
	// wrongType names fallback columns not used for their type, as `value "NOTE" (TEXT)`.
	wrongType []string
	// mapped is the --update-mappings part.
	mapped *mappingDiff
}

// dataSourcePatch builds the JSON Patch from existing to body. Existing mappings are
// kept while the new columns include them (case-insensitively, in the new case), so
// edits made in LaunchDarkly survive. LaunchDarkly requires exactly one of tableName,
// viewName, and sqlQuery, and omits unset keyColumn and valueColumn, hence "add".
func dataSourcePatch(existing, body map[string]any, constantKey bool) (overwriteResult, error) {
	var res overwriteResult
	cm, _ := body["columnMappings"].(map[string]any)
	old := jsonutil.GetMap(existing, "columnMappings")
	cols := newColumnSet(cm["columns"])

	oldKey := jsonutil.GetStr(old, "keyColumn")
	newKey := jsonutil.GetStr(cm, "keyColumn")
	if !constantKey && oldKey != "" {
		real, ok := cols.find(oldKey)
		if !ok {
			return res, fmt.Errorf("not updated: its key column %q is not in the updated query; metrics on it would filter a different column. Keep that column in the Statsig SQL, or update the data source by hand", oldKey)
		}
		newKey = real
	}

	var ops []launchdarkly.JSONPatchOp
	for _, field := range []string{"tableName", "viewName", "sqlQuery"} {
		_, had := existing[field]
		if v := jsonutil.GetStr(body, field); v != "" {
			ops = append(ops, launchdarkly.JSONPatchOp{Op: "add", Path: "/" + field, Value: v})
		} else if had {
			ops = append(ops, launchdarkly.JSONPatchOp{Op: "remove", Path: "/" + field})
		}
	}

	if old == nil {
		res.ops = append(ops, launchdarkly.JSONPatchOp{Op: "add", Path: "/columnMappings", Value: cm})
		return res, nil
	}

	var changes []string
	set := func(op, field string, v any) {
		ops = append(ops, launchdarkly.JSONPatchOp{Op: op, Path: "/columnMappings/" + field, Value: v})
	}

	if newKey != "" && newKey != oldKey {
		set("add", "keyColumn", newKey)
	}

	set("replace", "columns", cm["columns"])

	oldTS := jsonutil.GetStr(old, "timestampColumn")
	if real, ok := cols.find(oldTS); ok {
		if real != oldTS {
			set("replace", "timestampColumn", real)
		}
	} else if ts := jsonutil.GetStr(cm, "timestampColumn"); ts != "" && ts != oldTS {
		// The old one cannot stay: LaunchDarkly requires the timestamp column in the column list.
		if typ := cols.typeOf(ts); typ != "" && !warehouse.IsTimestampType(typ) {
			return res, fmt.Errorf("not updated: its timestamp column %q is not in the updated query, and the Statsig source's %q is %s, not a timestamp or date. Keep %q in the Statsig SQL, or correct the Statsig source's timestamp column, then rerun", oldTS, ts, typ, oldTS)
		}
		set("replace", "timestampColumn", ts)
		changes = append(changes, mappingChange("timestamp", oldTS, ts))
	}

	if oldVC := jsonutil.GetStr(old, "valueColumn"); oldVC != "" {
		vc, found := cols.find(jsonutil.GetStr(cm, "valueColumn"))
		typ := cols.typeOf(vc)
		if real, ok := cols.find(oldVC); ok {
			if real != oldVC {
				set("add", "valueColumn", real)
			}
		} else if found && (typ == "" || warehouse.IsNumericType(typ)) {
			set("add", "valueColumn", vc)
			changes = append(changes, mappingChange("value column", oldVC, vc))
		} else {
			if found {
				res.wrongType = append(res.wrongType, fmt.Sprintf("value %q (%s)", vc, typ))
			}
			set("remove", "valueColumn", nil)
			changes = append(changes, mappingChange("value column", oldVC, ""))
		}
	}

	oldCtx := stringMap(old["contexts"])
	newCtx := stringMap(cm["contexts"])
	var ctxOps []launchdarkly.JSONPatchOp
	var ctxChanges []string
	kept := 0
	for _, kind := range slices.Sorted(maps.Keys(oldCtx)) {
		path := "/columnMappings/contexts/" + launchdarkly.EscapeJSONPointer(kind)
		col := oldCtx[kind]
		if real, ok := cols.find(col); ok {
			kept++
			if real != col {
				ctxOps = append(ctxOps, launchdarkly.JSONPatchOp{Op: "add", Path: path, Value: real})
			}
			continue
		}
		if real, ok := cols.find(newCtx[kind]); ok {
			kept++
			ctxOps = append(ctxOps, launchdarkly.JSONPatchOp{Op: "add", Path: path, Value: real})
			ctxChanges = append(ctxChanges, mappingChange("context "+kind, col, real))
			continue
		}
		ctxOps = append(ctxOps, launchdarkly.JSONPatchOp{Op: "remove", Path: path})
		ctxChanges = append(ctxChanges, mappingChange("context "+kind, col, ""))
	}
	if kept == 0 && len(newCtx) > 0 {
		set("replace", "contexts", newCtx)
		for _, kind := range slices.Sorted(maps.Keys(union(oldCtx, newCtx))) {
			if oldCtx[kind] != newCtx[kind] {
				changes = append(changes, mappingChange("context "+kind, oldCtx[kind], newCtx[kind]))
			}
		}
	} else {
		ops = append(ops, ctxOps...)
		changes = append(changes, ctxChanges...)
	}
	res.ops, res.changes = ops, changes
	return res, nil
}

func union(a, b map[string]string) map[string]string {
	out := maps.Clone(a)
	maps.Copy(out, b)
	return out
}

// columnSet matches names exactly, then case-insensitively, returning the list's case.
type columnSet struct {
	exact map[string]bool
	lower map[string]string
	// types holds each listed column's type by name; "" when unknown.
	types map[string]string
}

func newColumnSet(columns any) columnSet {
	s := columnSet{exact: map[string]bool{}, lower: map[string]string{}, types: map[string]string{}}
	add := func(name, typ string) {
		if name == "" {
			return
		}
		s.exact[name] = true
		s.types[name] = typ
		if _, ok := s.lower[strings.ToLower(name)]; !ok {
			s.lower[strings.ToLower(name)] = name
		}
	}
	switch cols := columns.(type) {
	case []map[string]any:
		for _, c := range cols {
			add(jsonutil.GetStr(c, "name"), jsonutil.GetStr(c, "type"))
		}
	case []warehouse.FallbackColumn:
		for _, c := range cols {
			add(c.Name, c.Type)
		}
	case []any:
		for _, c := range cols {
			if m, ok := c.(map[string]any); ok {
				add(jsonutil.GetStr(m, "name"), jsonutil.GetStr(m, "type"))
			}
		}
	}
	return s
}

// typeOf returns the type of the column name matches, or "".
func (s columnSet) typeOf(name string) string {
	real, _ := s.find(name)
	return s.types[real]
}

func (s columnSet) find(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if s.exact[name] {
		return name, true
	}
	real, ok := s.lower[strings.ToLower(name)]
	return real, ok
}

func stringMap(v any) map[string]string {
	out := map[string]string{}
	switch m := v.(type) {
	case map[string]string:
		for k, val := range m {
			out[k] = val
		}
	case map[string]any:
		for k, val := range m {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// applyConstantEventKey wraps the SQL and re-previews it: LaunchDarkly validates the
// saved columns against the wrapped query, so the column list must come from it.
func (e *migrationEngine) applyConstantEventKey(body map[string]any, whType, integrationConfigID string, sourceColumns []string) error {
	column, err := warehouse.ApplyConstantEventKey(body, whType, sourceColumns)
	if err != nil {
		return err
	}
	if integrationConfigID == "" {
		return nil
	}
	wrapped := jsonutil.GetStr(body, "sqlQuery")
	preview, err := e.ld.PreviewDataSource(e.ctx, integrationConfigID, wrapped)
	if err != nil {
		return fmt.Errorf("preview of the wrapped source SQL failed (LaunchDarkly runs the same query to validate the data source): %w", err)
	}
	realColumns := warehouse.ExtractPreviewColumns(preview)
	if len(realColumns) == 0 {
		return fmt.Errorf("preview of the wrapped source SQL returned no columns")
	}
	cm := body["columnMappings"].(map[string]any)
	cm["columns"] = realColumns
	warehouse.ReconcileColumnMappings(cm, preview, realColumns)
	warehouse.PinConstantKeyColumn(cm, column, realColumns)
	return nil
}

func (e *migrationEngine) printDryRunReport() {
	fmt.Fprintf(os.Stderr, "\n%s\n", strings.Repeat("=", 60))
	fmt.Fprintln(os.Stderr, "  DRY RUN -- No changes were made to LaunchDarkly")
	fmt.Fprintf(os.Stderr, "%s\n\n", strings.Repeat("=", 60))

	// Report where the type came from. A guess read as a finding is how a run
	// ends up reporting a warehouse the customer does not use. The only error
	// here is an invalid --warehouse-type, already rejected at startup.
	detected, typeSource, _ := warehouse.ResolveWarehouseType(whFlagWarehouseType, e.whConnections, e.metricSources)
	switch {
	case detected == "":
		fmt.Fprintf(os.Stderr, "  Warehouse type: unknown (pass --warehouse-type)\n")
	case typeSource.IsConfident():
		fmt.Fprintf(os.Stderr, "  Warehouse type: %s (from %s)\n", detected, typeSource)
	default:
		fmt.Fprintf(os.Stderr, "  Warehouse type: %s -- GUESS ONLY, %s. Confirm with --warehouse-type before a real run.\n", detected, typeSource)
	}

	e.printDryRunDataSources(detected)

	if e.constantEventKey {
		e.writeDryRunBodies(detected)
	}

	fmt.Fprintln(os.Stderr, "\n  Note: metric definitions are migrated separately by `statsig-to-ld metrics convert`.")
}

type dryRunListing struct {
	// checked reports that the project's data sources were read.
	checked  bool
	total    int
	lines    []string
	warnings []string
	note     string
}

// dryRunDataSources makes the plans a real run would. Without LD credentials existing
// data sources are unknown, so every non-colliding source is listed as a create.
func (e *migrationEngine) dryRunDataSources(whType string) dryRunListing {
	var out dryRunListing
	existing := map[string]map[string]any{}
	if e.ldReadable {
		list, err := e.ld.ListMetricDataSources(e.ctx)
		if err != nil {
			out.note = fmt.Sprintf("Existing LaunchDarkly data sources could not be read (%v), so they were not checked; a real run stops on this error.", err)
		} else {
			existing = indexDataSources(list)
			out.checked = true
		}
	} else {
		out.note = "Existing LaunchDarkly data sources were not checked. Pass --ld-key and --ld-project to see which sources would be created, updated, skipped, or refused."
	}

	collisions := keyCollisions(e.metricSources)
	integrationKey := warehouse.WarehouseTypes[whType]
	withoutConstantKey, unchanged := 0, 0
	var issues []sourceIssue
	for _, source := range e.metricSources {
		st := jsonutil.GetStr(source, "sourceType")
		if st == "" {
			st = "query"
		}
		body := warehouse.MapMetricSourceToDataSource(source, e.environmentKey, integrationKey, "")
		plan := e.planSource(source, body, existing, collisions, whType)
		e.recordPlan(jsonutil.GetStr(body, "key"), plan)
		if plan.mappings != nil {
			issues = append(issues, plan.mappings.issues...)
		}
		if plan.note == noteExistsWithoutConstantKey {
			withoutConstantKey++
		}
		if plan.mappings != nil && plan.action == planSkip {
			unchanged++
		} else {
			out.lines = append(out.lines, fmt.Sprintf("%s (%s): %s", jsonutil.GetStr(body, "name"), st, dryRunOutcome(plan))+mappingLines(plan))
		}
		if plan.warning != "" {
			out.warnings = append(out.warnings, plan.warning)
		}
	}
	if unchanged > 0 {
		out.lines = append(out.lines, fmt.Sprintf("%d existing data source(s): would skip (no mapping changes)", unchanged))
	}
	if msg := withoutConstantKeySummary(withoutConstantKey); msg != "" {
		out.warnings = append(out.warnings, msg)
	}
	out.warnings = append(out.warnings, issueSummaries(issues)...)
	out.total = len(e.metricSources)
	return out
}

func (e *migrationEngine) printDryRunDataSources(whType string) {
	l := e.dryRunDataSources(whType)
	if l.checked {
		fmt.Fprintf(os.Stderr, "\n  Metric data sources (checked against LaunchDarkly project %q): %d\n", e.projectKey, l.total)
	} else {
		fmt.Fprintf(os.Stderr, "\n  Metric data sources: %d\n", l.total)
	}
	for _, line := range l.lines {
		fmt.Fprintf(os.Stderr, "    - %s\n", line)
	}
	for _, w := range l.warnings {
		fmt.Fprintf(os.Stderr, "\n  Warning: %s\n", w)
	}
	if l.note != "" {
		fmt.Fprintf(os.Stderr, "\n  Note: %s\n", l.note)
	}
}

func dryRunOutcome(plan dataSourcePlan) string {
	switch plan.action {
	case planUpdate:
		if len(plan.stale) > 0 {
			return fmt.Sprintf("would update (--force-overwrite; these bound metrics would match no rows: %s)", strings.Join(plan.stale, ", "))
		}
		return "would update"
	case planUpdateMappings:
		return "would update mappings"
	case planSkip:
		return fmt.Sprintf("would skip (%s)", plan.reason)
	case planRefuse:
		return fmt.Sprintf("would refuse (%s)", plan.reason)
	default:
		return "would create"
	}
}

// writeDryRunBodies writes the pre-preview bodies so the wrapped SQL can be run by hand.
func (e *migrationEngine) writeDryRunBodies(whType string) {
	integrationKey := warehouse.WarehouseTypes[whType]
	var bodies []map[string]any
	var problems int
	collisions := keyCollisions(e.metricSources)
	for _, source := range e.metricSources {
		body := warehouse.MapMetricSourceToDataSource(source, e.environmentKey, integrationKey, e.maintainerID)
		if names, ok := collisions[jsonutil.GetStr(body, "key")]; ok {
			body["error"] = fmt.Sprintf("Statsig sources %s all map to this data source key; none of them is created", quoteAll(names))
			problems++
		} else if conflicts := warehouse.ContextKindConflicts(source); len(conflicts) > 0 {
			body["error"] = kindConflictError(conflicts, false).Error()
			problems++
		} else if _, err := warehouse.ApplyConstantEventKey(body, whType, nil); err != nil {
			body["error"] = err.Error()
			problems++
		}
		bodies = append(bodies, body)
	}
	raw, _ := json.MarshalIndent(bodies, "", "  ")
	if err := os.WriteFile("data-source-bodies.json", raw, 0o644); err != nil {
		output.Warn(fmt.Sprintf("Could not write data-source-bodies.json: %v", err))
		return
	}
	fmt.Fprintf(os.Stderr, "\n  Wrapped data source SQL: data-source-bodies.json (%d sources, %d with an error)\n", len(bodies), problems)
}

func (e *migrationEngine) printReport() {
	ds := e.report.DataSources

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
	fmt.Fprintf(os.Stderr, "  Metric Data Sources:   %d created, %d updated, %d skipped, %d failed\n", ds.Created, ds.Updated, ds.Skipped, ds.Failed)

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

// writeSourceMapping writes a source-mapping.json that maps each Statsig
// metric source name to the LD data source key created in Phase 3a. This
// is the input format `statsig-to-ld metrics convert --source-mapping`
// expects, so the user can chain the two commands without hand-building
// the mapping.
func (e *migrationEngine) writeSourceMapping() error {
	if len(e.metricSources) == 0 {
		return nil
	}
	// Colliding keys are never created; unmapped keys belong to another environment.
	collisions := keyCollisions(e.metricSources)
	mapping := map[string]string{}
	for _, source := range e.metricSources {
		name := jsonutil.GetStr(source, "name")
		if name == "" {
			continue
		}
		key := warehouse.SanitizeKey(name)
		if _, ok := collisions[key]; ok || e.unmapped[key] {
			continue
		}
		mapping[name] = key
	}
	if len(mapping) == 0 {
		return nil
	}
	raw, err := json.MarshalIndent(mapping, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile("source-mapping.json", raw, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  Source mapping:        source-mapping.json (%d entries)\n", len(mapping))
	return nil
}

// printHandoff prints the next-step command the user should run to migrate
// warehouse-native metric definitions, now that data sources exist in LD.
func (e *migrationEngine) printHandoff() {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Next step — migrate metric definitions:")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "    statsig-to-ld metrics convert --all \\\n")
	fmt.Fprintf(os.Stderr, "      --ld-project %s \\\n", e.projectKey)
	fmt.Fprintln(os.Stderr, "      --source-mapping source-mapping.json")
	fmt.Fprintln(os.Stderr)
}
