package vertex

import (
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

// Keeping track of references in vertex is crucial for the memDAG pruning.
// Only vertices which are not referenced by any part of the system can be pruned.
// It is similar to the memory garbage collection, however there's no need to traverse
// the whole set of vertices and checking if it is accessible from the program or not.
// Instead, each vertex contains reference counter. It is created with value 1 when added to the memDAG,
// which means it is referenced by the memDAG itself. Then other vertices and other parts of the system,
// such as tippool, backlog and attachers 'reference/un-reference' the VID by incrementing/decrementing
// the counter.
// When counter is 1 for some period of time, vertex is pruned by the pruner.
// It sets the counter to 0 and deletes it from the mamDAG. If any part of the program tries
// to reference vertex with reference counter == 0, the referencing fails and that part must
// abandon vertex alone (not store VID) and it will be cleaned by usual Go garbage collector.
// The transaction can be pulled and attached again, then it will receive different VID

const vertexTTLSlots = 5

// Reference increments reference counter for the vertex which is not deleted yet (counter > 0).
// It also sets TTL for vertex if it has noo references (counter == 1)
func (vid *WrappedTx) Reference() bool {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	// can't use 'atomic.Int' because of this. The check must be atomic with the next check
	if vid.numReferences == 0 {
		return false
	}
	if vid.numReferences == 1 {
		// lifetime of the vertex starts (or re-starts, if completely unreferenced before)
		vid.dontPruneUntil = time.Now().Add(vertexTTLSlots * ledger.L().ID.SlotDuration())
	}
	vid.numReferences++
	return true
}

// UnReference decrements reference counter down to 1. It panics if counter value 1 is decremented because
// the value 0 is reserved for the deleted vertices (handled by DoPruningIfRelevant)
func (vid *WrappedTx) UnReference() {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	// must be references >= 1. Only pruner can put it to 0
	vid.numReferences--
	util.Assertf(vid.numReferences >= 1, "UnReference: reference counter can't go below 1: %s", vid.ID.StringShort)
	if vid.numReferences == 1 {
		vid.dontPruneUntil = time.Now().Add(vertexTTLSlots * ledger.L().ID.SlotDuration())
	}
}

// DoPruningIfRelevant either marks vertex deleted (counter = 0), or, if it already deleted (counter=0)
// with TTL matured, un-references its past cone this way helping to prune other older vertices
// This trick is necessary in order to avoid deadlock between global state of the memeDAG and the local state
// of the wrappedTx. When trying to modify both atomically will lead to deadlock
func (vid *WrappedTx) DoPruningIfRelevant(nowis time.Time) (markedForDeletion, unreferencedPastCone bool, references uint32) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			references = vid.numReferences
			switch references {
			case 0:
				// the vertex is already marked for deletion and will not be referenced by any part of the system
				// do nothing
				util.Assertf(vid.FlagsUpNoLock(FlagVertexTxAttachmentStarted|FlagVertexTxAttachmentFinished),
					"expected: 0 references -> attachment must be finished: %s", vid.StringNoLock)
				vid.pastCone = nil
				vid.SetFlagsUpNoLock(FlagVertexIgnoreAbsenceOfPastCone)
				markedForDeletion = true
			case 1:
				// the vertex is not referenced by any part of the system. It will be marked for deletion now,
				// in this locked section and will never be referenced again by other parts of the system
				// do not prune those with not-started or not finished attachers
				// checking if vertex is fully attached. Probably it is not necessary, because
				// in that case references would be > 1
				// do not mark for deletion vertices which just have been added to the memDAG
				// the state of the vertex should never be accessed again
				if vid.FlagsUpNoLock(FlagVertexTxAttachmentStarted|FlagVertexTxAttachmentFinished) && nowis.After(vid.dontPruneUntil) {
					vid.numReferences = 0
					v.UnReferenceDependencies()
					// avoid hidden referencing and memory leak
					vid.pastCone = nil
					vid.SetFlagsUpNoLock(FlagVertexIgnoreAbsenceOfPastCone)
					unreferencedPastCone = true
					markedForDeletion = true
				}
			default:
				// vid.references > 1
				// those fully attached and old enough vertices we convert to virtual txes.
				// Note, that vertex will be referenced by others and will not be deleted
				if vid.FlagsUpNoLock(FlagVertexTxAttachmentStarted | FlagVertexTxAttachmentFinished) {
					if nowis.After(vid.dontPruneUntil) || vid.GetTxStatusNoLock() == Bad {
						// vertex is old enough or bad, un-reference its past cone by converting vertex to virtual tx
						// baseline remains available in the virtual tx
						vid.convertToVirtualTxNoLock()
						v.UnReferenceDependencies() // to help GC and pruner
						vid.pastCone = nil
						vid.SetFlagsUpNoLock(FlagVertexIgnoreAbsenceOfPastCone)
						unreferencedPastCone = true
					}
				}
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			references = vid.numReferences
			switch references {
			case 0:
				util.Assertf(vid.FlagsUpNoLock(FlagVertexTxAttachmentStarted|FlagVertexTxAttachmentFinished),
					"attachment expected to be over 2: %s", vid.StringNoLock)
				markedForDeletion = true
			case 1:
				if nowis.After(vid.dontPruneUntil) || vid.GetTxStatusNoLock() == Bad {
					vid.numReferences = 0
					v.baselineBranch = nil // to help GC
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

	return int(vid.numReferences)
}
