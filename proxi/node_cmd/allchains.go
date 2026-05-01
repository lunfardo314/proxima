package node_cmd

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	showSequencersOnly  bool
	showDelegationsOnly bool
	byOwners            bool
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

	allChainsCmd.PersistentFlags().BoolVarP(&byOwners, "owners", "o", false, "show all chains grouped by their owners")
	err = viper.BindPFlag("owners", allChainsCmd.PersistentFlags().Lookup("owners"))
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

	chains, _, err := clnt.GetAllChains()
	glb.AssertNoError(err)
	if byOwners {
		listChainOwners(chains, rr)
	} else {
		listChains(chains, rr)
	}
}

func listChainsShort(chains []*ledger.OutputWithChainID, lrbRootRecord *multistate.BranchDataJSONAble) {
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
			}
			if cc := sd.ChainConstraint; cc != nil {
				sdHeight = fmt.Sprintf("(%d/%d)", cc.TransitionCounter, cc.BranchCounter)
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
			frozen := uint64(o.Output.FrozenCoverage(0))
			if !showDelegationsOnly {
				glb.Infof("%4d   %s sequencer %s %s, balance: %s, frozen: %s, total: %s, last active in LRB %d slots ago",
					count, o.ChainID.String(), name, seqHeight[o.ChainID], util.Th(bal), util.Th(frozen), util.Th(bal+frozen), currentSlot-seqSlot[o.ChainID])
			}
			totalOnSeqBalance += bal
			totalFrozen += frozen
		} else {
			if showSequencersOnly {
				continue
			}
			lock := o.Output.Lock()
			if dlg, isDelegation := lock.(*ledger.DelegateLock); isDelegation {
				targetID := dlg.Target
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
	glb.Infof("total number of chains:        %d", count)
	glb.Infof("total on sequencer balance:    %s (%s of supply)", util.Th(totalOnSeqBalance), perc(totalOnSeqBalance, lrbRootRecord.Supply))
	glb.Infof("total frozen (delegated):      %s (%s of supply)", util.Th(totalFrozen), perc(totalFrozen, lrbRootRecord.Supply))
	glb.Infof("total active coverage delta:   %s (%s of supply)", util.Th(totalOnSeqBalance+totalFrozen), perc(totalOnSeqBalance+totalFrozen, lrbRootRecord.Supply))
	inactive := lrbRootRecord.Supply - totalOnSeqBalance - totalFrozen
	glb.Infof("total inactive coverage delta: %s (%s of supply)", util.Th(inactive), perc(inactive, lrbRootRecord.Supply))
	glb.Infof("total supply:                  %s", util.Th(lrbRootRecord.Supply))
	glb.Infof("total ADJUSTED supply:         %s", util.Th(ledger.AdjustedAmount(lrbRootRecord.Supply, currentSlot)))
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
				name := md.Name()
				if cc := sd.ChainConstraint; cc != nil {
					seq = fmt.Sprintf("%s (%d/%d)", name, cc.TransitionCounter, cc.BranchCounter)
				} else {
					seq = name
				}
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
		cc := o.Output.ChainConstraint()
		if cc != nil {
			glb.Infof("      origin slot     : %d", cc.OriginSlot)
			glb.Infof("      transitions     : %d", cc.TransitionCounter)
			totalInflation := cc.CumulativeChainInflation + cc.CumulativeBranchBonus
			glb.Infof("      cum. inflation  : %s (chain: %s, branch bonus: %s)",
				util.Th(totalInflation), util.Th(cc.CumulativeChainInflation), util.Th(cc.CumulativeBranchBonus))
		}
		count++
	}
	glb.Infof("\ntotal %d chains", count)

}

func listChains(chains []*ledger.OutputWithChainID, lrbRootRecord *multistate.BranchDataJSONAble) {
	glb.Infof("\nshow sequencers only = %v", showSequencersOnly)
	glb.Infof("show delegations only = %v", showDelegationsOnly)

	if glb.IsVerbose() {
		glb.Infof("----------------- CHAIN OUTPUTS -------------------")
		listChainsVerbose(chains)
	} else {
		glb.Infof("----------------- CHAIN OUTPUTS (short) -------------------")
		listChainsShort(chains, lrbRootRecord)
	}
}

func listChainOwners(chains []*ledger.OutputWithChainID, lrbRootRecord *multistate.BranchDataJSONAble) {
	m := make(map[string][]*ledger.OutputWithChainID)

	var ownerStr string

	glb.Infof("\n------ CHAINS BY THEIR CONTROLLERS ------")
	for _, o := range chains {
		if dlg, ok := ledger.AsDelegationOutput(o.Output, o.ID); ok {
			ownerStr = dlg.Master().String()
		} else {
			ownerStr = o.Output.Lock().String()
		}
		m[ownerStr] = append(m[ownerStr], o)
	}
	owners := util.KeysSorted(m, func(k1, k2 string) bool {
		return len(m[k1]) > len(m[k2])
	})
	for _, owner := range owners {
		lst := m[owner]
		sort.Slice(lst, func(i, j int) bool {
			return bytes.Compare(lst[i].ChainID[:], lst[j].ChainID[:]) < 0
		})
		sum := uint64(0)
		seqs := 0
		delegations := 0
		others := 0
		ln := lines.New("       ")
		for _, o := range lst {
			sum += o.Output.TokenBalance()
			if o.Output.IsSequencerOutput() {
				ln.Add("sequencer  %s balance %s", o.ChainID.String(), util.Th(o.Output.TokenBalance()))
				seqs++
			} else {
				if dOut, isDelegation := ledger.AsDelegationOutput(o.Output, o.ID); isDelegation {
					ln.Add("delegation %s -> %s balance %s", o.ChainID.String(), dOut.Target.String(), util.Th(o.Output.TokenBalance()))
					delegations++
				} else {
					ln.Add("           %s balance %s", o.ChainID.String(), util.Th(o.Output.TokenBalance()))
					others++
				}
			}
		}
		glb.Infof("  %s (%2d = %d sequencers + %2d delegations + %2d other), total balance: %s",
			owner, len(lst), seqs, delegations, others, util.Th(sum))
		if glb.IsVerbose() {
			glb.Infof("%s", ln.String())
		}
	}
	glb.Infof("----------------------")
	glb.Infof("total chains: %d", len(chains))
	glb.Infof("total owners: %d", len(owners))
}
