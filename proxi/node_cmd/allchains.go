package node_cmd

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	showSequencersOnly      bool
	showDelegationsOnly     bool
	groupByDelegationTarget bool
)

func initAllChainsCmd() *cobra.Command {
	allChainsCmd := &cobra.Command{
		Use:   "allchains",
		Short: `lists all chains in the latest reliable branch`,
		Args:  cobra.NoArgs,
		Run:   runAllChainsCmd,
	}
	allChainsCmd.InitDefaultHelpCmd()

	allChainsCmd.PersistentFlags().BoolVarP(&showSequencersOnly, "seq", "q", false, "show sequencer chains only")
	err := viper.BindPFlag("seq", allChainsCmd.PersistentFlags().Lookup("seq"))
	glb.AssertNoError(err)

	allChainsCmd.PersistentFlags().BoolVarP(&showDelegationsOnly, "delegations", "l", false, "show delegation chains only")
	err = viper.BindPFlag("delegations", allChainsCmd.PersistentFlags().Lookup("delegations"))
	glb.AssertNoError(err)

	allChainsCmd.PersistentFlags().BoolVarP(&groupByDelegationTarget, "group", "g", false, "show all delegations grouped by delegation target")
	err = viper.BindPFlag("group", allChainsCmd.PersistentFlags().Lookup("group"))
	glb.AssertNoError(err)

	return allChainsCmd
}

func runAllChainsCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()

	clnt := glb.GetClient()
	rr, lrbid, err := clnt.GetLatestReliableBranch()
	glb.AssertNoError(err)

	glb.Infof("")
	glb.PrintLRB(&lrbid)
	glb.Infof("current slot is %d", ledger.SlotNow())

	if showDelegationsOnly && showSequencersOnly {
		listSequencerDelegationInfo(rr.Supply)
	} else {
		chains, _, err := clnt.GetAllChains()
		glb.AssertNoError(err)
		if groupByDelegationTarget {
			listGrouped(chains)
		} else {
			listChains(chains, rr)
		}
	}
}

func listChainsShort(chains []*ledger.OutputWithChainID, lrbRootRecord *multistate.RootRecord) {
	perc := func(denom, num uint64) string {
		return fmt.Sprintf("%.2f%%", 100*float64(denom)/float64(num))
	}

	sort.Slice(chains, func(i, j int) bool {
		ci := chains[i]
		cj := chains[j]
		if ci.ID.IsSequencerTransaction() == cj.ID.IsSequencerTransaction() {
			return ci.Output.TokenBalance() > cj.Output.TokenBalance()
		}
		if ci.ID.IsSequencerTransaction() && !cj.ID.IsSequencerTransaction() {
			return true
		}
		return false
	})
	seqNames := make(map[base.ChainID]string)
	seqHeight := make(map[base.ChainID]string)
	seqSlot := make(map[base.ChainID]uint32)
	for _, o := range chains {
		sd, _ := o.Output.SequencerOutputData()
		if sd != nil {
			sdName := "n/a"
			sdHeight := ""
			if md := sd.SequencerData; md != nil {
				sdName = md.Name()
				sdHeight = fmt.Sprintf("(%d/%d)", md.ChainHeight(), md.BranchHeight())
			}
			seqNames[o.ChainID] = sdName
			seqHeight[o.ChainID] = sdHeight
			seqSlot[o.ChainID] = o.ID.Slot()
		}
	}

	currentSlot := ledger.SlotNow()
	totalOnSeqBalance := uint64(0)
	totalFrozen := uint64(0)
	count := 0
	for _, o := range chains {
		bal := o.Output.TokenBalance()
		if name, isSeq := seqNames[o.ChainID]; isSeq {
			if showDelegationsOnly {
				continue
			}
			frozen := uint64(o.Output.FrozenCoverage(0))
			glb.Infof("   %s sequencer %s %s, balance: %s, frozen: %s, total: %s, last active in LRB %d slots ago",
				o.ChainID.String(), name, seqHeight[o.ChainID], util.Th(bal), util.Th(frozen), util.Th(bal+frozen), currentSlot-seqSlot[o.ChainID])
			totalOnSeqBalance += bal
			totalFrozen += frozen
		} else {
			if showSequencersOnly {
				continue
			}
			lock := o.Output.Lock()
			if dlg, isDelegation := lock.(*ledger.DelegateLock); isDelegation {
				targetID := dlg.Target.ChainID()
				targetName := targetID.String()
				if _, ok := seqNames[targetID]; ok {
					targetName = seqNames[targetID]
				}
				glb.Infof("%4d   %s --> %s, balance: %s (%s)", count, o.ChainID.String(), targetName, util.Th(bal), perc(bal, lrbRootRecord.Supply))
			} else {
				glb.Infof("%4d   %s, balance: %s", count, o.ChainID.String(), util.Th(bal))
			}
		}
		count++
	}

	glb.Infof("-----------------------")
	glb.Infof("number of sequencers:           %d", count)
	glb.Infof("total on sequencer balance:    %s (%s of supply)", util.Th(totalOnSeqBalance), perc(totalOnSeqBalance, lrbRootRecord.Supply))
	glb.Infof("total frozen (delegated):      %s (%s of supply)", util.Th(totalFrozen), perc(totalFrozen, lrbRootRecord.Supply))
	glb.Infof("total active coverage delta:   %s (%s of supply)", util.Th(totalOnSeqBalance+totalFrozen), perc(totalOnSeqBalance+totalFrozen, lrbRootRecord.Supply))
	inactive := lrbRootRecord.Supply - totalOnSeqBalance - totalFrozen
	glb.Infof("total inactive coverage delta: %s (%s of supply)", util.Th(inactive), perc(inactive, lrbRootRecord.Supply))
	glb.Infof("total supply:                  %s", util.Th(lrbRootRecord.Supply))
}

