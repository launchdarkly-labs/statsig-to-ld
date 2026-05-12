package cmd

import (
	"context"
	"log"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "statsig-to-ld",
	Short:   "Migrate Statsig projects to LaunchDarkly",
	Version: version,
	Long: `A CLI tool for migrating from Statsig to LaunchDarkly.

Subcommands:
  analyze            Read-only sizing report for a Statsig project before importing
  metrics convert    Convert Statsig metric definitions to LaunchDarkly metrics

Re-running any subcommand is safe — existing LD resources are detected and skipped.`,
}

func init() {
	// Disable default log timestamp — cleaner CLI output
	log.SetFlags(0)
}

// ExecuteContext runs the root command with the given context.
func ExecuteContext(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
}
