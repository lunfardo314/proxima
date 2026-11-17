package transaction

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
)

// StemTargetCheckFromSlot is the first slot whose transactions a node checks with
// CheckStemAndTargetUnlocks. Earlier history is not re-checked.
var StemTargetCheckFromSlot uint32 = 256000

// CheckStemAndTargetUnlocks applies two rules a later library upgrade puts into the ledger: a branch
// consumes its predecessor stem, and a delegation is unlocked by its target only in the target's own
// milestone, whose sequencer output books the frozen coverage the transaction moves. It is node
// policy for transactions from StemTargetCheckFromSlot. Requires the full context.
func (tx *Transaction) CheckStemAndTargetUnlocks() error {
	if tx.Slot() < StemTargetCheckFromSlot {
		return nil
	}
	if tx.IsBranchTransaction() {
		pred := tx.StemOutputData().PredecessorOutputID
		consumesStem := false
		for i := 0; i < tx.NumInputs(); i++ {
			in, err := tx.ConsumedOutputAt(byte(i))
			if err != nil {
				return err
			}
			if in.ID == pred && in.Output.Lock().Name() == ledger.StemLockName {
				consumesStem = true
				break
			}
		}
		if !consumesStem {
			return fmt.Errorf("CheckStemAndTargetUnlocks: branch does not consume its predecessor stem %s", pred.StringShort())
		}
	}
	for i := 0; i < tx.NumInputs(); i++ {
		if !tx.isUnlockedByTarget(byte(i)) {
			continue
		}
		in, err := tx.ConsumedOutputAt(byte(i))
		if err != nil {
			return err
		}
		d, ok := ledger.AsDelegationOutput(in.Output, in.ID)
		if !ok {
			continue
		}
		if !tx.IsSequencerTransaction() {
			return fmt.Errorf("CheckStemAndTargetUnlocks: delegation %s unlocked by its target outside a milestone", d.ChainID.StringShort())
		}
		seqID, ok := tx.SequencerOutput().ExtractChainID()
		if !ok || seqID != d.Target {
			return fmt.Errorf("CheckStemAndTargetUnlocks: delegation %s unlocked by its target in another sequencer's milestone", d.ChainID.StringShort())
		}
	}
	return nil
}
