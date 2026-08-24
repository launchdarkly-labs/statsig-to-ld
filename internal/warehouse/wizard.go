package warehouse

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/jsonutil"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/output"
)

// PromptWithDefault prompts for input, returning defaultVal if the user presses Enter.
func PromptWithDefault(reader *bufio.Reader, prompt, defaultVal string) string {
	if defaultVal != "" {
		fmt.Fprintf(os.Stderr, "  ? %s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Fprintf(os.Stderr, "  ? %s: ", prompt)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// PromptWarehouseType asks the user to confirm or pick a warehouse type. It
// returns an error rather than looping when input runs out, so a non-interactive
// run fails with something actionable instead of spinning on EOF forever.
func PromptWarehouseType(reader *bufio.Reader, suggestion string) (string, error) {
	fmt.Fprintln(os.Stderr)
	if suggestion != "" {
		output.Info(fmt.Sprintf("Best guess at the warehouse type: %s (not confirmed)", suggestion))
		fmt.Fprintf(os.Stderr, "  ? Use %s? [Y/n]: ", suggestion)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return "", errNoWarehouseTypeInput
		}
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "" || line == "y" || line == "yes" {
			return suggestion, nil
		}
	}
	fmt.Fprintln(os.Stderr, "  Available warehouse types:")
	for i, t := range SupportedWarehouseTypes {
		fmt.Fprintf(os.Stderr, "    %d. %s\n", i+1, t)
	}
	for attempt := 0; attempt < 5; attempt++ {
		fmt.Fprintf(os.Stderr, "  ? Select warehouse type [1-%d]: ", len(SupportedWarehouseTypes))
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return "", errNoWarehouseTypeInput
		}
		choice, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr == nil && choice >= 1 && choice <= len(SupportedWarehouseTypes) {
			return SupportedWarehouseTypes[choice-1], nil
		}
		fmt.Fprintln(os.Stderr, "  Invalid choice, try again.")
	}
	return "", errNoWarehouseTypeInput
}

// errNoWarehouseTypeInput is returned when the warehouse type cannot be
// confirmed interactively. The message names the flag because that is the only
// way to answer in a non-interactive run.
var errNoWarehouseTypeInput = errors.New(
	"could not determine the warehouse type: pass --warehouse-type (" +
		strings.Join(SupportedWarehouseTypes, ", ") + ")")

// WaitForEnter pauses until the user presses Enter.
func WaitForEnter(reader *bufio.Reader, prompt string) {
	fmt.Fprintf(os.Stderr, "  ? %s", prompt)
	_, _ = reader.ReadString('\n')
}

// OrDefault returns val if non-empty, otherwise def.
func OrDefault(val, def string) string {
	if val != "" {
		return val
	}
	return def
}

// titleCase capitalizes the first letter of s (avoids deprecated strings.Title).
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// -- Data Export Setup --

// SetupDataExportSnowflake sets up a Snowflake data export destination.
func SetupDataExportSnowflake(ctx context.Context, reader *bufio.Reader, ld *launchdarkly.Client, pf map[string]string) error {
	fmt.Fprintln(os.Stderr)
	output.Info("Snowflake data export setup")
	output.Info(strings.Repeat("-", 40))

	host := PromptWithDefault(reader, "Snowflake host address", pf["snowflake_host"])
	if host != "" && !strings.HasPrefix(host, "http") {
		host = "https://" + host
	}
	dbName := PromptWithDefault(reader, "Export database name", OrDefault(pf["database"], "LD_EXPORT"))
	whName := PromptWithDefault(reader, "Snowflake warehouse name", OrDefault(pf["snowflake_warehouse"], "LD_EXPORT_WH"))

	output.Info("Generating data export setup script...")
	setupBody := map[string]any{
		"snowflakeHostAddress": host,
		"includeNetworkPolicy": true,
	}
	if dbName != "" {
		setupBody["databaseName"] = dbName
	}
	if whName != "" {
		setupBody["warehouseName"] = whName
	}

	result, err := ld.GenerateDataExportSetup(ctx, "snowflake-v2", setupBody)
	if err != nil {
		return err
	}

	script := jsonutil.GetStr(result, "script")
	pubKey := jsonutil.GetStr(result, "publicKey")
	output.ShowScript(script)
	fmt.Fprintln(os.Stderr)
	WaitForEnter(reader, "Press Enter after running the setup script in Snowflake... ")

	fmt.Fprintln(os.Stderr)
	output.Info("Connection details being tested:")
	output.Info(fmt.Sprintf("  Host:      %s", host))
	output.Info(fmt.Sprintf("  Database:  %s", dbName))
	output.Info(fmt.Sprintf("  Warehouse: %s", whName))
	output.Info(fmt.Sprintf("  PublicKey:  %s...", jsonutil.Truncate(pubKey, 40)))

	const maxRetries = 3
	const retryDelaySec = 15
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt == 1 {
			output.Info("Completing data export setup...")
		} else {
			output.Info(fmt.Sprintf("Retry %d/%d — waiting %ds for Snowflake key propagation...", attempt, maxRetries, retryDelaySec))
			time.Sleep(time.Duration(retryDelaySec) * time.Second)
			output.Info("Retrying complete-setup...")
		}
		_, err = ld.CompleteDataExportSetup(ctx, "snowflake-v2", map[string]any{"publicKey": pubKey})
		if err == nil {
			output.Ok("Snowflake data export configured")
			return nil
		}

		errStr := err.Error()

		if strings.Contains(errStr, "404") {
			output.Warn("Revamped flow not available, using legacy endpoint...")
			body := map[string]any{
				"name": "Data Export",
				"kind": "snowflake-v2",
				"config": map[string]any{
					"snowflakeHostAddress": host,
					"public_key":           pubKey,
				},
				"on": true,
			}
			if dbName != "" {
				body["config"].(map[string]any)["databaseName"] = dbName
			}
			if whName != "" {
				body["config"].(map[string]any)["warehouseName"] = whName
			}
			_, err = ld.CreateDestination(ctx, body)
			if err != nil {
				return err
			}
			output.Ok("Snowflake data export created")
			return nil
		}

		output.Warn(fmt.Sprintf("Attempt %d failed: %s", attempt, errStr))
		if attempt < maxRetries {
			output.Info("Snowflake can take time to propagate RSA key changes. Retrying...")
		}
	}

	output.ErrMsg("All connection test attempts failed.")
	return err
}

