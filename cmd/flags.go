package cmd

import (
	"github.com/spf13/cobra"
)

var flagsCmd = &cobra.Command{
	Use:   "flags",
	Short: "Commands for importing Statsig flags into LaunchDarkly",
	Long:  `Commands for importing Statsig feature gates and dynamic configs into LaunchDarkly as flag shells. Targeting rules are applied separately via "targeting import".`,
}

func init() {
	rootCmd.AddCommand(flagsCmd)
}
