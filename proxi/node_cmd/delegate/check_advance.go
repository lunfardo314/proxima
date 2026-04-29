package delegate

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type delegationEstimate struct {
	seqName             string
	profitMarginPml     uint16
	greedy              bool
	effFrozenEpochs     byte
	frozenSlots         uint32
	projectedInflation  uint64
	advance             uint64
	availableForAdvance uint64
	affordable          bool
	shareRejected       bool   // seqTolerance < requiredShare
	suggestedShare      uint16 // max affordable share (greedy case)
	suggestedAdvance    uint64
	hasSuggestion       bool
	maxDelegationAmount uint64 // when not greedy and unaffordable
	hasMaxAmount        bool
}

// estimateDelegation computes the advance and checks affordability using target_info data.
// When unaffordable and greedy, suggests max affordable share.
// When unaffordable and not greedy, suggests max delegation amount.
func estimateDelegation(ti *api.SequencerTargetInfo, delegatedAmount uint64, frozenEpochs byte, requiredShare uint16, targetSeqID base.ChainID, slot uint32) *delegationEstimate {
	lib := ledger.L(slot)
	est := &delegationEstimate{
		seqName:         ti.Name,
		profitMarginPml: ti.ProfitMarginPml,
		greedy:          ti.Greedy,
	}

	seqTolerance := uint16(1000) - ti.ProfitMarginPml
	if seqTolerance < requiredShare {
		est.shareRejected = true
		if seqTolerance > 0 {
			est.suggestedShare = seqTolerance
			est.hasSuggestion = true
		}
		return est
	}

	est.effFrozenEpochs = frozenEpochs
	if est.effFrozenEpochs == 0 {
		est.effFrozenEpochs = byte(ti.MaxFrozenEpochs)
	}

	est.frozenSlots = lib.FrozenSlotsFromFrozenEpochs(targetSeqID, slot, est.effFrozenEpochs)
	est.projectedInflation = lib.ChainInflationMultiStep(delegatedAmount, slot, est.frozenSlots)
	est.advance = calcAdvanceEstimate(est.projectedInflation, ti.ProfitMarginPml, ti.Greedy, requiredShare)

	if ti.TokenBalance > ti.StorageDeposit {
		est.availableForAdvance = ti.TokenBalance - ti.StorageDeposit
	}

	est.affordable = est.advance <= est.availableForAdvance

	if !est.affordable {
		if ti.Greedy {
			// When greedy: advance = inflation * share / 1000. Find max share.
			if est.projectedInflation > 0 {
				maxShare := uint16((est.availableForAdvance * 1000) / est.projectedInflation)
				if maxShare > seqTolerance {
					maxShare = seqTolerance
				}
				if maxShare > 0 && maxShare < requiredShare {
					est.suggestedShare = maxShare
					est.suggestedAdvance = calcAdvanceEstimate(est.projectedInflation, ti.ProfitMarginPml, ti.Greedy, maxShare)
					est.hasSuggestion = true
				}
			}
		} else {
			// When not greedy: advance = inflation * seqTolerance / 1000 (share-independent).
			// Suggest max delegation amount instead.
			est.maxDelegationAmount = estimateMaxDelegationAmount(lib, est.availableForAdvance, targetSeqID, slot, est.effFrozenEpochs, ti.ProfitMarginPml, ti.Greedy, requiredShare)
			est.hasMaxAmount = true
		}
	}

	return est
}

// calcAdvanceEstimate mirrors the sequencer's calcAdvance logic.
// Greedy: advance = inflation * requiredShare / 1000
// Not greedy: advance = inflation * seqTolerance / 1000
func calcAdvanceEstimate(projectedInflation uint64, profitMarginPml uint16, greedy bool, requiredShare uint16) uint64 {
	if greedy {
		return (projectedInflation * uint64(requiredShare)) / 1000
	}
	seqTolerance := uint16(1000) - profitMarginPml
	return (projectedInflation * uint64(seqTolerance)) / 1000
}

