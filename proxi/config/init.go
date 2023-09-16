package config

import (
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var ConfigName string

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
	var configFname string
	if ConfigName == "" {
		configFname = "proxi.yaml"
	} else {
		configFname = ConfigName + ".yaml"
	}
	console.AssertNoError(viper.SafeWriteConfigAs(configFname))
}
