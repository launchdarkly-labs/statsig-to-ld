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
	whFlagRemoveUnmapped    bool
	whFlagMaintainerMapping string
	whFlagUpdateMaintainers bool
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

  # Dry run (no LD changes). With --ld-key, --ld-project, and --ld-environment it
  # also previews each source's query in the warehouse and reports which data
  # sources a real run would create, update, skip, refuse, or fail.
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
	warehouseCmd.Flags().StringVar(&whFlagLDMaintainer, "ld-maintainer", "", "Maintainer for created data sources: an email, a 24-character member ID, or \"none\" to send none (LaunchDarkly then assigns a personal token's member). Defaults to the member who owns the API token")
	warehouseCmd.Flags().StringVar(&whFlagMaintainerMapping, "ld-maintainer-mapping", "", "JSON file mapping Statsig metric source names to the maintainer of their data source: an email, a 24-character member ID, or \"none\". Sources it does not name get --ld-maintainer")
	warehouseCmd.Flags().BoolVar(&whFlagUpdateMaintainers, "update-maintainers", false, "Set the maintainer of existing data sources in this environment in place: to the one --ld-maintainer-mapping names, or, for one with no maintainer, to --ld-maintainer. A maintainer set in LaunchDarkly is replaced only by a mapping entry")
	warehouseCmd.Flags().BoolVar(&whFlagDryRun, "dry-run", false, "Export and preview data source mapping without writing to LD. With --ld-key, --ld-project, and --ld-environment, also check existing data sources and preview each query in the warehouse, as a real run does")
	warehouseCmd.Flags().BoolVar(&whFlagResume, "resume", false, "Resume from migration_state.json")
	warehouseCmd.Flags().StringVar(&whFlagOnly, "only", "", "Run only 'warehouse' (Phase 2) or 'data-sources' (Phase 3)")
	warehouseCmd.Flags().BoolVar(&whFlagOverwrite, "overwrite", false, "Update existing LD metric data sources that lack the constant event key in place: their query, key column, and column list (timestamp, value, and context mappings are kept while the new query returns them). Refuses a data source whose bound metrics use other event keys, whose value column or context kind columns the new query drops while bound metrics use them, or, with --constant-event-key=false, whose key column the new query no longer returns")
	warehouseCmd.Flags().BoolVar(&whFlagForceOverwrite, "force-overwrite", false, "Like --overwrite, but also updates data sources whose bound metrics use other event keys and would match no rows afterwards; the run still lists those metrics")
	warehouseCmd.Flags().BoolVar(&whFlagUpdateMappings, "update-mappings", false, "Correct existing LD metric data sources' timestamp, value, and context mappings from the Statsig export")
	warehouseCmd.Flags().BoolVar(&whFlagRemoveUnmapped, "remove-unmapped-contexts", false, "With --update-mappings, also remove context kinds the Statsig export does not map, except those bound metrics use as analysis or randomization units")
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
	// --update-maintainers patches; those data sources may count above too.
	MaintainersUpdated int `json:"maintainers_updated"`
	MaintainersFailed  int `json:"maintainers_failed"`
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
	verbose        bool

	constantEventKey bool
	forceOverwrite   bool
	updateMappings   bool
	removeUnmapped   bool
	ldReadable       bool

	// maintainerMapping is --ld-maintainer-mapping; maintainers holds its resolved
	// entries, by Statsig source name, and defaultMaintainer the run's.
	maintainerMapping   map[string]string
	maintainers         map[string]launchdarkly.Maintainer
	defaultMaintainer   launchdarkly.Maintainer
	maintainersResolved bool
	updateMaintainers   bool

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
	if whFlagRemoveUnmapped && !whFlagUpdateMappings {
		return fmt.Errorf("--remove-unmapped-contexts requires --update-mappings")
	}
	var maintainerMapping map[string]string
	if whFlagMaintainerMapping != "" {
		var err error
		if maintainerMapping, err = loadMaintainerMapping(whFlagMaintainerMapping); err != nil {
			return err
		}
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
		removeUnmapped:   whFlagRemoveUnmapped,
		ldReadable:       whFlagLDKey != "" && whFlagLDProject != "" && whFlagLDEnvironment != "",

		maintainerMapping: maintainerMapping,
		updateMaintainers: whFlagUpdateMaintainers,
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

	if err := e.checkMaintainerMapping(); err != nil {
		return err
	}

	if e.dryRun {
		// Member lookups are reads, so a dry run resolves the maintainers a real run would.
		if e.ldReadable {
			if err := e.resolveMaintainers(true); err != nil {
				return err
			}
		}
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
	if err := e.resolveMaintainers(true); err != nil {
		return err
	}
	except := ""
	if n := len(e.maintainerMapping); n > 0 {
		except = fmt.Sprintf(" except the %d Statsig source(s) --ld-maintainer-mapping names", n)
	}
	logMaintainer(e.defaultMaintainer, "data source", except)

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
	// The integration check first: it stops the run, and the export check may POST a probe.
	expExists, err := e.checkExperimentationExists(detected)
	if err != nil {
		return err
	}
	exportExists := e.checkDataExportExists(detected)

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
func (e *migrationEngine) checkExperimentationExists(whType string) (bool, error) {
	output.Info("Checking experimentation integration...")
	integrationKey, configID, err := e.environmentIntegration()
	if err != nil {
		// Read as "none", setup could add a second integration for the environment.
		return false, fmt.Errorf("could not read the LaunchDarkly experimentation integrations, so cannot tell whether one is set up for environment %q: %w. Rerun once the token can read them", e.environmentKey, err)
	}
	if configID != "" {
		output.Ok(fmt.Sprintf("Experimentation integration exists for env '%s' (%s), skipping", e.environmentKey, integrationKey))
		return true, nil
	}
	return false, nil
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

func (e *migrationEngine) getActiveIntegration() (string, string, error) {
	integrationKey, configID, err := e.environmentIntegration()
	if err != nil {
		// Another type's integration could be the one this environment uses.
		return "", "", fmt.Errorf("could not read the LaunchDarkly experimentation integrations, so cannot tell which warehouse the data sources use: %w. Rerun once the token can read them", err)
	}
	if configID != "" {
		return integrationKey, configID, nil
	}
	for _, t := range warehouse.SupportedWarehouseTypes {
		integrationKey := warehouse.WarehouseTypes[t]
		configs := e.ld.ListIntegrationConfigs(e.ctx, integrationKey)
		if len(configs) > 0 {
			configID := jsonutil.GetStr(configs[0], "_id")
			if configID == "" {
				configID = jsonutil.GetStr(configs[0], "id")
			}
			return integrationKey, configID, nil
		}
	}
	return "snowflake-experimentation", "", nil
}

// environmentIntegration returns the experimentation integration set up for this
// project and environment, in SupportedWarehouseTypes order. configID is "" when none
// is found. err joins the lists that could not be read, and is set even with a match.
func (e *migrationEngine) environmentIntegration() (integrationKey, configID string, err error) {
	var unread []string
	for _, t := range warehouse.SupportedWarehouseTypes {
		key := warehouse.WarehouseTypes[t]
		configs, readErr := e.ld.ReadIntegrationConfigs(e.ctx, key)
		if readErr != nil {
			unread = append(unread, readErr.Error())
			continue
		}
		for _, cfg := range configs {
			// The list is account-wide: match the project as well as the environment.
			env := jsonutil.GetMap(jsonutil.GetMap(cfg, "configValues"), "selectedEnv")
			if jsonutil.GetStr(env, "projectKey") == e.projectKey && jsonutil.GetStr(env, "environmentKey") == e.environmentKey {
				id := jsonutil.GetStr(cfg, "_id")
				if id == "" {
					id = jsonutil.GetStr(cfg, "id")
				}
				if integrationKey == "" {
					integrationKey, configID = key, id
				}
			}
		}
	}
	if len(unread) > 0 {
		err = errors.New(strings.Join(unread, "; "))
	}
	return integrationKey, configID, err
}

func (e *migrationEngine) phase3aMigrateDataSources() error {
	output.Phase(3, "Migrating metric data sources...")

	if len(e.metricSources) == 0 {
		output.Info("No metric sources to migrate.")
		return nil
	}

	integrationKey, integrationConfigID, err := e.getActiveIntegration()
	if err != nil {
		return err
	}
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
		body := warehouse.MapMetricSourceToDataSource(source, e.environmentKey, integrationKey, e.maintainerFor(source))
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

		dropped, _, err := e.previewSource(source, body, name, whType, integrationConfigID)
		if err != nil {
			e.failDataSource(key, name, err)
			continue
		}
		if dropped {
			droppedValue = append(droppedValue, key)
			e.report.Notes = append(e.report.Notes, reportNote{Code: noteValueColumnNotInQuery, DataSource: key})
		}

		if plan.action == planUpdate {
			res, _, err := e.updatePatch(existing[key], body, plan)
			if err != nil {
				e.failDataSource(key, name, err)
				continue
			}
			e.updateDataSource(existing[key], body, name, plan, res)
			continue
		}

		if _, err := e.ld.CreateMetricDataSource(e.ctx, body); err != nil {
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
	e.syncMaintainers(existing, collisions)
	if msg := withoutConstantKeySummary(withoutConstantKey); msg != "" {
		e.warn(msg)
	}
	if msg := droppedValueSummary(droppedValue); msg != "" {
		e.warn(msg)
	}
	for _, msg := range issueSummaries(e.issues) {
		e.warn(msg)
	}
	return nil
}

// previewSource fills body's columns and mappings from the warehouse preview, as the
// run sends them. dropped reports a Statsig value column the query does not return;
// rawErr, a failed preview of the Statsig query, which the run only logs.
func (e *migrationEngine) previewSource(source, body map[string]any, name, whType, integrationConfigID string) (dropped bool, rawErr, err error) {
	statsigValue := jsonutil.GetStr(jsonutil.GetMap(body, "columnMappings"), "valueColumn")
	var sourceColumns []string
	if previewSQL := warehouse.BuildPreviewSQL(source); integrationConfigID != "" && previewSQL != "" {
		preview, err := e.ld.PreviewDataSource(e.ctx, integrationConfigID, previewSQL)
		rawErr = err
		if err == nil {
			if realColumns := warehouse.ExtractPreviewColumns(preview); len(realColumns) > 0 {
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
	if e.constantEventKey {
		if err := e.applyConstantEventKey(body, whType, integrationConfigID, sourceColumns); err != nil {
			return false, rawErr, err
		}
	}
	return statsigValue != "" && jsonutil.GetStr(jsonutil.GetMap(body, "columnMappings"), "valueColumn") == "", rawErr, nil
}

func droppedValueSummary(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return fmt.Sprintf("%d data source(s) did not get the value column their Statsig source defines, because their query does not return it: %s. Add the column to the Statsig source's query and to the data source's in LaunchDarkly, or set a value column on their numeric metrics.",
		len(keys), strings.Join(keys, ", "))
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
	// kindConflicts refuse a plain --overwrite only if its fallback takes one of these kinds from the export.
	kindConflicts []warehouse.KindConflict
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
	conflicts := warehouse.ContextKindConflicts(source)
	if plan.action == planUpdate && !e.updateMappings {
		plan.kindConflicts = conflicts
		return e.planMappings(plan, ds, source)
	}
	takesExport := plan.action == planCreate || plan.action == planUpdate || (e.updateMappings && ds != nil && plan.action == planSkip)
	if takesExport && len(conflicts) > 0 {
		var kinds []string
		for _, c := range conflicts {
			kinds = append(kinds, c.Kind)
		}
		return plan.refuse("Statsig unit IDs share context kind(s) "+quoteAll(kinds), kindConflictError(conflicts, ds != nil))
	}
	return e.planMappings(plan, ds, source)
}

// conflictsTaken refuses when ops give a conflicting kind a column from the export.
func conflictsTaken(old map[string]any, ops []launchdarkly.JSONPatchOp, conflicts []warehouse.KindConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	before := stringMap(old["contexts"])
	_, after := mappingsAfter(old, ops)
	var taken []warehouse.KindConflict
	for _, c := range conflicts {
		if col, ok := after[c.Kind]; ok && !strings.EqualFold(col, before[c.Kind]) {
			taken = append(taken, c)
		}
	}
	if len(taken) == 0 {
		return nil
	}
	return kindConflictError(taken, true)
}

func kindConflictError(conflicts []warehouse.KindConflict, exists bool) error {
	var parts []string
	for _, c := range conflicts {
		parts = append(parts, fmt.Sprintf("Statsig unit IDs %s all map to context kind %q, with different columns (%s)", quoteAll(c.Units), c.Kind, quoteAll(c.Columns)))
	}
	if exists {
		return fmt.Errorf("not updated: %s. Keep one of them in the Statsig source's id-type mapping, then rerun", strings.Join(parts, "; "))
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

// updatePatch builds and checks the patch an update of a previewed body sends; reason
// is a short refusal reason when one exists.
func (e *migrationEngine) updatePatch(existing, body map[string]any, plan dataSourcePlan) (res overwriteResult, reason string, err error) {
	old := jsonutil.GetMap(existing, "columnMappings")
	res, err = e.overwritePatch(existing, body, plan)
	if err == nil {
		err = conflictsTaken(old, res.ops, plan.kindConflicts)
	}
	if err == nil {
		reason, err = e.guardRemovals(jsonutil.GetStr(body, "key"), old, res.ops, newColumnSet(jsonutil.GetMap(body, "columnMappings")["columns"]))
	}
	return res, reason, err
}

// updateDataSource patches in place: LaunchDarkly has no delete route for metric data
// sources and keeps keys unique after archiving.
func (e *migrationEngine) updateDataSource(existing, body map[string]any, name string, plan dataSourcePlan, res overwriteResult) {
	key := jsonutil.GetStr(body, "key")
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
	changes, issues := updateMappingOutcome(key, jsonutil.GetMap(body, "columnMappings"), res, plan)
	e.finishMappings(key, changes, issues)
}

// updateMappingOutcome gives an update's --update-mappings changes and issues.
func updateMappingOutcome(key string, cm map[string]any, res overwriteResult, plan dataSourcePlan) ([]string, []sourceIssue) {
	d, mp := mappingDiff{}, (*mappingPlan)(nil)
	if res.mapped != nil {
		d, mp = *res.mapped, plan.mappings
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
	return d.changes, mappingIssues(key, d, mp)
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
	whType := detected
	var l dryRunListing
	if e.only != "warehouse" {
		l = e.dryRunDataSources(detected)
		whType = l.whType
	}
	fmt.Fprintf(os.Stderr, "  %s\n", warehouseTypeLine(detected, typeSource, l))

	if e.only != "warehouse" {
		e.printDryRunDataSources(l)
		if e.constantEventKey {
			e.writeDryRunBodies(whType)
		}
	}

	fmt.Fprintln(os.Stderr, "\n  Note: metric definitions are migrated separately by `statsig-to-ld metrics convert`.")
}

// warehouseTypeLine says which warehouse type the dry run used and where it came from.
func warehouseTypeLine(detected string, source warehouse.WarehouseTypeSource, l dryRunListing) string {
	const fromIntegration = "from the LaunchDarkly environment's experimentation integration"
	switch {
	case l.previewed && detected != "" && l.whType != detected && source.IsConfident():
		return fmt.Sprintf("Warehouse type: %s (%s, which data sources use; %s from %s, which data export setup uses)", l.whType, fromIntegration, detected, source)
	case l.previewed:
		return fmt.Sprintf("Warehouse type: %s (%s)", l.whType, fromIntegration)
	case detected == "":
		return "Warehouse type: unknown (pass --warehouse-type)"
	case source.IsConfident():
		return fmt.Sprintf("Warehouse type: %s (from %s)", detected, source)
	}
	return fmt.Sprintf("Warehouse type: %s -- GUESS ONLY, %s. Confirm with --warehouse-type before a real run.", detected, source)
}

type dryRunListing struct {
	// checked reports that the project's data sources were read; previewed, that creates
	// and updates were previewed and checked as in a real run.
	checked, previewed bool
	// whType is the warehouse type the plans used: the integration's, when previewed.
	whType   string
	total    int
	lines    []string
	warnings []string
	notes    []string
	// maintainers lists --update-maintainers changes when maintainersChecked.
	maintainers        []string
	maintainersChecked bool
}

// dryRunDataSources makes the plans a real run would. With the environment's warehouse
// integration it also runs a real run's previews and checks on creates and updates.
// Without LD credentials existing data sources are unknown, so every non-colliding
// source is listed as a create.
func (e *migrationEngine) dryRunDataSources(whType string) dryRunListing {
	var out dryRunListing
	existing := map[string]map[string]any{}
	if e.ldReadable {
		list, err := e.ld.ListMetricDataSources(e.ctx)
		if err != nil {
			out.notes = append(out.notes, fmt.Sprintf("Existing LaunchDarkly data sources could not be read (%v), so they were not checked; a real run stops on this error.", err))
		} else {
			existing = indexDataSources(list)
			out.checked = true
		}
	} else {
		out.notes = append(out.notes, "Existing LaunchDarkly data sources were not checked, and no query was previewed. Pass --ld-key, --ld-project, and --ld-environment to see what a real run would create, update, skip, refuse, or fail.")
	}

	integrationKey, configID := warehouse.WarehouseTypes[whType], ""
	if out.checked {
		// Only this environment's integration: another one could preview a different warehouse.
		switch k, id, err := e.environmentIntegration(); {
		case err != nil:
			out.notes = append(out.notes, fmt.Sprintf("No query was previewed, because the experimentation integrations could not be read (%v); a real run stops on this error.", err))
		case id != "":
			integrationKey, configID, whType = k, id, warehouse.WarehouseTypeForIntegration(k)
			out.previewed = true
		default:
			out.notes = append(out.notes, fmt.Sprintf("No query was previewed, because LaunchDarkly project %q environment %q has no warehouse experimentation integration yet, so a create or update listed here can still fail when a real run previews it.", e.projectKey, e.environmentKey))
		}
	}

	collisions := keyCollisions(e.metricSources)
	withoutConstantKey, unchanged := 0, 0
	var issues []sourceIssue
	var droppedValue []string
	for _, source := range e.metricSources {
		st := jsonutil.GetStr(source, "sourceType")
		if st == "" {
			st = "query"
		}
		body := warehouse.MapMetricSourceToDataSource(source, e.environmentKey, integrationKey, "")
		key := jsonutil.GetStr(body, "key")
		plan := e.planSource(source, body, existing, collisions, whType)
		e.recordPlan(key, plan)
		outcome := dryRunOutcome(plan)
		var changes []string
		var srcIssues []sourceIssue
		if plan.mappings != nil {
			changes, srcIssues = plan.mappings.changes, plan.mappings.issues
		}
		if out.previewed && (plan.action == planCreate || plan.action == planUpdate) {
			r := e.previewPlan(source, body, existing[key], plan, whType, configID)
			changes, srcIssues = r.changes, r.issues
			if r.dropped {
				droppedValue = append(droppedValue, key)
			}
			if r.outcome != "" {
				outcome = r.outcome
			}
		}
		issues = append(issues, srcIssues...)
		if plan.note == noteExistsWithoutConstantKey {
			withoutConstantKey++
		}
		if plan.mappings != nil && plan.action == planSkip {
			unchanged++
		} else {
			out.lines = append(out.lines, fmt.Sprintf("%s (%s): %s", jsonutil.GetStr(body, "name"), st, outcome)+changeLines(changes))
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
	if msg := droppedValueSummary(droppedValue); msg != "" {
		out.warnings = append(out.warnings, msg)
	}
	out.warnings = append(out.warnings, issueSummaries(issues)...)
	switch {
	case out.checked && e.maintainersResolved && e.updateMaintainers:
		out.maintainersChecked = true
		for _, c := range e.maintainerChanges(existing, collisions, false) {
			out.maintainers = append(out.maintainers, c.key+": "+c.change)
		}
	case out.checked && e.maintainersResolved:
		if msg := maintainerMappingSummary(len(e.maintainerChanges(existing, collisions, true))); msg != "" {
			out.warnings = append(out.warnings, msg)
		}
	case (len(e.maintainerMapping) > 0 || e.updateMaintainers) && !e.ldReadable:
		out.notes = append(out.notes, "Existing data sources' maintainers were not compared. Pass --ld-key, --ld-project, and --ld-environment to see which would change.")
	case len(e.maintainerMapping) > 0 || e.updateMaintainers:
		out.notes = append(out.notes, "Existing data sources' maintainers were not compared, because the data sources could not be read.")
	}
	out.total = len(e.metricSources)
	out.whType = whType
	return out
}

// previewResult is what a real run's previews and checks would make of a planned create
// or update. outcome is set when the run would fail the source.
type previewResult struct {
	changes []string
	issues  []sourceIssue
	dropped bool
	outcome string
}

// previewPlan runs a planned create's or update's previews and checks, writing nothing.
func (e *migrationEngine) previewPlan(source, body, ds map[string]any, plan dataSourcePlan, whType, configID string) previewResult {
	key := jsonutil.GetStr(body, "key")
	dropped, rawErr, err := e.previewSource(source, body, jsonutil.GetStr(body, "name"), whType, configID)
	if err != nil {
		return previewResult{outcome: "would fail (" + failureReason(err) + ")"}
	}
	if rawErr != nil && !e.constantEventKey {
		// Nothing else previews the query, and LaunchDarkly runs it to validate the write.
		return previewResult{outcome: "would fail (preview of the Statsig SQL failed: " + failureReason(rawErr) + ")"}
	}
	r := previewResult{dropped: dropped}
	if plan.action != planUpdate {
		return r
	}
	res, reason, err := e.updatePatch(ds, body, plan)
	if err != nil {
		if reason == "" {
			reason = failureReason(err)
		}
		r.outcome = "would refuse (" + reason + ")"
		return r
	}
	mapped, issues := updateMappingOutcome(key, jsonutil.GetMap(body, "columnMappings"), res, plan)
	r.changes, r.issues = append(slices.Clone(res.changes), mapped...), issues
	return r
}

// failureReason is err's first line without its "not updated: " or "not created: ".
func failureReason(err error) string {
	first, _, _ := strings.Cut(err.Error(), "\n")
	for _, prefix := range []string{"not updated: ", "not created: "} {
		first = strings.TrimPrefix(first, prefix)
	}
	return first
}

func (e *migrationEngine) printDryRunDataSources(l dryRunListing) {
	switch {
	case l.previewed:
		fmt.Fprintf(os.Stderr, "\n  Metric data sources (checked against LaunchDarkly project %q, queries previewed): %d\n", e.projectKey, l.total)
	case l.checked:
		fmt.Fprintf(os.Stderr, "\n  Metric data sources (checked against LaunchDarkly project %q): %d\n", e.projectKey, l.total)
	default:
		fmt.Fprintf(os.Stderr, "\n  Metric data sources: %d\n", l.total)
	}
	for _, line := range l.lines {
		fmt.Fprintf(os.Stderr, "    - %s\n", line)
	}
	if l.maintainersChecked {
		fmt.Fprintf(os.Stderr, "\n  Maintainers that would change (--update-maintainers): %d\n", len(l.maintainers))
		for _, line := range l.maintainers {
			fmt.Fprintf(os.Stderr, "    - %s\n", line)
		}
	}
	for _, w := range l.warnings {
		fmt.Fprintf(os.Stderr, "\n  Warning: %s\n", w)
	}
	for _, n := range l.notes {
		fmt.Fprintf(os.Stderr, "\n  Note: %s\n", n)
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
		body := warehouse.MapMetricSourceToDataSource(source, e.environmentKey, integrationKey, e.maintainerFor(source))
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
	if e.updateMaintainers {
		fmt.Fprintf(os.Stderr, "  Maintainers:           %d updated, %d failed\n", ds.MaintainersUpdated, ds.MaintainersFailed)
	}

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
