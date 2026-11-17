package transaction

import (
	"fmt"
	"math"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// IssuanceBoundFromSlot is the first slot whose transactions a node checks with
// CheckIssuanceBound. Earlier history is not re-checked. MaxUint32 keeps the check
// off until a release sets the activation slot.
var IssuanceBoundFromSlot uint32 = math.MaxUint32

// CheckIssuanceBound caps the inflation a transaction declares by a bound computed in Go,
// without the EasyFL evaluator: one slot of chain inflation on everything it consumes
// (balance plus frozen coverage, at the slot-0 rate, which no later slot exceeds), the
// branch bonus ceiling on a branch, and the mining reward of the slot on each mine output.
// Honest transactions come up to it: chain inflation divides by M0 plus the slot, the bound by
// M0 alone, every term rounded down, so honest amounts reach about M0/(M0+slot) of it and never
// exceed it. It catches an evaluator fault that lets a fabricated amount through, not a small
// excess. It is node policy for transactions from IssuanceBoundFromSlot. Requires the full context.
func (tx *Transaction) CheckIssuanceBound() error {
	if tx.Slot() < IssuanceBoundFromSlot {
		return nil
	}
	var declared uint64
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, _ base.OutputID) bool {
		declared = addCapped(declared, o.Inflation())
		return true
	})
	if declared == 0 {
		return nil
	}
	lib := tx.Library
	var inflatable uint64
	for i := 0; i < tx.NumInputs(); i++ {
		in, err := tx.ConsumedOutputAt(byte(i))
		if err != nil {
			return err
		}
		a := in.Output.Amounts()
		inflatable = addCapped(addCapped(inflatable, a.TokenBalance()), uint64(a.FrozenCoverageAt(0)))
	}
	bound := inflatable / lib.MinimumInflatableAmount0
	if tx.IsBranchTransaction() {
		bound = addCapped(bound, lib.BranchInflationBonusBase(tx.Slot()))
	}
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, _ base.OutputID) bool {
		if o.Lock().Name() == ledger.MineLockName {
			bound = addCapped(bound, lib.MineAmountAtSlot(tx.Slot()))
		}
		return true
	})
	if declared > bound {
		return fmt.Errorf("CheckIssuanceBound: declared inflation %s exceeds the issuance bound %s",
			util.Th(declared), util.Th(bound))
	}
	return nil
}

func addCapped(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}