// estimateMaxDelegationAmount binary-searches for the largest delegation amount
// whose advance fits within availableBalance
func estimateMaxDelegationAmount(lib *ledger.Library, availableBalance uint64, targetSeqID base.ChainID, slot uint32, frozenEpochs byte, profitMarginPml uint16, greedy bool, requiredShare uint16) uint64 {
	frozenSlots := lib.FrozenSlotsFromFrozenEpochs(targetSeqID, slot, frozenEpochs)

	advanceForAmount := func(amount uint64) uint64 {
		infl := lib.ChainInflationMultiStep(amount, slot, frozenSlots)
		return calcAdvanceEstimate(infl, profitMarginPml, greedy, requiredShare)
	}

	lo, hi := uint64(0), availableBalance*1000
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if advanceForAmount(mid) <= availableBalance {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

func (est *delegationEstimate) displayLines(delegatedAmount uint64, requiredShare uint16, targetSeqID base.ChainID) *lines.Lines {
	ln := lines.New()
	ln.Add("--- Delegation Estimate ---")
	ln.Add("  Target sequencer:         %s", targetSeqID.String())
	if est.seqName != "" {
		ln.Add("  Name:                     '%s'", est.seqName)
	}
	ln.Add("  Delegated amount:         %s", util.Th(delegatedAmount))
	ln.Add("  Required inflation share: %d promille (%.1f%%)", requiredShare, float64(requiredShare)/10)

	if est.shareRejected {
		seqTolerance := uint16(1000) - est.profitMarginPml
		ln.Add("")
		ln.Add("  REJECTED: sequencer tolerance %d promille < required share %d promille", seqTolerance, requiredShare)
		ln.Add("  (sequencer profit margin is %d promille)", est.profitMarginPml)
		if est.hasSuggestion {
			ln.Add("  Max accepted share: %d promille (%.1f%%)", est.suggestedShare, float64(est.suggestedShare)/10)
		}
		return ln
	}

	ln.Add("  Frozen epochs:            %d", est.effFrozenEpochs)
	ln.Add("  Frozen slots:             %d", est.frozenSlots)
	ln.Add("  Projected inflation:      %s", util.Th(est.projectedInflation))
	ln.Add("  Estimated advance:        %s", util.Th(est.advance))
	ln.Add("  Seq available for advance:%s", util.Th(est.availableForAdvance))

	if est.affordable {
		ln.Add("  Status:                   AFFORDABLE")
	} else {
		ln.Add("  Status:                   NOT AFFORDABLE")
		if est.hasSuggestion {
			ln.Add("")
			ln.Add("  Suggested inflation share: %d promille (%.1f%%)", est.suggestedShare, float64(est.suggestedShare)/10)
			ln.Add("  Advance at suggested share: %s", util.Th(est.suggestedAdvance))
		}
		if est.hasMaxAmount {
			ln.Add("")
			ln.Add("  Max delegation amount this sequencer can accept: %s", util.Th(est.maxDelegationAmount))
		}
	}

	return ln
}

// confirmDelegationEstimate displays the estimate and handles the unaffordable case.
// Returns the effective share to use (may differ from input if user accepts suggestion).
func confirmDelegationEstimate(est *delegationEstimate, delegatedAmount uint64, requiredShare uint16, targetSeqID base.ChainID) uint16 {
	glb.Infof("%s", est.displayLines(delegatedAmount, requiredShare, targetSeqID).String())

	if est.shareRejected {
		if est.hasSuggestion {
			prompt := fmt.Sprintf("Use max accepted share of %d promille (%.1f%%) instead?",
				est.suggestedShare, float64(est.suggestedShare)/10)
			if glb.YesNoPrompt(prompt, false) {
				return est.suggestedShare
			}
		}
		glb.Infof("delegation cancelled")
		os.Exit(0)
	}

	if !est.affordable {
		if est.hasSuggestion {
			prompt := fmt.Sprintf("Use suggested share of %d promille (%.1f%%) instead?",
				est.suggestedShare, float64(est.suggestedShare)/10)
			if glb.YesNoPrompt(prompt, false) {
				return est.suggestedShare
			}
		}
		glb.Infof("delegation cancelled")
		os.Exit(0)
	}

	return requiredShare
}
