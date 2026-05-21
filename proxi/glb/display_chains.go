package glb

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// DelegationStatusString returns a short human-readable label
// describing the current consume-ability of a delegation output at
// txSlot. Pure wallet-side: takes the parsed view + Constants for
// epoch math and clock conversion. No ledger.L() singleton.
func DelegationStatusString(view *txbuildercore.DelegationOutputView, txSlot uint32, consts *txbuildercore.Constants) string {
	if view.IsInFrozenSlot(txSlot, consts) {
		unfreeze := view.UnfreezeSlot(consts)
		untilUnfreeze := time.Until(consts.ClockTime(base.T(unfreeze, 0)))
		h := untilUnfreeze / time.Hour
		hs := ""
		if h > 0 {
			hs = fmt.Sprintf("%d hours, ", h)
		}
		m := (untilUnfreeze - h*time.Hour) / time.Minute
		return fmt.Sprintf("frozen until slot %d (%s%d min from now)", unfreeze, hs, m)
	}
	if view.IsInSafeRevocationWindow(txSlot, consts) {
		_, to, applicable := view.SafeRevocationWindow(consts)
		Assertf(applicable, "inconsistency: SafeRevocationWindow")
		untilEnd := time.Until(consts.ClockTime(base.T(to+1, 0)))
		m := untilEnd / time.Minute
		return fmt.Sprintf("safe revocation until slot %d (for %d min more)", to, m)
	}
	if view.IsMarkedOnHold() {
		return "on hold"
	}
	// Otherwise unlockable by master.
	return "can be unlocked by master"
}

// DelegationOutputDisplayItem pairs a parsed wallet-view with the
// token balance read off the raw output. Used by LinesDelegationOutputs.
type DelegationOutputDisplayItem struct {
	View    *txbuildercore.DelegationOutputView
	Balance uint64
}

// LinesDelegationOutputs renders a multi-line summary of delegation
// outputs controlled by the wallet. Verbose mode adds origin /
// transition / cumulative-inflation lines and an annualised
// inflation estimate.
//
// The `askstop refund` (frozen-only) figure is computed server-side
// via clnt.EvalU64 (chainInflationMultiStep). If clnt is nil, the
// refund column is omitted.
func LinesDelegationOutputs(
	items []DelegationOutputDisplayItem,
	currentSlot uint32,
	walletBalance uint64,
	consts *txbuildercore.Constants,
	clnt *client.APIClient,
	prefix ...string,
) *lines.Lines {
	ln := lines.New(prefix...)
	for _, item := range items {
		view := item.View
		status := DelegationStatusString(view, currentSlot, consts)
		line := fmt.Sprintf("%34s  %20s  %s maxFrozen: %d",
			view.ChainID.String(), util.Th(item.Balance), status, view.MaxFrozenEpochs)
		if view.IsInFrozenSlot(currentSlot, consts) && clnt != nil {
			compensation, err := evalChainInflationMultiStepUnchecked(clnt, item.Balance, currentSlot, view.UnfreezeSlot(consts)-currentSlot+1)
			if err == nil {
				canAfford := ""
				if walletBalance < compensation {
					canAfford = " [INSUFFICIENT FUNDS]"
				}
				line += fmt.Sprintf(", askstop refund: %s%s", util.Th(compensation), canAfford)
			}
		}
		ln.Add("%s", line)
		if VerbosityLevel() > 0 {
			ln.Add("     delegation target %s", view.Target.String())
			totalInflation := view.CumulativeChainInflation + view.CumulativeBranchBonus
			ln.Add("     origin slot: %d, transitions: %d, cumulative inflation: %s",
				view.ChainOriginSlot, view.TransitionCounter, util.Th(totalInflation))
			if view.IsMarkedFrozen() {
				unfreeze := view.UnfreezeSlot(consts)
				totalSlots := unfreeze - view.ChainOriginSlot + 1
				if totalSlots > 0 {
					perYear := totalInflation * uint64(consts.SlotsPerYear()) / uint64(totalSlots)
					// epochSlots is inlined into the delegation lock at origin
					// (Phase 5 of claude/delegation_epoch_params.md).
					lessShareForSafeRevocation := 1 - float64(consts.SafeRevocationSlots)/
						float64(uint32(view.MaxFrozenEpochs)*view.EpochSlots+consts.SafeRevocationSlots)
					ln.Add("     estimated annualized inflation: %s/year (adj. %.2f%%)",
						util.Th(uint64(float64(perYear)*lessShareForSafeRevocation)), lessShareForSafeRevocation*100)
				}
			}
		}
	}
	return ln
}

// ChainOutputDisplayItem pairs a parsed chain-constraint view with
// the output ID + token balance. DelegationParams is non-nil when the
// chain output carries a delegationParams constraint at slot 6.
type ChainOutputDisplayItem struct {
	ChainID          base.ChainID // resolved (origin → blake2b(oid))
	OutputID         base.OutputID
	Balance          uint64
	ChainConstraint  *txbuildercore.ChainConstraintView
	DelegationParams *txbuildercore.DelegationParamsView
}

// LinesChainOutputs renders a multi-line summary of non-delegation
// chain outputs. Verbose mode adds the cumulative-inflation
// breakdown. delegationParams (if present at the output) is shown on
// its own line in both verbose and non-verbose modes.
func LinesChainOutputs(items []ChainOutputDisplayItem, currentSlot uint32, prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	for _, item := range items {
		cc := item.ChainConstraint
		ln.Add("%34s  %20s   since slot: %d, last active %d slots ago, transitions: %d",
			item.ChainID.String(), util.Th(item.Balance), cc.OriginSlot,
			currentSlot-uint32(item.OutputID.Slot()), cc.TransitionCounter)
		if item.DelegationParams != nil {
			ln.Add("      delegationParams: epochSlots=%d, maxFrozenEpochs=%d",
				item.DelegationParams.EpochSlots, item.DelegationParams.MaxFrozenEpochs)
		}
		if IsVerbose() {
			totalInflation := cc.CumulativeChainInflation + cc.CumulativeBranchBonus
			ln.Add("      cumulative inflation: %s", util.Th(totalInflation))
			ln.Add("        chain inflation:    %s", util.Th(cc.CumulativeChainInflation))
			ln.Add("        branch bonus:       %s", util.Th(cc.CumulativeBranchBonus))
		}
	}
	return ln
}

// evalChainInflationMultiStepUnchecked sends one /eval call with a
// chainInflationMultiStep formula. Local to glb so it doesn't pull
// in the delegate-cmd helper; we return the error rather than
// asserting so the display path can degrade gracefully if the API
// is unreachable mid-render.
func evalChainInflationMultiStepUnchecked(clnt *client.APIClient, amount uint64, slot, forSlots uint32) (uint64, error) {
	src := fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)", amount, slot, forSlots)
	return clnt.EvalU64(0, src)
}
