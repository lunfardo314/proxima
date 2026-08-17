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
	walletAccount := glb.GetWalletAccount()
	glb.Infof("")
	glb.Infof("wallet address:             %s", walletAccount.String())
	glb.Infof("actual tag-along sequencer: %s", glb.GetTagAlongSequencerID(true).String())
	// preference only; the fee actually paid is the larger of this and the
	// minimum the target sequencer declares
	glb.Infof("tag-along fee preference:   %d", glb.GetTagAlongFee())
}