func listChainsVerbose(chains []*ledger.OutputWithChainID) {
	count := 0
	counter := 0
	for _, o := range chains {
		lock := o.Output.Lock()
		seq := "NO"
		sd, _ := o.Output.SequencerOutputData()
		if sd != nil {
			if showDelegationsOnly {
				continue
			}
			seq = "YES"
			if md := sd.SequencerData; md != nil {
				seq = fmt.Sprintf("%s (%d/%d)", md.Name(), md.ChainHeight(), md.BranchHeight())
			}
		}

		if o.Output.Lock().Name() == ledger.DelegateLockName {
			if showSequencersOnly {
				continue
			}
		}
		counter++
		glb.Infof("\n%2d: %s, sequencer: "+seq, counter, o.ChainID.String())
		glb.Infof("      balance         : %s", util.Th(o.Output.TokenBalance()))
		glb.Infof("      controller lock : %s", lock.String())
		glb.Infof("      output          : %s", o.ID.String())
		count++
	}
	glb.Infof("\ntotal %d chains", count)

}

func listChains(chains []*ledger.OutputWithChainID, lrbRootRecord *multistate.RootRecord) {
	glb.Infof("\nshow sequencers only = %v", showSequencersOnly)
	glb.Infof("show delegations only = %v", showDelegationsOnly)
	glb.Infof("------------------------------")

	if glb.IsVerbose() {
		listChainsVerbose(chains)
	} else {
		listChainsShort(chains, lrbRootRecord)
	}
}

