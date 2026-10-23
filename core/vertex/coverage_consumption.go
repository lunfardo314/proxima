package vertex

import (
	"context"
	"fmt"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"math/bits"
	"slices"
)

func (pc *PastCone) CoverageActivityTime() base.LedgerTime {
	return ledger.L(pc.txTs.Slot).CoverageActivityTime(pc.txTs)
}

func (pc *PastCone) isCoverageActivityConsumer(consumer *WrappedTx) bool {
	activity := pc.CoverageActivityTime()
	if consumer == nil {
		return pc.txTs.Slot == activity.Slot
	}
	return consumer.Slot() == activity.Slot && !pc.IsInTheState(consumer)
}

// IsConsumedInCoverageActivity binds virtual inputs to their actual target.
func (pc *PastCone) IsConsumedInCoverageActivity(out WrappedOutput) bool {
	return slices.ContainsFunc(pc.findConsumersOf(out), pc.isCoverageActivityConsumer)
}

// Equivalent to replaying the complete compatible older cone into a frontier:
// its outputs are baseline UTXOs plus non-rooted pre-activity outputs. Count
// only those spent in the activity slot. Same-slot internal outputs never count
// again. Native attachment establishes consistency; only touched outputs are
// read, not a copy or scan of the complete ledger state.
func (pc *PastCone) slotLocalConsumptionDelta(ctx context.Context, getStateReader func(base.TransactionID) multistate.StateReader) (uint64, error) {
	activity := pc.CoverageActivityTime()
	rdr := getStateReader(*pc.GetBaseline())
	if rdr == nil {
		return 0, fmt.Errorf("coverage baseline state reader unavailable")
	}
	var delta uint64
	for vid := range pc.vertices {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		for idx, consumers := range pc.consumersByOutputIndex(vid) {
			if !slices.ContainsFunc(consumers, pc.isCoverageActivityConsumer) {
				continue
			}
			oid := vid.OutputID(idx)
			var out *ledger.Output
			var err error
			if pc.IsInTheState(vid) {
				out = multistate.GetOutputFromStateReader(rdr, oid)
			} else {
				if oid.Slot() >= activity.Slot {
					continue
				}
				out, err = vid.OutputAt(idx)
			}
			if err != nil {
				return 0, err
			}
			if out == nil {
				return 0, fmt.Errorf("coverage frontier output %s unavailable", oid.StringShort())
			}
			if oid.Timestamp().After(activity) {
				return 0, fmt.Errorf("coverage frontier output after activity slot")
			}
			weight := ledger.Coverage(out, oid, activity)
			var overflow uint64
			delta, overflow = bits.Add64(delta, weight, 0)
			if overflow != 0 {
				return 0, fmt.Errorf("coverage overflow")
			}
		}
	}
	return delta, nil
}
