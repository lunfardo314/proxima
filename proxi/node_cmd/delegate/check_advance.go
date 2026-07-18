package delegate

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
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
	cutRejected       bool   // seqTolerance < requiredCut
	suggestedCut      uint16 // max affordable cut (greedy case)
	suggestedAdvance    uint64
	hasSuggestion       bool
	maxDelegationAmount uint64 // when not greedy and unaffordable
	hasMaxAmount        bool
}

// defaultMaxFrozenEpochs is the CLI default for -e/--epochs: the ledger's
// absolute maximum (constDelegationMaxFrozenEpochsMax), matching the default
// used when creating a sequencer chain. It is a literal because the flag is
// registered before the node connection that would supply ledger constants.
const defaultMaxFrozenEpochs = 35

// resolveFrozenEpochs caps the delegator's chosen freeze depth at the target
// chain's own maxFrozenEpochs, which the delegation lock enforces. Since the
// default is the ledger-wide maximum, a target with a narrower window clamps
// rather than rejecting the delegation. 0 keeps its "use target's" meaning.
func resolveFrozenEpochs(chosen byte, ti *api.SequencerTargetInfo) byte {
	targetMax := byte(ti.MaxFrozenEpochs)
	if chosen == 0 || chosen > targetMax {
		return targetMax
	}
	return chosen
}

// estimateDelegation computes the advance and checks affordability
// using target_info data. When unaffordable and greedy, suggests max
// affordable cut. When unaffordable and not greedy, suggests max
// delegation amount.
//
// Inflation is computed server-side via /eval (consts handles the
// epoch arithmetic locally). No ledger.L() singleton.
func estimateDelegation(consts *txbuildercore.Constants, clnt *client.APIClient, ti *api.SequencerTargetInfo, delegatedAmount uint64, frozenEpochs byte, requiredCut uint16, targetSeqID base.ChainID, slot uint32) *delegationEstimate {
	est := &delegationEstimate{
		seqName:         ti.Name,
		profitMarginPml: ti.ProfitMarginPml,
		greedy:          ti.Greedy,
	}

	seqTolerance := uint16(1000) - ti.ProfitMarginPml
	if seqTolerance < requiredCut {
		est.cutRejected = true
		if seqTolerance > 0 {
			est.suggestedCut = seqTolerance
			est.hasSuggestion = true
		}
		return est
	}

	est.effFrozenEpochs = resolveFrozenEpochs(frozenEpochs, ti)

	est.frozenSlots = consts.FrozenSlotsFromFrozenEpochs(targetSeqID, slot, ti.EpochDurationSlots, est.effFrozenEpochs)
	est.projectedInflation = evalChainInflationMultiStep(clnt, delegatedAmount, slot, est.frozenSlots)
	est.advance = calcAdvanceEstimate(est.projectedInflation, ti.ProfitMarginPml, ti.Greedy, requiredCut)

	if ti.TokenBalance > ti.StorageDeposit {
		est.availableForAdvance = ti.TokenBalance - ti.StorageDeposit
	}

	est.affordable = est.advance <= est.availableForAdvance

	if !est.affordable {
		if ti.Greedy {
			// When greedy: advance = inflation * cut / 1000. Find max cut.
			if est.projectedInflation > 0 {
				maxCut := uint16((est.availableForAdvance * 1000) / est.projectedInflation)
				if maxCut > seqTolerance {
					maxCut = seqTolerance
				}
				if maxCut > 0 && maxCut < requiredCut {
					est.suggestedCut = maxCut
					est.suggestedAdvance = calcAdvanceEstimate(est.projectedInflation, ti.ProfitMarginPml, ti.Greedy, maxCut)
					est.hasSuggestion = true
				}
			}
		} else {
			// When not greedy: advance = inflation * seqTolerance / 1000 (cut-independent).
			// Suggest max delegation amount instead.
			est.maxDelegationAmount = estimateMaxDelegationAmount(consts, clnt, est.availableForAdvance, targetSeqID, slot, ti.EpochDurationSlots, est.effFrozenEpochs, ti.ProfitMarginPml, ti.Greedy, requiredCut)
			est.hasMaxAmount = true
		}
	}

	return est
}

// evalChainInflationMultiStep delegates chainInflationMultiStep
// evaluation to the node via /eval (slot=0 = latest library). Panics
// via glb.AssertNoError on transport / per-formula failure; the
// caller is a one-shot CLI flow where this is acceptable.
func evalChainInflationMultiStep(clnt *client.APIClient, amount uint64, slot, forSlots uint32) uint64 {
	src := fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)", amount, slot, forSlots)
	ret, err := clnt.EvalU64(0, src)
	glb.AssertNoError(err)
	return ret
}

