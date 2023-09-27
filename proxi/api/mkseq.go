package api

import (
	"github.com/spf13/cobra"
)

func initMakeSeqCmd(apiCmd *cobra.Command) {
	getMakeSeqCmd := &cobra.Command{
		Use:   "mkseq <chainID> [options]",
		Short: `adds sequencer constraint to the existing chain output and makes chain a sequencer`,
		Args:  cobra.ExactArgs(1),
		Run:   runMakeSeqCmd,
	}

	getMakeSeqCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getMakeSeqCmd)
}

func runMakeSeqCmd(_ *cobra.Command, args []string) {
	//wallet := glb.GetWalletData()
	//target := glb.MustGetTarget()

	//tagAlongSeqID := glb.GetTagAlongSequencerID()
	//glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	//tagAlongFee := viper.GetUint64("tag-along.fee")
	//if tagAlongFee < minimumFee {
	//	tagAlongFee = defaultTagAlongFee
	//}
	//var err error
	//onChainAmount := defaultOnChainAmount
	//if len(args) == 1 {
	//	onChainAmount, err = strconv.ParseUint(args[0], 10, 64)
	//	glb.AssertNoError(err)
	//}
}
