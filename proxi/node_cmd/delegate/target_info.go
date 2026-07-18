package delegate

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/cobra"
)

func initTargetInfoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target_info <sequencer ID>",
		Short: "display comprehensive information about a target sequencer for delegators",
		Args:  cobra.ExactArgs(1),
		Run:   runTargetInfoCmd,
	}
	cmd.PersistentFlags().Bool("json", false, "output as JSON")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runTargetInfoCmd(cmd *cobra.Command, args []string) {
	seqID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	info, err := glb.GetClient().GetSequencerTargetInfo(seqID)
	glb.Assertf(err == nil, "cannot retrieve target info for %s: %v", seqID.String(), err)

	jsonFlag, _ := cmd.Flags().GetBool("json")
	if jsonFlag {
		data, err := json.MarshalIndent(info, "", "  ")
		glb.AssertNoError(err)
		fmt.Println(string(data))
		return
	}

	glb.Infof("%s", targetInfoLines(info, glb.GetLedgerConstants()).String())
}

func targetInfoLines(ti *api.SequencerTargetInfo, consts *txbuildercore.Constants) *lines.Lines {
	ln := lines.New()

	ln.Add("--- Identity & Chain ---")
	ln.Add("  Sequencer ID:         %s", ti.SequencerID)
	if ti.Name != "" {
		ln.Add("  Name:                 %s", ti.Name)
	}
	ln.Add("  Origin slot:          %d", ti.OriginSlot)
	ln.Add("  Current output slot:  %d", ti.CurrentOutputSlot)
	ln.Add("  Transition counter:   %s", util.Th(ti.TransitionCounter))
	ln.Add("  Branch counter:       %s", util.Th(uint64(ti.BranchCounter)))
	if slotsLived := ti.CurrentOutputSlot - ti.OriginSlot; slotsLived > 0 {
		ln.Add("  Steps per slot:       %.2f", float64(ti.TransitionCounter)/float64(slotsLived))
	}

	ln.Add("--- Balances ---")
	ln.Add("  Token balance:        %s", util.Th(ti.TokenBalance))
	ln.Add("  Storage deposit:      %s", util.Th(ti.StorageDeposit))
	availableForAdvance := uint64(0)
	if ti.TokenBalance > ti.StorageDeposit {
		availableForAdvance = ti.TokenBalance - ti.StorageDeposit
	}
	ln.Add("  Available for advance:%s", util.Th(availableForAdvance))
	hasNonZero := false
	for _, v := range ti.FrozenCoverage {
		if v != 0 {
			hasNonZero = true
			break
		}
	}
	if hasNonZero {
		ln.Add("  Frozen coverage vector:")
		for i, v := range ti.FrozenCoverage {
			if v != 0 {
				ln.Add("    [%d]: %s", i, util.Th(uint64(v)))
			}
		}
	}
	inflatableAmount := ti.TokenBalance
	if len(ti.FrozenCoverage) > 0 && ti.FrozenCoverage[0] > 0 {
		inflatableAmount += uint64(ti.FrozenCoverage[0])
	}
	ln.Add("  Inflatable amount:    %s", util.Th(inflatableAmount))
	ln.Add("  Cum chain inflation:  %s", util.Th(ti.CumulativeChainInflation))
	ln.Add("  Cum branch bonus:     %s", util.Th(ti.CumulativeBranchBonus))

	ln.Add("--- Sequencer Parameters ---")
	ln.Add("  Minimum fee:          %s", util.Th(ti.MinimumFee))
	ln.Add("  Profit margin:        %d promille", ti.ProfitMarginPml)
	ln.Add("  Greedy:               %v", ti.Greedy)
	ln.Add("  Pace:                 %d", ti.Pace)
	ln.Add("  Freeze bounds:        %v", ti.EnforceFreezeBounds)

	ln.Add("--- Delegation Info ---")
	ln.Add("  Current epoch:        %d", ti.CurrentEpoch)
	epochBoundaryTime := consts.ClockTime(base.T(ti.NextEpochBoundarySlot, 0))
	ln.Add("  Next epoch boundary:  slot %d (%s)", ti.NextEpochBoundarySlot, epochBoundaryTime.Format("2006-01-02 15:04:05"))
	if timeLeft := time.Until(epochBoundaryTime); timeLeft > 0 {
		ln.Add("  Time to next epoch:   %s", timeLeft.Truncate(time.Second))
	}
	ln.Add("  Max frozen epochs:    %d", ti.MaxFrozenEpochs)
	ln.Add("  Epoch duration:       %d slots", ti.EpochDurationSlots)
	ln.Add("  Coverage lower bound: %s", util.Th(ti.CoverageLowerBound))
	ln.Add("  Coverage upper bound: %s", util.Th(ti.CoverageUpperBound))
	withinBounds := inflatableAmount >= ti.CoverageLowerBound && inflatableAmount <= ti.CoverageUpperBound
	if withinBounds {
		ln.Add("  Inflatable amount:    WITHIN bounds")
	} else {
		ln.Add("  Inflatable amount:    OUT OF bounds")
	}

	return ln
}
