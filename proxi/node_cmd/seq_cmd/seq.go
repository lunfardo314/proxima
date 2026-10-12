package seq_cmd

import (
	"bytes"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
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

// isControlledBy reports whether the chain output is locked by a sigLock of
// the given holder: the holder ID is the index value of a sigLock output.
func isControlledBy(lib *txbuildercore.Library[any], o *ledger.OutputWithChainID, holder base.HolderID) bool {
	lockBin, err := o.Output.ConstraintAt(ledger.ConstraintIndexLock)
	if err != nil {
		return false
	}
	sym, _, _, err := lib.ParseBytecodeOneLevel(lockBin)
	if err != nil || sym != ledger.SigLockName {
		return false
	}
	iv := o.Output.IndexValues()
	return len(iv) == 1 && bytes.Equal(iv[0], holder[:])
}
