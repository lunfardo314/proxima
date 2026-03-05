package delegate

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
)

// checkSequencerCanAcceptDelegation estimates the advance the sequencer would need to pay
// when freezing this delegation and checks if the sequencer has enough token balance
// (after reserving the storage deposit). Exits with error if the sequencer cannot accept it.
func checkSequencerCanAcceptDelegation(seqOut *ledger.OutputWithChainID, delegatedAmount uint64, frozenEpochs byte, targetSeqID base.ChainID, slot uint32) {
	seqData, ok := seqOut.Output.SequencerOutputData()
	if !ok {
		glb.Infof("WARNING: could not parse sequencer output data, skipping advance check")
		return
	}

	lib := ledger.L(slot)

	effFrozenEpochs := frozenEpochs
	if effFrozenEpochs == 0 {
		effFrozenEpochs = byte(lib.MaxFrozenEpochs)
	}

	frozenSlots := lib.FrozenSlotsFromFrozenEpochs(targetSeqID, slot, effFrozenEpochs)
	projectedInflation := lib.ChainInflationMultiStep(delegatedAmount, slot, frozenSlots)
	advance := estimateAdvance(projectedInflation, seqData)

	seqBalance := seqOut.Output.TokenBalance()
	seqStorageDeposit := ledger.MinimumStorageDeposit(seqOut.Output)
	availableForAdvance := uint64(0)
	if seqBalance > seqStorageDeposit {
		availableForAdvance = seqBalance - seqStorageDeposit
	}

	seqName := ""
	if seqData.SequencerData != nil {
		seqName = seqData.SequencerData.Name()
	}

	glb.Infof("target sequencer '%s':", seqName)
	glb.Infof("  token balance:          %s", util.Th(seqBalance))
	glb.Infof("  min storage deposit:    %s", util.Th(seqStorageDeposit))
	glb.Infof("  available for advance:  %s", util.Th(availableForAdvance))
	glb.Infof("  estimated advance (%d frozen epochs, %d frozen slots): %s",
		effFrozenEpochs, frozenSlots, util.Th(advance))

	if advance > availableForAdvance {
		maxAmount := estimateMaxDelegationAmount(lib, availableForAdvance, targetSeqID, slot, effFrozenEpochs, seqData)
		glb.Infof("sequencer cannot accept this delegation: not enough balance to pay the advance")
		glb.Infof("  maximum delegation amount this sequencer can currently accept (estimate): %s", util.Th(maxAmount))
		glb.Fatalf("delegation refused")
	}
}

// estimateAdvance computes the advance the same way the sequencer does in calcAdvance
func estimateAdvance(projectedInflation uint64, seqData *ledger.SequencerOutputData) uint64 {
	if seqData.SequencerData != nil && seqData.SequencerData.IsGreedy() {
		return (projectedInflation * 100) / 1000
	}
	profitMargin := uint16(0)
	if seqData.SequencerData != nil {
		profitMargin = seqData.SequencerData.InflationProfitMarginPromille()
	}
	seqTolerance := uint16(1000) - profitMargin
	return (projectedInflation * uint64(seqTolerance)) / 1000
}

// estimateMaxDelegationAmount binary-searches for the largest delegation amount
// whose advance fits within availableBalance
func estimateMaxDelegationAmount(lib *ledger.Library, availableBalance uint64, targetSeqID base.ChainID, slot uint32, frozenEpochs byte, seqData *ledger.SequencerOutputData) uint64 {
	frozenSlots := lib.FrozenSlotsFromFrozenEpochs(targetSeqID, slot, frozenEpochs)

	advanceForAmount := func(amount uint64) uint64 {
		infl := lib.ChainInflationMultiStep(amount, slot, frozenSlots)
		return estimateAdvance(infl, seqData)
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
