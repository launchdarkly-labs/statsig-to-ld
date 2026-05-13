package cmd

import (
	"github.com/spf13/cobra"
)

var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Commands for migrating Statsig metrics to LaunchDarkly",
	Long:  `Commands for migrating Statsig metric definitions to LaunchDarkly.`,
}

func init() {
	rootCmd.AddCommand(metricsCmd)
}
