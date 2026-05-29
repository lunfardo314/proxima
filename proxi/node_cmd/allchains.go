package node_cmd

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
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
	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	currentSlot := glb.GetLedgerTimeNow().Slot

	clnt := glb.GetClient()
	rr, lrbid, err := clnt.GetLatestReliableBranch()
	glb.AssertNoError(err)

	glb.Infof("")
	glb.PrintLRB(&lrbid)
	glb.Infof("current slot is %d", currentSlot)

	chains, _, err := clnt.GetAllChains()
	glb.AssertNoError(err)
	if byOwners {
		listChainOwners(chains, rr, lib)
	} else {
		listChains(chains, rr, lib, consts, currentSlot)
	}
}

// chainInfo bundles wallet-side parsed metadata for a single chain
// output. Computed once per output in listChainsShort/Verbose so the
// display loops stay singleton-free.
type chainInfo struct {
	o           *ledger.OutputWithChainID
	isSequencer bool
	seqName     string
	chainCC     *txbuildercore.ChainConstraintView
	isDelegate  bool
	dview       *txbuildercore.DelegationOutputView
	lockBin     []byte
}

func parseChainInfo(o *ledger.OutputWithChainID, lib *txbuildercore.Library[any]) chainInfo {
	ci := chainInfo{o: o}
	ci.lockBin, _ = o.Output.ConstraintAt(ledger.ConstraintIndexLock)

	if cc, err := lib.ParseChainConstraint(o.Output.MustConstraintAt(ledger.ConstraintIndexChain)); err == nil {
		ci.chainCC = cc
	}
	ci.isSequencer = o.ID.IsSequencerTransaction()
	if ci.isSequencer {
		if sd, err := ledger.ParseSequencerData(o.Output); err == nil {
			ci.seqName = sd.Name()
		}
	}
	if dv, isDlg, err := lib.ParseDelegationOutput(o.Output.Output, o.ID); err == nil && isDlg {
		ci.isDelegate = true
		ci.dview = dv
	}
	return ci
}

func listChainsShort(chains []*ledger.OutputWithChainID, lrbRootRecord *multistate.BranchDataJSONAble, lib *txbuildercore.Library[any], consts *txbuildercore.Constants, currentSlot uint32) {
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

	infos := make([]chainInfo, len(chains))
	for i, o := range chains {
		infos[i] = parseChainInfo(o, lib)
	}

	seqNames := make(map[base.ChainID]string)
	seqHeight := make(map[base.ChainID]string)
	seqSlot := make(map[base.ChainID]uint32)
	for _, ci := range infos {
		if !ci.isSequencer {
			continue
		}
		name := ci.seqName
		if name == "" {
			name = "n/a"
		}
		height := ""
		if ci.chainCC != nil {
			height = fmt.Sprintf("(%d/%d)", ci.chainCC.TransitionCounter, ci.chainCC.BranchCounter)
		}
		seqNames[ci.o.ChainID] = name
		seqHeight[ci.o.ChainID] = height
		seqSlot[ci.o.ChainID] = ci.o.ID.Slot()
	}

	totalOnSeqBalance := uint64(0)
	totalFrozen := uint64(0)
	count := 0
	for _, ci := range infos {
		o := ci.o
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
			if ci.isDelegate {
				targetID := ci.dview.Target
				targetName := targetID.String()
				if n, ok := seqNames[targetID]; ok {
					targetName = n
				}
				glb.Infof("%4d   %s --> %s, balance: %s (%s)", count, o.ChainID.String(), targetName, util.Th(bal), perc(bal, lrbRootRecord.Supply))
			} else {
				glb.Infof("%4d   %s, balance: %s", count, o.ChainID.String(), util.Th(bal))
			}
		}
		count++
	}

	glb.Infof("-----------------------")
	glb.Infof("total number of chained accounts: %d", count)
	glb.Infof("total on sequencer accounts:      %s (%s of supply)", util.Th(totalOnSeqBalance), perc(totalOnSeqBalance, lrbRootRecord.Supply))
	glb.Infof("total frozen (delegated):         %s (%s of supply)", util.Th(totalFrozen), perc(totalFrozen, lrbRootRecord.Supply))
	glb.Infof("total active coverage delta:      %s (%s of supply)", util.Th(totalOnSeqBalance+totalFrozen), perc(totalOnSeqBalance+totalFrozen, lrbRootRecord.Supply))
	inactive := lrbRootRecord.Supply - totalOnSeqBalance - totalFrozen
	glb.Infof("total inactive coverage delta:    %s (%s of supply)", util.Th(inactive), perc(inactive, lrbRootRecord.Supply))
	glb.Infof("total supply:                     %s", util.Th(lrbRootRecord.Supply))
	glb.Infof("total ADJUSTED supply:            %s", util.Th(consts.AdjustedAmount(lrbRootRecord.Supply, currentSlot)))
}

