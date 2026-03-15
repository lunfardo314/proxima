package multispam_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func Init() *cobra.Command {
	multispamCmd := &cobra.Command{
		Use:   "multispam [<subcommand>]",
		Short: "multi-sender spammer for TPS testing",
		Args:  cobra.NoArgs,
	}

	multispamCmd.PersistentFlags().StringP("config", "c", "", "proxi config profile name (for fund/init only)")
	err := viper.BindPFlag("config", multispamCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	multispamCmd.PersistentFlags().String("multispam-config", "multispam.yaml", "multispam config file")
	err = viper.BindPFlag("multispam-config", multispamCmd.PersistentFlags().Lookup("multispam-config"))
	glb.AssertNoError(err)

	multispamCmd.InitDefaultHelpCmd()
	multispamCmd.AddCommand(
		initInitCmd(),
		initInfoCmd(),
		initFundCmd(),
		initRunCmd(),
	)
	return multispamCmd
}
