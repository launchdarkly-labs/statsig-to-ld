package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/analyze"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/httputil"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
	"github.com/spf13/cobra"
)

var analyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "Survey a Statsig project and print a pre-migration sizing report",
	Long: `Read-only survey of a Statsig project. Counts gates, dynamic configs,
environments, and metrics, and classifies each by how the importer will
treat it under D8 (fail-closed on lossy targeting transformations).

Writes nothing to Statsig or LaunchDarkly. Safe to run repeatedly.

The --ld-key and --ld-project flags are optional. When both are provided,
the report includes an environment-mapping preview (which Statsig envs
would be auto-created in LD on import). Without them, the report covers
the Statsig side only.

Examples:
  # Statsig-side analysis only (no LD account needed yet)
  statsig-to-ld analyze --statsig-key console-...

  # Full analysis including LD env mapping preview
  statsig-to-ld analyze \
    --statsig-key console-... --ld-key api-... --ld-project my-project

  # Save the structured report to a JSON file alongside the table
  statsig-to-ld analyze --statsig-key console-... --output analyze-report.json`,
	RunE: runAnalyze,
}

var (
	flagAnalyzeStatsigKey string
	flagAnalyzeStatsigURL string
	flagAnalyzeLDKey      string
	flagAnalyzeLDURL      string
	flagAnalyzeLDProject  string
	flagAnalyzeOutput     string
	flagAnalyzeIncludeTag string
)

func init() {
	rootCmd.AddCommand(analyzeCmd)

	analyzeCmd.Flags().StringVar(&flagAnalyzeStatsigKey, "statsig-key", "", "Statsig Console API key (console-xxx)")
	analyzeCmd.Flags().StringVar(&flagAnalyzeStatsigURL, "statsig-url", "", "Statsig API base URL override (e.g. https://statsigapi.net/console/v1)")
	analyzeCmd.Flags().StringVar(&flagAnalyzeLDKey, "ld-key", "", "LaunchDarkly API access token (api-xxx). Optional; enables env mapping preview when provided.")
	analyzeCmd.Flags().StringVar(&flagAnalyzeLDURL, "ld-url", "", "LaunchDarkly API base URL override")
	analyzeCmd.Flags().StringVar(&flagAnalyzeLDProject, "ld-project", "", "LaunchDarkly project key. Required if --ld-key is set.")
	analyzeCmd.Flags().StringVar(&flagAnalyzeOutput, "output", "", "Write structured JSON report to this path in addition to the table")
	analyzeCmd.Flags().StringVar(&flagAnalyzeIncludeTag, "include-tag", "", "Only include Statsig gates and dynamic configs with this tag (single value, not comma-separated)")
}

func runAnalyze(cmd *cobra.Command, args []string) error {
	httputil.SetVersion(version)
	ctx := cmd.Context()

	if flagAnalyzeStatsigKey == "" {
		flagAnalyzeStatsigKey = os.Getenv("STATSIG_CONSOLE_KEY")
	}
	if flagAnalyzeLDKey == "" {
		flagAnalyzeLDKey = os.Getenv("LD_API_KEY")
	}

	if flagAnalyzeStatsigKey == "" {
		key, err := promptForKey("Statsig Console API key (console-xxx)")
		if err != nil {
			return fmt.Errorf("reading Statsig key: %w", err)
		}
		flagAnalyzeStatsigKey = key
	}
	if flagAnalyzeStatsigKey == "" {
		return errors.New("Statsig Console API key is required (set STATSIG_CONSOLE_KEY env, use --statsig-key, or enter at prompt)")
	}
	if !strings.HasPrefix(flagAnalyzeStatsigKey, "console-") {
		return errors.New(`Statsig key should start with "console-" — this is a Console API key, not a server secret key`)
	}

	// LD key is optional. If one is provided, project must also be.
	if flagAnalyzeLDKey != "" {
		if !strings.HasPrefix(flagAnalyzeLDKey, "api-") {
			return errors.New(`LaunchDarkly key should start with "api-" — this is an API access token`)
		}
		if flagAnalyzeLDProject == "" {
			return errors.New("--ld-project is required when --ld-key is set")
		}
	}

	flagAnalyzeLDURL = strings.TrimRight(flagAnalyzeLDURL, "/")
	flagAnalyzeStatsigURL = strings.TrimRight(flagAnalyzeStatsigURL, "/")
	if flagAnalyzeLDURL != "" && !strings.HasPrefix(flagAnalyzeLDURL, "https://") && !strings.HasPrefix(flagAnalyzeLDURL, "http://") {
		return fmt.Errorf("--ld-url must include the scheme (e.g. https://%s)", flagAnalyzeLDURL)
	}
	if flagAnalyzeStatsigURL != "" && !strings.HasPrefix(flagAnalyzeStatsigURL, "https://") && !strings.HasPrefix(flagAnalyzeStatsigURL, "http://") {
		return fmt.Errorf("--statsig-url must include the scheme (e.g. https://%s)", flagAnalyzeStatsigURL)
	}

	sgClient := statsig.NewClient(flagAnalyzeStatsigKey, flagAnalyzeStatsigURL)

	log.Println("Fetching Statsig gates...")
	gates, err := sgClient.ListGates(ctx)
	if err != nil {
		return fmt.Errorf("listing gates: %w", err)
	}

	log.Println("Fetching Statsig dynamic configs...")
	dcs, err := sgClient.ListDynamicConfigs(ctx)
	if err != nil {
		return fmt.Errorf("listing dynamic configs: %w", err)
	}

	log.Println("Fetching Statsig environments...")
	statsigEnvs, err := sgClient.ListEnvironments(ctx)
	if err != nil {
		return fmt.Errorf("listing environments: %w", err)
	}

	log.Println("Fetching Statsig metrics...")
	metrics, err := sgClient.ListAllMetrics(ctx)
	if err != nil {
		return fmt.Errorf("listing metrics: %w", err)
	}

	// Apply tag filter to gates and DCs if requested. Metrics use a separate
	// tag scheme and are not filtered here.
	if flagAnalyzeIncludeTag != "" {
		gates = statsig.FilterGates(gates, flagAnalyzeIncludeTag)
		dcs = statsig.FilterDynamicConfigs(dcs, flagAnalyzeIncludeTag)
		log.Printf("Filtered to tag %q: %d gates, %d dynamic configs", flagAnalyzeIncludeTag, len(gates), len(dcs))
	}

	// LD-side env preview is optional.
	var ldEnvs []launchdarkly.Environment
	if flagAnalyzeLDKey != "" {
		log.Println("Fetching LaunchDarkly environments...")
		ldClient := launchdarkly.NewClient(flagAnalyzeLDKey, flagAnalyzeLDProject, flagAnalyzeLDURL)
		ldEnvs, err = ldClient.ListEnvironments(ctx)
		if err != nil {
			return fmt.Errorf("listing LD environments: %w", err)
		}
	}

	report := analyze.Build(flagAnalyzeLDProject, gates, dcs, statsigEnvs, ldEnvs, metrics)

	report.PrintTable(os.Stdout)

	if flagAnalyzeOutput != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling JSON report: %w", err)
		}
		if err := os.WriteFile(flagAnalyzeOutput, data, 0600); err != nil {
			return fmt.Errorf("writing report to %s: %w", flagAnalyzeOutput, err)
		}
		fmt.Printf("Report written to %s\n", flagAnalyzeOutput)
	}

	return nil
}
