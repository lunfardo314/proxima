package api

import (
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core"
	api "github.com/lunfardo314/proxima/proxi/api/seq"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func Init(rootCmd *cobra.Command) {
	apiCmd := &cobra.Command{
		Use:   "api [<subcommand>]",
		Short: "specifies node api subcommand",
		Args:  cobra.MaximumNArgs(1),
	}

	apiCmd.PersistentFlags().String("api.endpoint", "", "<DNS name>:port")
	err := viper.BindPFlag("api.endpoint", apiCmd.PersistentFlags().Lookup("api.endpoint"))
	glb.AssertNoError(err)

	apiCmd.InitDefaultHelpCmd()
	initGetOutputsCmd(apiCmd)
	initGetUTXOCmd(apiCmd)
	initGetChainOutputCmd(apiCmd)
	initCompactOutputsCmd(apiCmd)
	initTransferCmd(apiCmd)
	initSpamCmd(apiCmd)

	api.Init(apiCmd)

	rootCmd.AddCommand(apiCmd)
}

func getClient() *client.APIClient {
	return client.New(viper.GetString("api.endpoint"))
}

func displayTotals(outs []*core.OutputWithID) {
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
