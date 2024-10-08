package seq_cmd

import (
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var seqIDstr string

func Init() *cobra.Command {
	seqCmd := &cobra.Command{
		Use:     "sequencer",
		Aliases: []string{"seq"},
		Short:   `defines subcommands for the sequencer`,
		Args:    cobra.NoArgs,
	}

	seqCmd.PersistentFlags().StringVar(&seqIDstr, "sequencer.id", "", "default sequencer chainID in hex-encoded form")
	err := viper.BindPFlag("sequencer.id", seqCmd.PersistentFlags().Lookup("sequencer.id"))
	glb.AssertNoError(err)

	glb.AddFlagTarget(seqCmd)

	seqCmd.AddCommand(
		initSeqWithdrawCmd(),
	)

	seqCmd.InitDefaultHelpCmd()
	return seqCmd
}

func getClient() *client.APIClient {
	return client.NewWithGoogleDNS(viper.GetString("api.endpoint"))
}
