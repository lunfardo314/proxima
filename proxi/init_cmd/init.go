package init_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func CmdInit() *cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init",
		Args:  cobra.NoArgs,
		Short: "specifies initialization subcommands",
		Run: func(cmd *cobra.Command, args []string) {
		},
	}
	initCmd.AddCommand(
		initProfileCmd(),
		initIDCmd(),
		initGenesisDBCmd(),
		initNodeConfigCmd(),
	)
	initCmd.InitDefaultHelpCmd()
	return initCmd
}

func readInConfig() {
	configName := viper.GetString("config")
	if configName == "" {
		configName = "proxi"
	}
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	viper.SetConfigName(configName)
	viper.SetConfigFile("./" + configName + ".yaml")

	viper.AutomaticEnv() // read in environment variables that match

	_ = viper.ReadInConfig()
	glb.Infof("using profile: %s", viper.ConfigFileUsed())
}