// SetupDataExportGeneric provides instructions for non-Snowflake data export setup.
func SetupDataExportGeneric(reader *bufio.Reader, whType string) error {
	output.Info(fmt.Sprintf("%s data export setup", titleCase(whType)))
	output.Info("Please configure data export in the LD UI:")
	output.Info(fmt.Sprintf("  Settings > Integrations > Data Export > %s", titleCase(whType)))
	WaitForEnter(reader, "Press Enter after configuring data export in the LD UI... ")
	return nil
}

// -- Experimentation Integration Setup --

// SetupSnowflake sets up a Snowflake experimentation integration.
func SetupSnowflake(ctx context.Context, reader *bufio.Reader, ld *launchdarkly.Client, projectKey, envKey string, pf map[string]string) error {
	fmt.Fprintln(os.Stderr)
	output.Info("Snowflake experimentation setup")
	output.Info(strings.Repeat("-", 40))
	if len(pf) > 0 {
		output.Info("Pre-filled from your Statsig config:")
	}

	host := PromptWithDefault(reader, "Snowflake host address", pf["snowflake_host"])
	if host != "" && !strings.HasPrefix(host, "http") {
		host = "https://" + host
	}
	dbName := PromptWithDefault(reader, "Metrics database name", OrDefault(pf["database"], "LD_EXPERIMENTATION"))
	schemaName := PromptWithDefault(reader, "Metrics schema name", OrDefault(pf["schema"], "RESULTS"))

	output.Info("Generating Snowflake setup script...")
	setupBody := map[string]any{
		"name": fmt.Sprintf("Snowflake WHN (%s/%s)", projectKey, envKey),
		"configValues": map[string]any{
			"snowflakeHostAddress": host,
			"metricsDatabaseName":  dbName,
			"metricsSchemaName":    schemaName,
			"selectedEnv": map[string]any{
				"projectKey":     projectKey,
				"environmentKey": envKey,
				"environmentId":  pf["_env_id"],
				"projectId":      pf["_project_id"],
			},
		},
	}

	result, err := ld.GenerateSnowflakeSetup(ctx, setupBody)
	if err != nil {
		return err
	}
	configID := jsonutil.GetStr(result, "id")
	if configID == "" {
		configID = jsonutil.GetStr(result, "_id")
	}

	script := jsonutil.GetStr(result, "snowflakeSetupScript")
	output.ShowScript(script)
	fmt.Fprintln(os.Stderr)
	WaitForEnter(reader, "Press Enter after running the setup script in Snowflake... ")

	output.Info("Verifying Snowflake setup...")
	_, err = ld.CompleteSetup(ctx, configID)
	if err != nil {
		return err
	}
	output.Ok("Snowflake experimentation configured")
	return nil
}

