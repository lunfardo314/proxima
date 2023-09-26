package api

import (
	"os"
	"strconv"

	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initMakeChainCmd(apiCmd *cobra.Command) {
	getMakeChainCmd := &cobra.Command{
		Use:   "mkchain [<initial on-chain balance>]",
		Short: `creates new chain origin`,
		Args:  cobra.MaximumNArgs(1),
		Run:   runMakeChainCmd,
	}

	getMakeChainCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getMakeChainCmd)
}

const (
	defaultOnChainAmount = uint64(1_000_000)
	defaultTagAlongFee   = 500
	minimumFee           = defaultTagAlongFee
)

func runMakeChainCmd(_ *cobra.Command, args []string) {
	wallet := glb.GetWalletData()
	target := glb.MustGetTarget()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	tagAlongFee := viper.GetUint64("tag-along.fee")
	if tagAlongFee < minimumFee {
		tagAlongFee = defaultTagAlongFee
	}
	var err error
	onChainAmount := defaultOnChainAmount
	if len(args) == 1 {
		onChainAmount, err = strconv.ParseUint(args[0], 10, 64)
		glb.AssertNoError(err)
	}

	glb.Infof("Creating new chain origin:")
	glb.Infof("   on-chain balance: %s", util.GoThousands(onChainAmount))
	glb.Infof("   tag-along fee %s to the sequencer %s", util.GoThousands(tagAlongFee), tagAlongSeqID)
	glb.Infof("   source account: %s", wallet.Account)
	glb.Infof("   total cost: %s", util.GoThousands(onChainAmount+tagAlongFee))
	glb.Infof("   chain controller: %s", target)

	if !glb.YesNoPrompt("proceed?:", false) {
		glb.Infof("exit")
		os.Exit(0)
	}
}
