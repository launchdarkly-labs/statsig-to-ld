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
	"github.com/launchdarkly-labs/statsig-to-ld/internal/report"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/statsig"
	"github.com/launchdarkly-labs/statsig-to-ld/internal/targeting"
	"github.com/spf13/cobra"
)

// Names of D8 lossy features that --accept-data-loss can opt into.
const (
	lossySegments         = "segments"
	lossyPrerequisites    = "prerequisites"
	lossyCustomUnitID     = "custom_unit_id"
	lossyUnreachableRules = "unreachable_rules"
	lossyMultiVariantDC   = "multi_variant_overrides"

	acceptAllSentinel = "all"
)

var targetingImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Apply Statsig targeting to LaunchDarkly flag shells",
	Long: `Fetch Statsig gates/dynamic configs and apply their targeting rules,
rollouts, and per-user overrides to the matching LaunchDarkly flag shells
(by sanitized key). Flag shells must already exist — run "flags import"
first.

D8 fail-closed behavior: sources whose targeting cannot be faithfully
reproduced (passes_segment / fails_segment / passes_gate / fails_gate /
custom unit_id; multi-variant DC overrides) are SKIPPED by default. To
opt in, pass --accept-data-loss with the feature names you accept, or
--accept-data-loss=all to accept everything.

Examples:
  # Strict: skip any flag whose source has lossy targeting
  statsig-to-ld targeting import --all \
    --statsig-key console-... --ld-key api-... --ld-project my-project

  # Accept all lossy features (matches the goaltender worker's behavior)
  statsig-to-ld targeting import --all --accept-data-loss=all \
    --statsig-key console-... --ld-key api-... --ld-project my-project

  # Accept only specific lossy features
  statsig-to-ld targeting import --all \
    --accept-data-loss=segments,unreachable_rules \
    --statsig-key console-... --ld-key api-... --ld-project my-project

  # Don't auto-create missing LD envs (degrades to "unreachable" for those)
  statsig-to-ld targeting import --all --no-create-envs \
    --statsig-key console-... --ld-key api-... --ld-project my-project`,
	RunE: runTargetingImport,
}

var (
	flagTIAll              bool
	flagTIDryRun           bool
	flagTIImportType       string
	flagTIIncludeTag       string
	flagTILDTag            string
	flagTIAcceptDataLoss   string
	flagTINoCreateEnvs     bool
	flagTIStatsigKey       string
	flagTIStatsigURL       string
	flagTILDKey            string
	flagTILDURL            string
	flagTILDProject        string
	flagTIOutput           string
	flagTIFormat           string
	flagTIVerbose          bool
)

func init() {
	targetingCmd.AddCommand(targetingImportCmd)

	targetingImportCmd.Flags().BoolVar(&flagTIAll, "all", false, "Apply targeting to all flags tagged --ld-tag (currently the only mode)")
	targetingImportCmd.Flags().BoolVar(&flagTIDryRun, "dry-run", false, "Build the patch ops without sending them to LaunchDarkly")
	targetingImportCmd.Flags().StringVar(&flagTIImportType, "import-type", importTypeBoth, "What source to read: gates | dynamic-configs | both")
	targetingImportCmd.Flags().StringVar(&flagTIIncludeTag, "include-tag", "", "Only consider Statsig sources with this tag (single value)")
	targetingImportCmd.Flags().StringVar(&flagTILDTag, "ld-tag", defaultLDImportTag, "LD tag identifying flags previously created by 'flags import'")
	targetingImportCmd.Flags().StringVar(&flagTIAcceptDataLoss, "accept-data-loss", "",
		"Opt in to importing flags whose targeting will lose information. Use \"all\" to accept everything, or a comma-separated list: segments, prerequisites, custom_unit_id, unreachable_rules, multi_variant_overrides")
	targetingImportCmd.Flags().BoolVar(&flagTINoCreateEnvs, "no-create-envs", false, "Do not auto-create missing LD environments; mark them unreachable instead")

	targetingImportCmd.Flags().StringVar(&flagTIStatsigKey, "statsig-key", "", "Statsig Console API key (console-xxx)")
	targetingImportCmd.Flags().StringVar(&flagTIStatsigURL, "statsig-url", "", "Statsig API base URL override")
	targetingImportCmd.Flags().StringVar(&flagTILDKey, "ld-key", "", "LaunchDarkly API access token (api-xxx)")
	targetingImportCmd.Flags().StringVar(&flagTILDURL, "ld-url", "", "LaunchDarkly API base URL override")
	targetingImportCmd.Flags().StringVar(&flagTILDProject, "ld-project", "", "LaunchDarkly project key (required — even for --dry-run, since the dry-run reads existing LD flags + environments)")

	targetingImportCmd.Flags().StringVar(&flagTIOutput, "output", "targeting-import-report.json", "Path for the migration report")
	targetingImportCmd.Flags().StringVar(&flagTIFormat, "format", "json", "Report format: json or csv")
	targetingImportCmd.Flags().BoolVarP(&flagTIVerbose, "verbose", "v", false, "Show detailed per-flag progress")
}