func listChainsVerbose(chains []*ledger.OutputWithChainID, lib *txbuildercore.Library[any]) {
	count := 0
	counter := 0
	for _, o := range chains {
		ci := parseChainInfo(o, lib)
		seq := "NO"
		if ci.isSequencer {
			if showDelegationsOnly {
				continue
			}
			seq = "YES"
			name := ci.seqName
			if name != "" {
				if ci.chainCC != nil {
					seq = fmt.Sprintf("%s (%d/%d)", name, ci.chainCC.TransitionCounter, ci.chainCC.BranchCounter)
				} else {
					seq = name
				}
			}
		}

		if ci.isDelegate && showSequencersOnly {
			continue
		}
		counter++
		glb.Infof("\n%2d: %s, sequencer: "+seq, counter, o.ChainID.String())
		glb.Infof("      balance         : %s", util.Th(o.Output.TokenBalance()))
		glb.Infof("      controller lock : %s", formatLockBytecode(ci.lockBin, lib))
		glb.Infof("      output          : %s", o.ID.String())
		if cc := ci.chainCC; cc != nil {
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

func listChains(chains []*ledger.OutputWithChainID, lrbRootRecord *multistate.BranchDataJSONAble, lib *txbuildercore.Library[any], consts *txbuildercore.Constants, currentSlot uint32) {
	glb.Infof("\nshow sequencers only = %v", showSequencersOnly)
	glb.Infof("show delegations only = %v", showDelegationsOnly)

	if glb.IsVerbose() {
		glb.Infof("----------------- CHAINED OUTPUTS -------------------")
		listChainsVerbose(chains, lib)
	} else {
		glb.Infof("----------------- CHAINED OUTPUTS (short) -------------------")
		listChainsShort(chains, lrbRootRecord, lib, consts, currentSlot)
	}
}

func listChainOwners(chains []*ledger.OutputWithChainID, _ *multistate.BranchDataJSONAble, lib *txbuildercore.Library[any]) {
	m := make(map[string][]*ledger.OutputWithChainID)
	infos := make(map[base.ChainID]chainInfo, len(chains))

	glb.Infof("\n------ CHAINS BY THEIR CONTROLLERS ------")
	for _, o := range chains {
		ci := parseChainInfo(o, lib)
		infos[o.ChainID] = ci
		var ownerStr string
		if ci.isDelegate {
			ownerStr = formatSigLockHolderID(ci.dview.MasterID)
		} else {
			ownerStr = formatLockBytecode(ci.lockBin, lib)
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
			ci := infos[o.ChainID]
			sum += o.Output.TokenBalance()
			switch {
			case ci.isSequencer:
				ln.Add("sequencer  %s balance %s", o.ChainID.String(), util.Th(o.Output.TokenBalance()))
				seqs++
			case ci.isDelegate:
				ln.Add("delegation %s -> %s balance %s", o.ChainID.String(), ci.dview.Target.String(), util.Th(o.Output.TokenBalance()))
				delegations++
			default:
				ln.Add("           %s balance %s", o.ChainID.String(), util.Th(o.Output.TokenBalance()))
				others++
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

// formatLockBytecode is the wallet-side display equivalent of
// `Output.Lock().String()`. Singleton-free: uses the wallet library
// to decompile the lock bytecode at index 2 to its EasyFL source
// form. Stable per-lock so it works as an owner-grouping key.
func formatLockBytecode(lockBin []byte, lib *txbuildercore.Library[any]) string {
	if len(lockBin) == 0 {
		return "<no-lock>"
	}
	src, err := lib.DecompileBytecode(lockBin)
	if err != nil {
		return fmt.Sprintf("<decompile error: %v>", err)
	}
	return src
}

// formatSigLockHolderID renders a holder ID the way `SigLock.String()`
// does — `sigLock(0x<hex>)`. Used for delegation owner grouping so
// the master groups under the same key as a regular sigLock-owned
// chain controlled by the same holder.
func formatSigLockHolderID(h base.HolderID) string {
	return fmt.Sprintf("sigLock(0x%s)", hex.EncodeToString(h[:]))
}
