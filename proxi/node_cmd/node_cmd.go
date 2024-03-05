package node_cmd

import (
	"time"

	"github.com/lunfardo314/proxima/core/inclusion"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/proxi/node_cmd/seq_cmd"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func Init() *cobra.Command {
	nodeCmd := &cobra.Command{
		Use:   "node [<subcommand>]",
		Short: "specifies node API subcommand",
		Args:  cobra.NoArgs,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
	}

	nodeCmd.PersistentFlags().StringP("config", "c", "", "proxi config profile name")
	err := viper.BindPFlag("config", nodeCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	nodeCmd.PersistentFlags().String("private_key", "", "ED25519 private key (hex encoded)")
	err = viper.BindPFlag("private_key", nodeCmd.PersistentFlags().Lookup("private_key"))
	glb.AssertNoError(err)

	nodeCmd.PersistentFlags().String("api.endpoint", "", "<DNS name>:port")
	err = viper.BindPFlag("api.endpoint", nodeCmd.PersistentFlags().Lookup("api.endpoint"))
	glb.AssertNoError(err)

	nodeCmd.PersistentFlags().BoolP("nowait", "n", false, "do not wait for inclusion")
	err = viper.BindPFlag("nowait", nodeCmd.PersistentFlags().Lookup("nowait"))
	glb.AssertNoError(err)

	nodeCmd.InitDefaultHelpCmd()
	nodeCmd.AddCommand(
		initGetOutputsCmd(),
		initGetChainOutputCmd(),
		initCompactOutputsCmd(),
		initBalanceCmd(),
		initTransferCmd(),
		initSpamCmd(),
		initMakeChainCmd(),
		initChainsCmd(),
		//initSyncInfoCmd(),
		initNodeInfoCmd(),
		seq_cmd.Init(),
	)

	//node_cmd.Init(nodeCmd) ????

	return nodeCmd
}

func displayTotals(outs []*ledger.OutputWithID) {
	var sumOnChains, sumOutsideChains uint64
	var numChains, numNonChains int

	for _, o := range outs {
		if _, idx := o.Output.ChainConstraint(); idx != 0xff {
			numChains++
			sumOnChains += o.Output.Amount()
		} else {
			numNonChains++
			sumOutsideChains += o.Output.Amount()
		}
	}
	if numNonChains > 0 {
		glb.Infof("amount controlled on %d non-chain outputs: %s", numNonChains, util.GoTh(sumOutsideChains))
	}
	if numChains > 0 {
		glb.Infof("amount controlled on %d chain outputs: %s", numChains, util.GoTh(sumOnChains))
	}
	glb.Infof("TOTAL controlled on %d outputs: %s", numChains+numNonChains, util.GoTh(sumOnChains+sumOutsideChains))
}

func getTagAlongFee() uint64 {
	return viper.GetUint64("tag_along.fee")
}

func GetTagAlongSequencerID() *ledger.ChainID {
	seqIDStr := viper.GetString("tag_along.sequencer_id")
	if seqIDStr == "" {
		return nil
	}
	ret, err := ledger.ChainIDFromHexString(seqIDStr)
	glb.AssertNoError(err)

	o, err := glb.GetClient().GetChainOutputData(ret)
	glb.AssertNoError(err)
	glb.Assertf(o.ID.IsSequencerTransaction(), "can't get tag-along sequencer %s: chain output %s is not a sequencer output",
		ret.StringShort(), o.ID.StringShort())

	return &ret
}

func NoWait() bool {
	return viper.GetBool("nowait")
}

func ReportTxStatus(txid ledger.TransactionID, poll time.Duration, stopFun ...func(status *vertex.TxIDStatus, inclusionData map[ledger.ChainID]tippool.TxInclusion) bool) {
	stop := func(_ *vertex.TxIDStatus, inclusionData map[ledger.ChainID]tippool.TxInclusion) bool {
		percTotal, percDominating := inclusion.ScoreLatestSlot(inclusionData)
		return percTotal == 100 && percDominating == 100
	}
	if len(stopFun) > 0 {
		stop = stopFun[0]
	}

	glb.Infof("Transaction %s (hex=%s):", txid.String(), txid.StringHex())
	for {
		vertexStatus, inclusionData, err := glb.GetClient().QueryTxIDStatus(txid)
		glb.AssertNoError(err)
		glb.Infof(vertexStatus.Lines().Join(", "))
		if len(inclusionData) > 0 {
			_, incl := inclusion.InLatestSlot(inclusionData)
			percTotal, percDominating := incl.Score()
			glb.Infof("   total branches: %d, included in total: %d%%, in dominating: %d%%", incl.NumBranchesTotal, percTotal, percDominating)
		}
		if vertexStatus.Deleted || vertexStatus.Status == vertex.Bad {
			return
		}
		if stop(vertexStatus, inclusionData) {
			return
		}
		time.Sleep(poll)
	}
}
