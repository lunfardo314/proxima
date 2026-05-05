package seq_cmd

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"golang.org/x/exp/maps"
)

func initSeqInfoCmd() *cobra.Command {
	seqSendCmd := &cobra.Command{
		Use:   "info <sequencer ID>",
		Short: `displays sequencer info`,
		Args:  cobra.ExactArgs(1),
		Run:   runSeqInfoCmd,
	}

	glb.AddFlagTarget(seqSendCmd)

	seqSendCmd.InitDefaultHelpCmd()
	return seqSendCmd
}

func runSeqInfoCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	seqID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	clnt := glb.GetClient()
	chains, _, err := clnt.GetAllChains()

	var seqUTXO *ledger.OutputWithChainID
	delegations := make([]ledger.DelegationOutput, 0)

	for _, ch := range chains {
		if ch.ChainID == seqID {
			seqUTXO = ch
			continue
		}

		if dOut, ok := ledger.AsDelegationOutput(ch.Output, ch.ID); ok {
			if dOut.Target == seqID {
				delegations = append(delegations, dOut)
			}
		}
	}
	glb.Assertf(seqUTXO != nil, "can't find chain output with ID %s", seqID.String())

	seqDataStr := "(not a sequencer)"
	if seqUTXO.ID.IsSequencerTransaction() {
		seqData, err := ledger.ParseSequencerData(seqUTXO.Output)
		if err != nil {
			seqDataStr = fmt.Sprintf("(ParseSequencerData = %v)", err.Error())
		} else {
			seqDataStr = "(" + seqData.Name() + ")"
		}
	}

	glb.Infof("\n---- the chain output %s ----\n%s", seqDataStr, seqUTXO.LinesHR("    ").String())

	if len(delegations) == 0 {
		glb.Infof("\nno delegations to display")
		return
	}
	sort.Slice(delegations, func(i, j int) bool {
		return delegations[i].Output.TokenBalance() > delegations[j].Output.TokenBalance()
	})
	currentSlot := ledger.SlotNow()
	glb.Infof("\ncurrent slot %d", currentSlot)
	glb.Infof("\n---- delegations (%d) ----", len(delegations))

	unfreezeBySlot := make(map[uint32]int)
	revocable := 0
	for _, dOut := range delegations {
		glb.Infof("   %s  %20s  %s  maxFreeze: %d  master: %s",
			dOut.ChainID.String(), util.Th(dOut.Output.TokenBalance()),
			glb.DelegationStatusString(dOut, currentSlot), dOut.MaxFrozenEpochs, ledger.SigLock(dOut.MasterID).String())
		if dOut.IsInFrozenSlot(currentSlot) {
			unfreeze := dOut.UnfreezeSlot()
			unfreezeBySlot[unfreeze]++
		}
		if dOut.IsInSafeRevocationWindow(currentSlot) {
			revocable++
		}
	}

	slots := maps.Keys(unfreezeBySlot)
	sort.Slice(slots, func(i, j int) bool {
		return slots[i] < slots[j]
	})
	glb.Infof("\n---- unfreezes by slot ----")
	for _, s := range slots {
		epoch := ledger.L(s).EpochFromSlotDirect(seqID, s)
		glb.Infof("   %d: %d (epoch %d)", s, unfreezeBySlot[s], epoch)
	}
	glb.Infof("number of unlockable by master: %d", revocable)
}
