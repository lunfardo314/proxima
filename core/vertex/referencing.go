package vertex

import (
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

const (
	referencedVertexTTLSlots    = 5
	notReferencedVertexTTLSlots = 5
)

func (vid *WrappedTx) Reference() bool {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	// can't use atomic.Int because of this
	if vid.references == 0 {
		return false
	}
	if vid.references == 1 {
		// lifetime of the vertex starts (or re-starts, if completely unreferenced before)
		vid.dontPruneUntil = time.Now().Add(referencedVertexTTLSlots * ledger.L().ID.SlotDuration())
	}
	vid.references++
	return true
}

func (vid *WrappedTx) UnReference() {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	// must be references >= 1. Only pruner can put it to 0
	vid.references--
	util.Assertf(vid.references >= 1, "UnReference: reference count can't go below 1: %s", vid.ID.StringShort)
	if vid.references == 1 {
		vid.dontPruneUntil = time.Now().Add(notReferencedVertexTTLSlots * ledger.L().ID.SlotDuration())
	}
}

// DoPruningIfRelevant either marks vertex pruned, or, if it is matured,
// un-references its past cone this way helping to prune other older vertices
// Returns true if vertex was marked deleted and should be removed from the MemDAG
func (vid *WrappedTx) DoPruningIfRelevant(nowis time.Time) (markedForDeletion, unreferencedPastCone bool) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			switch vid.references {
			case 0:
				util.Assertf(vid.FlagsUpNoLock(FlagVertexTxAttachmentStarted|FlagVertexTxAttachmentFinished), "attachment expected to be over 1")
				markedForDeletion = true
			case 1:
				// do not prune those with not-started or not finished attachers
				if vid.FlagsUpNoLock(FlagVertexTxAttachmentStarted | FlagVertexTxAttachmentFinished) {
					if nowis.After(vid.dontPruneUntil) {
						vid.references = 0
						markedForDeletion = true
						v.UnReferenceDependencies()
						unreferencedPastCone = true
					}
				}
			default:
				// vid.references > 1
				if vid.FlagsUpNoLock(FlagVertexTxAttachmentStarted | FlagVertexTxAttachmentFinished) {
					if nowis.After(vid.dontPruneUntil) {
						// vertex is old enough, un-reference its past cone by converting vertex to virtual tx
						vid._put(_virtualTx{VirtualTxFromTx(v.Tx)})
						v.UnReferenceDependencies()
						unreferencedPastCone = true
					}
				}
			}
		},
		VirtualTx: func(_ *VirtualTransaction) {
			switch vid.references {
			case 0:
				util.Assertf(vid.FlagsUpNoLock(FlagVertexTxAttachmentStarted|FlagVertexTxAttachmentFinished), "attachment expected to be over 2")
				markedForDeletion = true
			case 1:
				if nowis.After(vid.dontPruneUntil) {
					vid.references = 0
					markedForDeletion = true
				}
			}
		},
	})
	return
}

func (vid *WrappedTx) MustReference() {
	util.Assertf(vid.Reference(), "MustReference: failed with %s", vid.IDShortString)
}

func (vid *WrappedTx) NumReferences() int {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return int(vid.references)
}