func listGrouped(chains []*ledger.OutputWithChainID) {
	glb.Infof("\ndelegations grouped by target lock")
	glb.Infof("\nFUNCTION DISABLED")
	return
	//m := make(map[string][]*ledger.OutputWithChainID)
	//
	//total := uint64(0)
	//count := 0
	//for _, o := range chains {
	//	dOut, isDelegation := ledger.AsDelegationOutput(o.Output, o.ID)
	//	if !isDelegation {
	//		continue
	//	}
	//	dls := dOut.Target.String()
	//	lst := m[dls]
	//	if len(lst) == 0 {
	//		lst = make([]*ledger.OutputWithChainID, 0)
	//	}
	//	lst = append(lst, o)
	//	m[dls] = lst
	//}
	//
	//for tl := range m {
	//	glb.Infof("\ntarget lock: %s, total delegations: %d", tl, len(m[tl]))
	//	totalForTarget := uint64(0)
	//	for _, o := range m[tl] {
	//		glb.Infof("\n      id              : %s", o.ChainID.String())
	//		glb.Infof("      balance         : %s", util.Th(o.Output.TokenBalance()))
	//		glb.Infof("      controller lock : %s", o.Output.Lock().String())
	//		glb.Infof("      output          : %s", o.ID.String())
	//		totalForTarget += o.Output.TokenBalance()
	//		count++
	//	}
	//	glb.Infof("\n------ TokenBalance delegated to the target lock: %s", util.Th(totalForTarget))
	//	total += totalForTarget
	//}
	//glb.Infof("\nTOTAL delegations: %d, delegated amount: %s", count, util.Th(total))
}

func listSequencerDelegationInfo(supply uint64) {
	glb.Infof("\nFUNCTION DISABLED")
	return
	//
	//bySeq, _, err := glb.GetClient().GetDelegationsBySequencer()
	//glb.AssertNoError(err)
	//
	//keys := util.KeysSorted(bySeq, func(k1, k2 string) bool {
	//	return bySeq[k1].Balance > bySeq[k2].Balance
	//	//switch {
	//	//case len(bySeq[k1].Delegations) > len(bySeq[k2].Delegations):
	//	//	return true
	//	//case len(bySeq[k1].Delegations) == len(bySeq[k2].Delegations):
	//	//	return bySeq[k1].SequencerName < bySeq[k2].SequencerName
	//	//}
	//	//return false
	//})
	//
	//if glb.IsVerbose() {
	//	glb.Infof("\nSequencers with delegations:\n")
	//} else {
	//	glb.Infof("\nSequencers with delegation totals:\n")
	//}
	//totalDelegated := uint64(0)
	//totalDelegations := 0
	//
	//slotNow := uint32(ledger.TimeNow().Slot)
	//for i, seqIDHex := range keys {
	//	seqData := bySeq[seqIDHex]
	//
	//	delegatedAmount := uint64(0)
	//	for _, dd := range seqData.Delegations {
	//		delegatedAmount += dd.Amount
	//	}
	//	doid, err := base.OutputIDFromHexString(seqData.SequencerOutputID)
	//	glb.AssertNoError(err)
	//	glb.Infof("%2d. %s (%s)\t   chain balance: %20s    total delegated: %20s (%d)    last active: %d slots ago",
	//		i, seqIDHex, seqData.SequencerName, util.Th(seqData.Balance), util.Th(delegatedAmount), len(seqData.Delegations), ledger.TimeNow().Slot-doid.Slot())
	//	totalDelegated += delegatedAmount
	//	totalDelegations += len(seqData.Delegations)
	//
	//	if !glb.IsVerbose() {
	//		continue
	//	}
	//	for delID, delData := range seqData.Delegations {
	//		inflation := delData.Amount - delData.StartAmount
	//		slotsSince := slotNow - delData.SinceSlot
	//		annual := float64(ledger.Const.SlotsPerYear()) * (float64(inflation) * 100 / float64(delData.StartAmount)) / float64(slotsSince)
	//		glb.Infof("            %s %20s (+%s) annual %.2f%%",
	//			delID, util.Th(delData.Amount), util.Th(delData.Amount-delData.StartAmount), annual)
	//	}
	//	glb.Infof("")
	//}
	//
	//glb.Infof("---------------")
	//glb.Infof("TOTAL SUPPLY          :  %s", util.Th(supply))
	//glb.Infof("TOTAL DELEGATIONS     :  %d", totalDelegations)
	//glb.Infof("TOTAL DELEGATED AMOUNT:  %s (%.2f%% of supply)", util.Th(totalDelegated), 100*float64(totalDelegated)/float64(supply))
}
