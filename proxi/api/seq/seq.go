package api

import (
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/proxi/glb"
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
			glb.Infof("target sequencer ID is %s", glb.GetOwnSequencerID().String())
		},
	}

	seqCmd.PersistentFlags().StringVar(&seqIDstr, "sequencer.id", "", "default sequencer chainID in hex-encoded form")
	err := viper.BindPFlag("sequencer.id", seqCmd.PersistentFlags().Lookup("sequencer.id"))
	glb.AssertNoError(err)

	initSeqWithdrawCmd(seqCmd)
	seqCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(seqCmd)
}

func getClient() *client.APIClient {
	return client.New(viper.GetString("api.endpoint"))
}
