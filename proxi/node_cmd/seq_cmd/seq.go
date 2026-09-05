package seq_cmd

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

// sequencerIDStr is bound to the --sequencer/-q flag of the whole 'seq' command
// tree. Empty means "the sequencer this wallet controls".
var sequencerIDStr string

func Init() *cobra.Command {
	seqCmd := &cobra.Command{
		Use:     "sequencer",
		Aliases: []string{"seq"},
		Short:   `defines subcommands for the sequencer`,
		Args:    cobra.NoArgs,
	}

	glb.AddFlagTarget(seqCmd)
	seqCmd.PersistentFlags().StringVarP(&sequencerIDStr, "sequencer", "q", "", "sequencer in question (default: the wallet's own sequencer)")

	seqCmd.AddCommand(
		initSeqInitCmd(),
		initSeqWithdrawCmd(),
		initSeqInfoCmd(),
		initSeqSetCmd(),
	)

	seqCmd.InitDefaultHelpCmd()
	return seqCmd
}

// sequencerInQuestion resolves the sequencer a subcommand acts on: the
// --sequencer/-q flag when given, otherwise the one configured as own in the
// wallet profile.
func sequencerInQuestion() base.ChainID {
	if sequencerIDStr != "" {
		ret, err := base.ChainIDFromHexString(sequencerIDStr)
		glb.Assertf(err == nil, "failed parsing sequencer ID '%s': %v", sequencerIDStr, err)
		return ret
	}
	own := glb.GetOwnSequencerID()
	glb.Assertf(own != nil, "sequencer not specified: pass --sequencer/-q or configure wallet.sequencer_id")
	return *own
}