// calcAdvanceEstimate mirrors the sequencer's calcAdvance logic.
// Greedy: advance = inflation * requiredCut / 1000
// Not greedy: advance = inflation * seqTolerance / 1000
func calcAdvanceEstimate(projectedInflation uint64, profitMarginPml uint16, greedy bool, requiredCut uint16) uint64 {
	if greedy {
		return (projectedInflation * uint64(requiredCut)) / 1000
	}
	seqTolerance := uint16(1000) - profitMarginPml
	return (projectedInflation * uint64(seqTolerance)) / 1000
}

// estimateMaxDelegationAmount binary-searches for the largest
// delegation amount whose advance fits within availableBalance.
// Each search step issues one /eval call for the inflation estimate;
// the search converges in ≤ ~70 iterations.
func estimateMaxDelegationAmount(consts *txbuildercore.Constants, clnt *client.APIClient, availableBalance uint64, targetSeqID base.ChainID, slot uint32, epochSlots uint32, frozenEpochs byte, profitMarginPml uint16, greedy bool, requiredCut uint16) uint64 {
	frozenSlots := consts.FrozenSlotsFromFrozenEpochs(targetSeqID, slot, epochSlots, frozenEpochs)

	advanceForAmount := func(amount uint64) uint64 {
		infl := evalChainInflationMultiStep(clnt, amount, slot, frozenSlots)
		return calcAdvanceEstimate(infl, profitMarginPml, greedy, requiredCut)
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

func (est *delegationEstimate) displayLines(delegatedAmount uint64, requiredCut uint16, targetSeqID base.ChainID) *lines.Lines {
	ln := lines.New()
	ln.Add("--- Delegation Estimate ---")
	ln.Add("  Target sequencer:         %s", targetSeqID.String())
	if est.seqName != "" {
		ln.Add("  Name:                     '%s'", est.seqName)
	}
	ln.Add("  Delegated amount:         %s", util.Th(delegatedAmount))
	ln.Add("  Required inflation cut: %d promille (%.1f%%)", requiredCut, float64(requiredCut)/10)

	if est.cutRejected {
		seqTolerance := uint16(1000) - est.profitMarginPml
		ln.Add("")
		ln.Add("  REJECTED: sequencer tolerance %d promille < required cut %d promille", seqTolerance, requiredCut)
		ln.Add("  (sequencer profit margin is %d promille)", est.profitMarginPml)
		if est.hasSuggestion {
			ln.Add("  Max accepted cut: %d promille (%.1f%%)", est.suggestedCut, float64(est.suggestedCut)/10)
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
			ln.Add("  Suggested inflation cut: %d promille (%.1f%%)", est.suggestedCut, float64(est.suggestedCut)/10)
			ln.Add("  Advance at suggested cut: %s", util.Th(est.suggestedAdvance))
		}
		if est.hasMaxAmount {
			ln.Add("")
			ln.Add("  Max delegation amount this sequencer can accept: %s", util.Th(est.maxDelegationAmount))
		}
	}

	return ln
}

// confirmDelegationEstimate displays the estimate and handles the unaffordable case.
// Returns the effective cut to use (may differ from input if user accepts suggestion).
func confirmDelegationEstimate(est *delegationEstimate, delegatedAmount uint64, requiredCut uint16, targetSeqID base.ChainID) uint16 {
	glb.Infof("%s", est.displayLines(delegatedAmount, requiredCut, targetSeqID).String())

	if est.cutRejected {
		if est.hasSuggestion {
			prompt := fmt.Sprintf("Use max accepted cut of %d promille (%.1f%%) instead?",
				est.suggestedCut, float64(est.suggestedCut)/10)
			if glb.YesNoPrompt(prompt, false) {
				return est.suggestedCut
			}
		}
		glb.Infof("delegation cancelled")
		os.Exit(0)
	}

	if !est.affordable {
		if est.hasSuggestion {
			prompt := fmt.Sprintf("Use suggested cut of %d promille (%.1f%%) instead?",
				est.suggestedCut, float64(est.suggestedCut)/10)
			if glb.YesNoPrompt(prompt, false) {
				return est.suggestedCut
			}
		}
		glb.Infof("delegation cancelled")
		os.Exit(0)
	}

	return requiredCut
}
