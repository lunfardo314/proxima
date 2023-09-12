package setup

import (
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Init adds all commands in the package to the root command
func Init(rootCmd *cobra.Command) {
	rootCmd.AddCommand(initInitCmd())
	rootCmd.AddCommand(initConfigSetCmd())
	rootCmd.AddCommand(initSetKeyCmd())
}

func initInitCmd() *cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init [-c profile]",
		Args:  cobra.NoArgs,
		Short: "initializes an empty config profile",
		Run:   runInitCommand,
	}
	return initCmd
}

const minimumSeedLength = 8

func runInitCommand(_ *cobra.Command, _ []string) {
	console.NoError(viper.SafeWriteConfig())
}
