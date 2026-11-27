package node_cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

var (
	summaryLRBDepth             int
	summarySortBySafeRevocation bool
)

func initSummaryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "summary",
		Short: `displays the accounts of the watch list side by side and their total`,
		Long: `Displays the sigLock outputs and the delegations mastered by every account of
the watch list, and their total. The watch list is the wallet, the cold wallet
and the 'watch' section of the profile, a mapping of names to holder IDs; with
--target, the one account given. With -v, every delegation is listed.

'proxi node balance' shows everything one account holds.`,
		Args: cobra.NoArgs,
		Run:  runSummaryCmd,
	}
	glb.AddFlagTarget(cmd)
	// bound variables only, no viper binding: 'lrb-depth' and 'rw' are registered by
	// 'balance' too, and a viper key bound from two command trees keeps only the last
	cmd.PersistentFlags().IntVar(&summaryLRBDepth, "lrb-depth", 0,
		"read state N branches back from the LRB (0 = the LRB itself)")
	cmd.PersistentFlags().BoolVarP(&summarySortBySafeRevocation, "rw", "w", false,
		"with -v, sort delegations by safe revocation window")
	cmd.InitDefaultHelpCmd()
	return cmd
}

// watchedHolder is what the summary counts for one watch list entry (kb/proxi_watch.md).
type watchedHolder struct {
	glb.WatchEntry
	numSigLock    int
	sumSigLock    uint64
	delegations   []glb.DelegationOutputDisplayItem
	sumDelegation uint64
	partial       bool
}

// runSummaryCmd displays the balances of every entry of the watch list, or of the target
// alone, and their total.
func runSummaryCmd(cmd *cobra.Command, _ []string) {
	glb.Assertf(summaryLRBDepth >= 0, "--lrb-depth must be >= 0")
	var entries []glb.WatchEntry
	if cmd.Flags().Changed("target") {
		sl, isSigLock := glb.MustGetTarget().(ledger.SigLock)
		glb.Assertf(isSigLock, "summary: the target must be a holder ID, not a chain lock")
		entries = []glb.WatchEntry{{Name: "target", HolderID: base.HolderID(sl)}}
	} else {
		var err error
		entries, err = glb.GetWatchList()
		glb.AssertNoError(err)
		glb.Assertf(len(entries) > 0, "watch list is empty: no wallet key, no cold wallet and no 'watch' section in the profile")
	}

	lib := glb.GetTxLibrary()
	clnt := glb.GetClient()
	holders := make([]*watchedHolder, 0, len(entries))
	var lrbID base.TransactionID
	sameLRB := true
	for i, e := range entries {
		res, err := clnt.GetOutputsForControllerID(e.HolderID[:], client.GetOutputsParams{
			LockType:   api.GetOutputsLockTypeAll,
			MaxOutputs: api.GetOutputsIterationCap,
			LRBDepth:   summaryLRBDepth,
		})
		glb.AssertNoError(err)
		if i == 0 {
			lrbID = res.LRBID
		} else if res.LRBID != lrbID {
			sameLRB = false
		}
		h := &watchedHolder{WatchEntry: e, partial: res.LimitExceeded}
		for _, o := range res.Outputs {
			if dview, isDelegation, err := lib.ParseDelegationOutput(o.Output.Output, o.ID); err == nil && isDelegation {
				if dview.MasterID == e.HolderID {
					h.delegations = append(h.delegations, glb.DelegationOutputDisplayItem{View: dview, Balance: o.Output.TokenBalance()})
					h.sumDelegation += o.Output.TokenBalance()
				}
				continue
			}
			// sigLock reads its holder from index value 0, so the output is the holder's
			// only when that value is the holder ID
			kind, err := lib.ClassifyLock(o.Output.Bytes(), e.HolderID)
			if err != nil || kind != txbuildercore.LockKindSig {
				continue
			}
			ivBin, err := o.Output.ConstraintAt(txbuildercore.ConstraintIndexIndexValues)
			if err != nil {
				continue
			}
			if iv, err := txbuildercore.DecodeIndexValuesTuple(ivBin); err == nil && len(iv) > 0 && string(iv[0]) == string(e.HolderID[:]) {
				h.numSigLock++
				h.sumSigLock += o.Output.TokenBalance()
			}
		}
		holders = append(holders, h)
	}

	glb.PrintLRB(&lrbID)
	if !sameLRB {
		glb.Infof("WARNING: the LRB moved while reading: entries were not all read on the same branch")
	}
	displayBalanceSummary(holders)
}

func displayBalanceSummary(holders []*watchedHolder) {
	verbose := glb.IsVerbose()
	var seqNames map[base.ChainID]string
	if verbose {
		seqNames = make(map[base.ChainID]string)
		active, inactive, err := glb.FetchSequencerCandidates()
		glb.AssertNoError(err)
		for _, c := range append(active, inactive...) {
			seqNames[c.ID] = c.Name
		}
	}
	consts := glb.GetLedgerConstants()
	currentSlot := glb.GetLedgerTimeNow().Slot

	nameWidth := len("TOTAL")
	for _, h := range holders {
		nameWidth = max(nameWidth, len(h.Name))
	}
	holderWidth := 10
	if verbose {
		holderWidth = 64
	}

	header := fmt.Sprintf("%-*s  %-*s  %28s  %28s  %24s", nameWidth, "name", holderWidth, "holder", "sigLock outputs", "delegations", "total")
	glb.Infof("%s", header)
	var numSigLock, numDelegations int
	var sumSigLock, sumDelegation uint64
	for _, h := range holders {
		holderStr := h.HolderID.String()
		if !verbose {
			holderStr = holderStr[:8] + ".."
		}
		line := ""
		if h.partial {
			line = "  (partial: iteration cap hit)"
		}
		glb.Infof("%-*s  %-*s  %4d %23s  %4d %23s  %24s%s", nameWidth, h.Name, holderWidth, holderStr,
			h.numSigLock, util.Prox(h.sumSigLock), len(h.delegations), util.Prox(h.sumDelegation),
			util.Prox(h.sumSigLock+h.sumDelegation), line)
		numSigLock += h.numSigLock
		sumSigLock += h.sumSigLock
		numDelegations += len(h.delegations)
		sumDelegation += h.sumDelegation

		if !verbose {
			continue
		}
		sort.Slice(h.delegations, func(i, j int) bool {
			if summarySortBySafeRevocation {
				return h.delegations[i].View.UnfreezeSlot(consts) < h.delegations[j].View.UnfreezeSlot(consts)
			}
			return h.delegations[i].Balance > h.delegations[j].Balance
		})
		for _, d := range h.delegations {
			name, ok := seqNames[d.View.Target]
			if !ok {
				name = "?"
			}
			glb.Infof("      %s  %24s  -> %-8s %s  %s", d.View.ChainID.String(), util.Prox(d.Balance),
				name, d.View.Target.StringShort(), glb.DelegationStatusString(d.View, currentSlot, consts))
		}
	}
	glb.Infof("%s", strings.Repeat("-", len(header)))
	glb.Infof("%-*s  %-*s  %4d %23s  %4d %23s  %24s", nameWidth, "TOTAL", holderWidth, "",
		numSigLock, util.Prox(sumSigLock), numDelegations, util.Prox(sumDelegation), util.Prox(sumSigLock+sumDelegation))
}
