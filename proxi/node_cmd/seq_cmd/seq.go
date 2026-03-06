package seq_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

var seqIDstr string

func Init() *cobra.Command {
	seqCmd := &cobra.Command{
		Use:     "sequencer",
		Aliases: []string{"seq"},
		Short:   `defines subcommands for the sequencer`,
		Args:    cobra.NoArgs,
	}

	glb.AddFlagTarget(seqCmd)

	seqCmd.AddCommand(
		initSeqWithdrawCmd(),
		initSeqInfoCmd(),
		initSeqSetCmd(),
	)

	seqCmd.InitDefaultHelpCmd()
	return seqCmd
}
