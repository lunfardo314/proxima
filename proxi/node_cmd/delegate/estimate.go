package delegate

import (
	"strconv"

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
delegation amount and required cut. Does not require a private key.

Examples:
  # Can this sequencer accept a 1_000_000_000_000 delegation at 900 promille cut?
  proxi node dlg estimate <seqID> 1000000000000

  # What if I only require 100 promille cut?
  proxi node dlg estimate <seqID> 1000000000000 --cut 100

  # What's the max delegation at default cut (900)?
  proxi node dlg estimate <seqID> 0`,
		Args: cobra.ExactArgs(2),
		Run:  runEstimateCmd,
	}

	cmd.PersistentFlags().Uint16("cut", 900, "required inflation cut in promille (0-1000)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runEstimateCmd(cmd *cobra.Command, args []string) {
	seqID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	amountInt, err := strconv.ParseUint(args[1], 10, 64)
	glb.AssertNoError(err)
	amount := amountInt

	cut, _ := cmd.Flags().GetUint16("cut")
	glb.Assertf(cut <= 1000, "required inflation cut must be 0-1000 promille")

	consts := glb.GetLedgerConstants()
	client := glb.GetClient()
	ti, err := client.GetSequencerTargetInfo(seqID)
	glb.Assertf(err == nil, "cannot retrieve target info for %s: %v", seqID.String(), err)

	slot := glb.GetLedgerTimeNow().Slot
	est := estimateDelegation(consts, client, ti, amount, byte(consts.DelegationMaxFrozenEpochs), cut, seqID, slot)

	glb.Infof("%s", est.displayLines(amount, cut, seqID).String())

	if amount == 0 && !est.cutRejected {
		// amount=0 means "show me the max delegation this sequencer can accept"
		maxAmount := estimateMaxDelegationAmount(consts, client, est.availableForAdvance, seqID, slot, ti.EpochDurationSlots, est.effFrozenEpochs, ti.ProfitMarginPml, ti.Greedy, cut)
		glb.Infof("\nMax delegation amount at %d promille cut: %s", cut, util.Th(maxAmount))
	}
}