func runTargetingImport(cmd *cobra.Command, args []string) error {
	httputil.SetVersion(version)

	if flagTIStatsigKey == "" {
		flagTIStatsigKey = os.Getenv("STATSIG_CONSOLE_KEY")
	}
	if flagTILDKey == "" {
		flagTILDKey = os.Getenv("LD_API_KEY")
	}

	if !flagTIAll {
		return errors.New("--all is required")
	}
	switch flagTIImportType {
	case importTypeGates, importTypeDynamicConfigs, importTypeBoth:
	default:
		return fmt.Errorf("--import-type must be one of %q, %q, %q (got %q)",
			importTypeGates, importTypeDynamicConfigs, importTypeBoth, flagTIImportType)
	}
	if flagTIFormat != "json" && flagTIFormat != "csv" {
		return fmt.Errorf(`--format must be "json" or "csv" (got %q)`, flagTIFormat)
	}

	acceptedLossy, acceptAll, err := parseAcceptDataLoss(flagTIAcceptDataLoss)
	if err != nil {
		return err
	}

	if flagTIStatsigKey == "" {
		k, err := promptForKey("Statsig Console API key (console-xxx)")
		if err != nil {
			return fmt.Errorf("reading Statsig key: %w", err)
		}
		flagTIStatsigKey = k
	}
	if flagTIStatsigKey == "" {
		return errors.New("Statsig Console API key is required")
	}
	if !strings.HasPrefix(flagTIStatsigKey, "console-") {
		return errors.New(`Statsig key should start with "console-"`)
	}

	// LD credentials are required even in --dry-run mode: we still need to
	// read existing LD flags (for matching) and existing LD environments
	// (for env reconciliation) before building the patch ops we'd otherwise
	// send. Without these reads, "dry-run" couldn't produce a meaningful
	// plan.
	if flagTILDKey == "" {
		k, err := promptForKey("LaunchDarkly API access token (api-xxx)")
		if err != nil {
			return fmt.Errorf("reading LD key: %w", err)
		}
		flagTILDKey = k
	}
	if flagTILDKey == "" {
		return errors.New("LD API access token is required (set LD_API_KEY env, use --ld-key, or enter at prompt — required even for --dry-run)")
	}
	if !strings.HasPrefix(flagTILDKey, "api-") {
		return errors.New(`LD key should start with "api-"`)
	}
	if flagTILDProject == "" {
		return errors.New("--ld-project is required (even for --dry-run, since the dry-run reads existing LD flags and environments)")
	}

	flagTIStatsigURL = strings.TrimRight(flagTIStatsigURL, "/")
	flagTILDURL = strings.TrimRight(flagTILDURL, "/")
	if flagTIFormat == "csv" && strings.HasSuffix(flagTIOutput, ".json") {
		flagTIOutput = strings.TrimSuffix(flagTIOutput, ".json") + ".csv"
	}

	ctx := cmd.Context()
	sg := statsig.NewClient(flagTIStatsigKey, flagTIStatsigURL)
	ld := launchdarkly.NewClient(flagTILDKey, flagTILDProject, flagTILDURL)

	rpt := report.NewTargetingReport()

	// 1. Fetch sources from Statsig
	var (
		gates []statsig.Gate
		dcs   []statsig.DynamicConfig
	)
	if flagTIImportType == importTypeGates || flagTIImportType == importTypeBoth {
		log.Println("Fetching Statsig gates...")
		gates, err = sg.ListGates(ctx)
		if err != nil {
			return fmt.Errorf("listing gates: %w", err)
		}
		if flagTIIncludeTag != "" {
			gates = statsig.FilterGates(gates, flagTIIncludeTag)
		}
		log.Printf("Fetched %d gates", len(gates))
	}
	if flagTIImportType == importTypeDynamicConfigs || flagTIImportType == importTypeBoth {
		log.Println("Fetching Statsig dynamic configs...")
		dcs, err = sg.ListDynamicConfigs(ctx)
		if err != nil {
			return fmt.Errorf("listing dynamic configs: %w", err)
		}
		if flagTIIncludeTag != "" {
			dcs = statsig.FilterDynamicConfigs(dcs, flagTIIncludeTag)
		}
		log.Printf("Fetched %d dynamic configs", len(dcs))
	}

	// 2. Apply D8 fail-closed filtering
	gatesAccepted, gatesRefused := splitGatesByD8(gates, acceptedLossy, acceptAll)
	dcsAccepted, dcsRefused := splitDCsByD8(dcs, acceptedLossy, acceptAll)
	for _, g := range gatesRefused {
		rpt.AddSkippedLossy("gate:"+g.ID, analyze.LossyTargetingFeatures(g))
	}
	for _, c := range dcsRefused {
		rpt.AddSkippedLossy("dynamic_config:"+c.ID, analyze.LossyDCTargetingFeatures(c))
	}
	if n := len(gatesRefused) + len(dcsRefused); n > 0 && !flagTIVerbose {
		log.Printf("D8: %d source(s) skipped due to lossy targeting. Re-run with --accept-data-loss to include them.", n)
	}

	// 3. Build plan (ld is guaranteed non-nil by the credential check above)
	plan, err := targeting.BuildPlan(ctx, sg, ld, targeting.PlanInputs{
		Gates:          gatesAccepted,
		DynamicConfigs: dcsAccepted,
		LDTag:          flagTILDTag,
		NoCreateEnvs:   flagTINoCreateEnvs,
	})
	if err != nil {
		return fmt.Errorf("building targeting plan: %w", err)
	}
	for _, n := range plan.BuildNotes {
		rpt.AddGlobalNote(n.Severity, fmt.Sprintf("[%s] %s", n.FlagKey, n.Message))
	}

	// 4. Fetch existing LD flag shells (filtered by import tag)
	log.Printf("Fetching LaunchDarkly flags tagged %q...", flagTILDTag)
	ldFlags, err := ld.ListAllFlags(ctx, flagTILDTag)
	if err != nil {
		return fmt.Errorf("listing LD flags: %w", err)
	}
	log.Printf("Found %d flags in LD tagged %q", len(ldFlags), flagTILDTag)

	// 5. Apply
	results := plan.Apply(ctx, ld, ldFlags, flagTIDryRun)
	for _, r := range results {
		notes := convertNotes(r.Notes)
		switch r.Status {
		case targeting.StatusApplied:
			rpt.AddApplied(r.FlagKey, notes)
			if flagTIVerbose {
				log.Printf("OK     %s", r.FlagKey)
			} else {
				fmt.Fprint(os.Stderr, ".")
			}
		case targeting.StatusSkippedDryRun:
			rpt.AddSkippedDryRun(r.FlagKey, notes)
			if flagTIVerbose {
				log.Printf("DRY    %s", r.FlagKey)
			} else {
				fmt.Fprint(os.Stderr, "D")
			}
		case targeting.StatusSkippedNoSource:
			rpt.AddSkippedNoSource(r.FlagKey)
			if flagTIVerbose {
				log.Printf("NOSRC  %s (no matching Statsig source)", r.FlagKey)
			} else {
				fmt.Fprint(os.Stderr, "N")
			}
		case targeting.StatusFailed:
			rpt.AddFailed(r.FlagKey, r.Error, notes)
			if flagTIVerbose {
				log.Printf("FAIL   %s  %s", r.FlagKey, r.Error)
			} else {
				fmt.Fprint(os.Stderr, "X")
			}
		}
	}
	if len(results) > 0 && !flagTIVerbose {
		fmt.Fprintln(os.Stderr)
	}

	// FlagsConsidered must include sources that were D8-refused before plan
	// build — they're already in rpt.Flags as `skipped_lossy` entries but
	// aren't in ldFlags. Without this sum the summary table would understate
	// what was processed.
	rpt.Finalize(len(ldFlags) + len(gatesRefused) + len(dcsRefused))

	if err := writeTargetingReport(rpt); err != nil {
		return err
	}
	rpt.PrintSummaryTable(os.Stdout)
	fmt.Printf("Report written to %s\n", flagTIOutput)
	return nil
}

