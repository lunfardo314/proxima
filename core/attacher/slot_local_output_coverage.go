package attacher

import (
	"context"
	"fmt"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// CoverageDeltaWithOutputs includes the actual unsigned candidate outputs. The
// builder constructs them once before scoring and then fills in the existing
// coverage declaration before signing. No separate signed-output comparison.
func (a *IncrementalAttacher) CoverageDeltaWithOutputs(ctx context.Context, target base.LedgerTime, outputs []*ledger.Output, seqIndex byte) (uint64, error) {
	if target.Slot != a.targetSlot || target.IsSlotBoundary() != a.isBranch {
		return 0, fmt.Errorf("coverage target mismatch")
	}
	if !ledger.L(target.Slot).SlotLocalOutputCoverage {
		return a.CoverageDeltaWithContext(ctx)
	}
	if len(outputs) == 0 || int(seqIndex) >= len(outputs) || outputs[seqIndex] == nil || !outputs[seqIndex].IsSequencerOutput() {
		return 0, fmt.Errorf("missing candidate sequencer output")
	}
	return a.pastCone.CurrentSlotOutputCoverage(ctx, a.getBaselineStateReader, target, outputs)
}
