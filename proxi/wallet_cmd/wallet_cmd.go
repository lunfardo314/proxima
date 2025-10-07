package wallet_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func InitWalletCmd() *cobra.Command {
	walletCmd := &cobra.Command{
		Use:   "wallet",
		Short: "displays wallet config",
		Args:  cobra.NoArgs,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runWalletCmd,
	}
	walletCmd.InitDefaultHelpCmd()
	return walletCmd
}

func runWalletCmd(_ *cobra.Command, _ []string) {
	glb.GetClient()
	walletData := glb.GetWalletData()
	glb.Infof("")
	glb.Infof("wallet address:             %s", walletData.Account.String())
	glb.Infof("actual tag-along sequencer: %s", glb.GetTagAlongSequencerID(true).String())
	glb.Infof("tag-along fee:              %d", glb.GetTagAlongFee())
}
