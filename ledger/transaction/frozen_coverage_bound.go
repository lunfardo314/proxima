package transaction

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// FrozenCoverageBoundFromSlot is the first slot whose transactions a node checks with
// CheckFrozenCoverageBound. Earlier history is not re-checked.
var FrozenCoverageBoundFromSlot uint32 = 275500

// CheckFrozenCoverageBound caps the frozen coverage of a milestone's sequencer output in the
// current epoch by a bound computed in Go from the parsed delegations, without the EasyFL
// evaluator and the frozen-coverage builtins (balances in one transaction are bounded by the
// supply, so the int64 sums cannot overflow): the predecessor's epoch-adjusted coverage, plus
// the balance of every delegation the milestone freezes to its own chain and the increase of
// every top-up of one still frozen, minus the balance of every delegation still frozen to it
// that it consumes without producing it frozen again (a hold). Honest milestones meet it
// exactly; it catches any fault, in the rules or in their evaluation, that lets frozen coverage
// grow without delegated tokens behind it. Node policy for transactions from
// FrozenCoverageBoundFromSlot. Requires the full context.
func (tx *Transaction) CheckFrozenCoverageBound() error {
	slot := tx.Slot()
	if slot < FrozenCoverageBoundFromSlot || !tx.IsSequencerTransaction() {
		return nil
	}
	seqOut := tx.SequencerOutput()
	succ, ok := ledger.AsOutputWithChainID(seqOut.Output, seqOut.ID)
	if !ok {
		return nil
	}
	got := succ.Output.FrozenCoverage(0)
	if got <= 0 {
		return nil
	}
	ts := tx.Timestamp()

	var bound int64
	if cc := succ.Output.ChainConstraint(); cc != nil && !cc.IsOrigin() {
		pred, err := tx.ConsumedOutputAt(cc.PredecessorInputIndex)
		if err != nil {
			return err
		}
		if predChain, isChain := ledger.AsOutputWithChainID(pred.Output, pred.ID); isChain && predChain.ChainID == succ.ChainID {
			bound = predChain.AdjustedFrozenCoverage(ts)
		}
	}
	// the delegations frozen to this chain that the milestone consumes, by input index
	stillFrozen := make(map[byte]uint64)
	for i := 0; i < tx.NumInputs(); i++ {
		in, err := tx.ConsumedOutputAt(byte(i))
		if err != nil {
			return err
		}
		d, isDelegation := ledger.AsDelegationOutput(in.Output, in.ID)
		if isDelegation && d.Target == succ.ChainID && d.IsInFrozenSlot(slot) {
			stillFrozen[byte(i)] = d.Output.TokenBalance()
		}
	}
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid base.OutputID) bool {
		d, isDelegation := ledger.AsDelegationOutput(o, oid)
		if !isDelegation || d.Target != succ.ChainID || d.IsOrigin() {
			return true
		}
		predBalance, wasFrozen := stillFrozen[d.PredecessorInputIndex]
		delete(stillFrozen, d.PredecessorInputIndex)
		switch {
		case !d.IsMarkedFrozen() || !d.IsInFrozenSlot(slot):
			// a hold, or anything else leaving it unfrozen: the predecessor's balance comes off
			if wasFrozen {
				bound -= int64(predBalance)
			}
		case wasFrozen:
			// a top-up of a delegation still frozen: only the increase is new
			if b := d.Output.TokenBalance(); b > predBalance {
				bound += int64(b - predBalance)
			}
		default:
			bound += int64(d.Output.TokenBalance())
		}
		return true
	})
	// a delegation still frozen to this chain and consumed without a successor delegation
	for _, b := range stillFrozen {
		bound -= int64(b)
	}
	if got > bound {
		return fmt.Errorf("CheckFrozenCoverageBound: frozen coverage %s of sequencer %s exceeds the bound %s",
			util.Th(got), succ.ChainID.StringShort(), util.Th(bound))
	}
	return nil
}