// parseAcceptDataLoss parses the --accept-data-loss value into a set of
// accepted feature names. The literal "all" returns acceptAll=true.
func parseAcceptDataLoss(raw string) (set map[string]bool, acceptAll bool, err error) {
	set = map[string]bool{}
	if raw == "" {
		return set, false, nil
	}
	if raw == acceptAllSentinel {
		return set, true, nil
	}
	valid := map[string]bool{
		lossySegments:         true,
		lossyPrerequisites:    true,
		lossyCustomUnitID:     true,
		lossyUnreachableRules: true,
		lossyMultiVariantDC:   true,
	}
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !valid[v] {
			return nil, false, fmt.Errorf("unknown --accept-data-loss feature %q (valid: segments, prerequisites, custom_unit_id, unreachable_rules, multi_variant_overrides, all)", v)
		}
		set[v] = true
	}
	return set, false, nil
}

func splitGatesByD8(gates []statsig.Gate, accepted map[string]bool, acceptAll bool) (acceptedGates, refused []statsig.Gate) {
	for _, g := range gates {
		lossy := analyze.LossyTargetingFeatures(g)
		if len(lossy) == 0 || acceptAll || allAccepted(lossy, accepted) {
			acceptedGates = append(acceptedGates, g)
		} else {
			refused = append(refused, g)
		}
	}
	return
}

func splitDCsByD8(dcs []statsig.DynamicConfig, accepted map[string]bool, acceptAll bool) (acceptedDCs, refused []statsig.DynamicConfig) {
	for _, c := range dcs {
		lossy := analyze.LossyDCTargetingFeatures(c)
		if len(lossy) == 0 || acceptAll || allAccepted(lossy, accepted) {
			acceptedDCs = append(acceptedDCs, c)
		} else {
			refused = append(refused, c)
		}
	}
	return
}

func allAccepted(features []string, accepted map[string]bool) bool {
	for _, f := range features {
		if !accepted[f] {
			return false
		}
	}
	return true
}

func convertNotes(notes []targeting.Note) []report.TargetingNote {
	out := make([]report.TargetingNote, 0, len(notes))
	for _, n := range notes {
		out = append(out, report.TargetingNote{Severity: n.Severity, Message: n.Message})
	}
	return out
}

func writeTargetingReport(rpt *report.TargetingReport) error {
	var data []byte
	if flagTIFormat == "csv" {
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
	if err := os.WriteFile(flagTIOutput, data, 0600); err != nil {
		return fmt.Errorf("writing report to %s: %w", flagTIOutput, err)
	}
	return nil
}

