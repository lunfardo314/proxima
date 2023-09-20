package api

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/config"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initGetOutputsCmd(apiCmd *cobra.Command) {
	getOutputsCmd := &cobra.Command{
		Use:   "get_outputs",
		Short: `returns all outputs locked in the accountable from the heaviest state of the latest epoch`,
		Args:  cobra.NoArgs,
		Run:   runGetOutputsCmd,
	}

	getOutputsCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getOutputsCmd)
}

func mustGetAccount() core.Accountable {
	str := viper.GetString("target")
	if str == "" {
		ownPrivateKey := config.GetPrivateKey()
		util.Assertf(ownPrivateKey != nil, "private key undefined")
		ret := core.AddressED25519FromPrivateKey(ownPrivateKey)
		console.Infof("own account will be used: %s", ret.String())
		return ret
	}
	accountable, err := core.AccountableFromSource(str)
	console.AssertNoError(err)
	return accountable
}

func runGetOutputsCmd(_ *cobra.Command, _ []string) {
	accountable := mustGetAccount()

	oData, err := getClient().GetAccountOutputs(accountable)
	console.AssertNoError(err)

	outs, err := txutils.ParseAndSortOutputData(oData, nil)
	console.AssertNoError(err)

	console.Infof("%d outputs locked in the account %s", len(outs), accountable.String())
	for _, o := range outs {
		console.Infof(o.String())
	}
}
