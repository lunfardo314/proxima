package vertex

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

const (
	vertexTTLSlots      = 5
	keepNotDeletedSlots = 1
)

func (vid *WrappedTx) Reference(by ...string) bool {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	{
		if len(by) > 0 {
			if len(vid.tmpRefBy) == 0 {
				vid.tmpRefBy = make([]string, 0)
			}
			vid.tmpRefBy = append(vid.tmpRefBy, fmt.Sprintf("ref %s (%d)", by[0], vid.references))
		}
		//fmt.Printf(">>>>>>>>>>>> reference %s (%d) by %+v\n", vid.ID.StringShort(), vid.references, by)
	}

	// can't use atomic.Int because of this
	if vid.references == 0 {
		return false
	}
	if vid.references == 1 {
		// lifetime of the vertex starts (or re-starts, if completely unreferenced before)
		vid.dontPruneUntil = time.Now().Add(vertexTTLSlots * ledger.L().ID.SlotDuration())
	}
	vid.references++
	return true
}

func (vid *WrappedTx) UnReference(by ...string) {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	{
		if len(by) > 0 {
			if len(vid.tmpRefBy) == 0 {
				vid.tmpRefBy = make([]string, 0)
			}
			vid.tmpRefBy = append(vid.tmpRefBy, fmt.Sprintf("UNref %s (%d)", by[0], vid.references))
		}
		//fmt.Printf(">>>>>>>>>>>> UNreference %s (%d) by %v\n", vid.ID.StringShort(), vid.references, by)
	}

	// must be references >= 1. Only pruner can put it to 0
	vid.references--
	util.Assertf(vid.references >= 1, "UnReference: reference count can't go below 1: %s", vid.ID.StringShort)
	if vid.references == 1 {
		vid.dontPruneUntil = time.Now().Add(keepNotDeletedSlots * ledger.L().ID.SlotDuration())
	}
}

func (vid *WrappedTx) RefLines() []string {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.tmpRefBy
}

// DoPruningIfRelevant either marks vertex pruned, or, if it is matured,
// un-references its past cone this way helping to prune other older vertices
// Returns true if vertex was marked deleted and should be removed from the DAG
func (vid *WrappedTx) DoPruningIfRelevant(nowis time.Time) (markedForDeletion, unreferencedPastCone bool) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			switch vid.references {
			case 0:
				markedForDeletion = true
			case 1:
				if nowis.After(vid.dontPruneUntil) {
					vid.references = 0
					markedForDeletion = true
					v.UnReferenceDependencies()
					unreferencedPastCone = true
				}
			default:
				// vid.references > 1
				if nowis.After(vid.dontPruneUntil) {
					// vertex is old enough, un-reference its past cone
					// by converting vertex to virtual tx
					vid._put(_virtualTx{VirtualTxFromVertex(v.Tx)})
					v.UnReferenceDependencies()
					unreferencedPastCone = true
				}
			}
		},
		VirtualTx: func(_ *VirtualTransaction) {
			switch vid.references {
			case 0:
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

func (vid *WrappedTx) MustReference(by ...string) {
	util.Assertf(vid.Reference(by...), "MustReference: failed with %s", vid.IDShortString)
}

func (vid *WrappedTx) NumReferences() int {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return int(vid.references)
}
