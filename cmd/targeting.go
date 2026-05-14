package cmd

import (
	"github.com/spf13/cobra"
)

var targetingCmd = &cobra.Command{
	Use:   "targeting",
	Short: "Commands for applying Statsig targeting rules to existing LaunchDarkly flag shells",
	Long:  `Commands for applying Statsig targeting rules, rollouts, and overrides to LaunchDarkly flag shells created by "flags import".`,
}

func init() {
	rootCmd.AddCommand(targetingCmd)
}