// SetupBigQuery sets up a BigQuery experimentation integration.
func SetupBigQuery(ctx context.Context, reader *bufio.Reader, ld *launchdarkly.Client, projectKey, envKey string, pf map[string]string) error {
	fmt.Fprintln(os.Stderr)
	output.Info("BigQuery connection setup")
	output.Info(strings.Repeat("-", 40))

	gcpProject := PromptWithDefault(reader, "GCP project ID", pf["gcp_project"])
	keyPath := PromptWithDefault(reader, "Path to service account JSON key file", "")
	keyPath = strings.TrimSpace(keyPath)

	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("service account key file not found: %s", keyPath)
	}
	var serviceKey any
	if err := json.Unmarshal(raw, &serviceKey); err != nil {
		return fmt.Errorf("invalid JSON in key file: %w", err)
	}
	keyJSON, _ := json.Marshal(serviceKey)

	datasetID := PromptWithDefault(reader, "Results dataset ID", OrDefault(pf["dataset"], "ld_experimentation"))

	output.Info("Creating BigQuery integration...")
	body := map[string]any{
		"name":    fmt.Sprintf("BigQuery WHN (%s/%s)", projectKey, envKey),
		"enabled": true,
		"configValues": map[string]any{
			"gcpProjectId":      gcpProject,
			"serviceAccountKey": string(keyJSON),
			"resultsDatasetId":  datasetID,
			"selectedEnv": map[string]any{
				"projectKey":     projectKey,
				"environmentKey": envKey,
				"environmentId":  pf["_env_id"],
				"projectId":      pf["_project_id"],
			},
		},
		"tags": []string{},
	}
	_, err = ld.CreateIntegrationConfig(ctx, "bigquery-experimentation", body)
	if err != nil {
		return err
	}
	output.Ok("BigQuery connection created")
	return nil
}

// SetupDatabricks sets up a Databricks experimentation integration.
func SetupDatabricks(ctx context.Context, reader *bufio.Reader, ld *launchdarkly.Client, projectKey, envKey string, pf map[string]string) error {
	fmt.Fprintln(os.Stderr)
	output.Info("Databricks connection setup")
	output.Info(strings.Repeat("-", 40))

	host := PromptWithDefault(reader, "Databricks workspace URL", pf["databricks_host"])
	httpPath := PromptWithDefault(reader, "HTTP path", pf["databricks_http_path"])
	catalog := PromptWithDefault(reader, "Unity Catalog name", pf["catalog"])
	schema := PromptWithDefault(reader, "Schema name", pf["schema"])
	token := PromptWithDefault(reader, "Access token (will be visible)", "")

	output.Info("Creating Databricks integration...")
	body := map[string]any{
		"name":    fmt.Sprintf("Databricks WHN (%s/%s)", projectKey, envKey),
		"enabled": true,
		"configValues": map[string]any{
			"databricksHost": host,
			"httpPath":       httpPath,
			"catalog":        catalog,
			"schema":         schema,
			"accessToken":    token,
			"selectedEnv": map[string]any{
				"projectKey":     projectKey,
				"environmentKey": envKey,
				"environmentId":  pf["_env_id"],
				"projectId":      pf["_project_id"],
			},
		},
		"tags": []string{},
	}
	_, err := ld.CreateIntegrationConfig(ctx, "databricks-experimentation", body)
	if err != nil {
		return err
	}
	output.Ok("Databricks connection created")
	return nil
}

// SetupRedshift sets up a Redshift experimentation integration.
func SetupRedshift(ctx context.Context, reader *bufio.Reader, ld *launchdarkly.Client, projectKey, envKey string, pf map[string]string) error {
	fmt.Fprintln(os.Stderr)
	output.Info("Redshift connection setup")
	output.Info(strings.Repeat("-", 40))

	endpoint := PromptWithDefault(reader, "Cluster endpoint", pf["redshift_host"])
	identifier := PromptWithDefault(reader, "Cluster identifier", "")
	region := PromptWithDefault(reader, "AWS region", "")
	accountID := PromptWithDefault(reader, "AWS account ID", "")
	roleArn := PromptWithDefault(reader, "IAM role ARN", "")
	dbName := PromptWithDefault(reader, "Metrics database name", pf["database"])
	schemaName := PromptWithDefault(reader, "Metrics schema name", pf["schema"])

	output.Info("Generating Redshift setup scripts...")
	setupBody := map[string]any{
		"name": fmt.Sprintf("Redshift WHN (%s/%s)", projectKey, envKey),
		"configValues": map[string]any{
			"clusterEndpoint":     endpoint,
			"clusterIdentifier":   identifier,
			"clusterRegion":       region,
			"clusterAwsAccountId": accountID,
			"iamRoleArn":          roleArn,
			"metricsDatabaseName": dbName,
			"metricsSchemaName":   schemaName,
			"selectedEnv": map[string]any{
				"projectKey":     projectKey,
				"environmentKey": envKey,
				"environmentId":  pf["_env_id"],
				"projectId":      pf["_project_id"],
			},
		},
	}

	result, err := ld.GenerateRedshiftSetup(ctx, setupBody)
	if err != nil {
		return err
	}
	configID := jsonutil.GetStr(result, "id")
	if configID == "" {
		configID = jsonutil.GetStr(result, "_id")
	}

	fmt.Fprintln(os.Stderr)
	output.Info("Setup scripts have been generated.")
	output.Info("Please run the SQL scripts and apply the IAM policy.")
	fmt.Fprintln(os.Stderr)
	WaitForEnter(reader, "Press Enter after completing the Redshift setup steps... ")

	output.Info("Verifying Redshift setup...")
	_, err = ld.CompleteSetup(ctx, configID)
	if err != nil {
		return err
	}
	output.Ok("Redshift connection configured")
	return nil
}
