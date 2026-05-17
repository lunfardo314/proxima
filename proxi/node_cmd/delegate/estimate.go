package delegate

import (
	"strconv"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initEstimateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "estimate <sequencer ID> <amount> [flags]",
		Short: "estimate delegation affordability without a wallet key",
		Long: `Estimates whether the target sequencer can afford the advance for a given
delegation amount and required share. Does not require a private key.

Examples:
  # Can this sequencer accept a 1_000_000_000_000 delegation at 900 promille share?
  proxi node dlg estimate <seqID> 1000000000000

  # What if I only require 100 promille share?
  proxi node dlg estimate <seqID> 1000000000000 --share 100

  # What's the max delegation at default share (900)?
  proxi node dlg estimate <seqID> 0`,
		Args: cobra.ExactArgs(2),
		Run:  runEstimateCmd,
	}

	cmd.PersistentFlags().Uint16("share", 900, "required inflation share in promille (0-1000)")
	cmd.PersistentFlags().Uint8P("epochs", "e", 0, "max frozen epochs (0 = maximum)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runEstimateCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	seqID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	amountInt, err := strconv.ParseUint(args[1], 10, 64)
	glb.AssertNoError(err)
	amount := amountInt

	share, _ := cmd.Flags().GetUint16("share")
	glb.Assertf(share <= 1000, "required inflation share must be 0-1000 promille")

	epochs, _ := cmd.Flags().GetUint8("epochs")

	ti, err := glb.GetClient().GetSequencerTargetInfo(seqID)
	glb.Assertf(err == nil, "cannot retrieve target info for %s: %v", seqID.String(), err)

	slot := ledger.SlotNow()
	est := estimateDelegation(ti, amount, epochs, share, seqID, slot)

	glb.Infof("%s", est.displayLines(amount, share, seqID).String())

	if amount == 0 && !est.shareRejected {
		// amount=0 means "show me the max delegation this sequencer can accept"
		lib := ledger.L(slot)
		maxAmount := estimateMaxDelegationAmount(lib, est.availableForAdvance, seqID, slot, ti.EpochDurationSlots, est.effFrozenEpochs, ti.ProfitMarginPml, ti.Greedy, share)
		glb.Infof("\nMax delegation amount at %d promille share: %s", share, util.Th(maxAmount))
	}
}
