package node_cmd

import (
	"sync"

	"github.com/lunfardo314/proxima/api/client"
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
		initGetUTXOCmd(),
		initGetChainOutputCmd(),
		initCompactOutputsCmd(),
		initBalanceCmd(),
		initTransferCmd(),
		initSpamCmd(),
		initMakeChainCmd(),
		initChainsCmd(),
		initSyncInfoCmd(),
		initNodeInfoCmd(),
		seq_cmd.Init(),
	)

	//node_cmd.Init(nodeCmd) ????

	return nodeCmd
}

var displayEndpointOnce sync.Once

func getClient() *client.APIClient {
	endpoint := viper.GetString("api.endpoint")
	glb.Assertf(endpoint != "", "node API endpoint not specified")
	displayEndpointOnce.Do(func() {
		glb.Infof("using API endpoint: %s", endpoint)
	})
	return client.New(endpoint)
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
		glb.Infof("amount controlled on %d non-chain outputs: %s", numNonChains, util.GoThousands(sumOutsideChains))
	}
	if numChains > 0 {
		glb.Infof("amount controlled on %d chain outputs: %s", numChains, util.GoThousands(sumOnChains))
	}
	glb.Infof("TOTAL controlled on %d outputs: %s", numChains+numNonChains, util.GoThousands(sumOnChains+sumOutsideChains))
}

func getTagAlongFee() uint64 {
	return viper.GetUint64("tag-along.fee")
}

func GetTagAlongSequencerID() *ledger.ChainID {
	seqIDStr := viper.GetString("tag-along.sequencer_id")
	if seqIDStr == "" {
		return nil
	}
	ret, err := ledger.ChainIDFromHexString(seqIDStr)
	glb.AssertNoError(err)

	o, err := getClient().GetChainOutputData(ret)
	glb.AssertNoError(err)
	glb.Assertf(o.ID.IsSequencerTransaction(), "can't get tag-along sequencer %s: chain output %s is not a sequencer output",
		ret.Short(), o.ID.StringShort())

	return &ret
}

func NoWait() bool {
	return viper.GetBool("nowait")
}
