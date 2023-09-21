package api

import (
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var seqIDstr string

func Init(apiCmd *cobra.Command) {
	seqCmd := &cobra.Command{
		Use:     "sequencer",
		Aliases: []string{"seq"},
		Short:   `defines subcommands for the sequencer`,
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			console.Infof("target sequencer ID is %s", getSequencerID().String())
		},
	}

	seqCmd.PersistentFlags().StringVar(&seqIDstr, "sequencer.id", "", "default sequencer chainID in hex-encoded form")
	err := viper.BindPFlag("sequencer.id", seqCmd.PersistentFlags().Lookup("sequencer.id"))
	console.AssertNoError(err)

	initSeqWithdrawCmd(seqCmd)
	seqCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(seqCmd)
}

func getSequencerID() *core.ChainID {
	ret, err := core.ChainIDFromHexString(viper.GetString("sequencer.id"))
	console.AssertNoError(err)
	return &ret
}

func getClient() *client.APIClient {
	return client.New(viper.GetString("api.endpoint"))
}

func mustGetTargetAccount() core.Lock {
	str := viper.GetString("target")
	if str == "" {
		ownPrivateKey := glb.GetPrivateKey()
		util.Assertf(ownPrivateKey != nil, "private key undefined")
		ret := core.AddressED25519FromPrivateKey(ownPrivateKey)
		console.Infof("own account will be used as target: %s", ret.String())
		return ret
	}
	accountable, err := core.AccountableFromSource(str)
	console.AssertNoError(err)
	return accountable.AsLock()
}
