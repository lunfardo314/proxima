package factory

import (
	"bytes"
	"encoding/binary"
	"sort"
	"sync"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/crypto/blake2b"
)

// combinationSet tracks which (extend, {endorse1, endorse2, ...}) combinations
// have been checked. Endorsement order is irrelevant — the key is a deterministic
// hash of the sorted set of endorsed transaction IDs plus the extend output ID.
//
// Shared by every factory of a group: a combination one heuristic has already built is never
// rebuilt by another, which is what keeps several factories from costing several times the
// attacher work. Hence the mutex — the set is read and written from all of their goroutines.
type combinationSet struct {
	mutex   sync.Mutex
	checked set.Set[combinationKey]
}

type combinationKey [8]byte

func newCombinationSet() *combinationSet {
	return &combinationSet{
		checked: set.New[combinationKey](),
	}
}

// combinationHash computes a deterministic hash for an (extend, endorseSet) combination.
// The endorsement IDs are sorted before hashing so order doesn't matter.
func combinationHash(extend vertex.WrappedOutput, currentEndorsements []*vertex.WrappedTx, newEndorsement *vertex.WrappedTx) combinationKey {
	// collect all endorsement IDs
	ids := make([]base.TransactionID, 0, len(currentEndorsements)+1)
	for _, e := range currentEndorsements {
		ids = append(ids, e.ID())
	}
	ids = append(ids, newEndorsement.ID())

	// sort for deterministic ordering
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})

	h, _ := blake2b.New256(nil)
	// hash extend output ID
	oid := extend.DecodeID()
	h.Write(oid[:])
	// hash sorted endorsement IDs
	for _, id := range ids {
		h.Write(id[:])
	}
	// also hash the count to distinguish different-sized sets
	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], uint32(len(ids)))
	h.Write(countBuf[:])

	var key combinationKey
	copy(key[:], h.Sum(nil))
	return key
}

// unmark forgets a combination, so a pair rejected only because its past cone was not solid yet
// can be retried later in the slot rather than being written off for the whole of it.
func (cs *combinationSet) unmark(extend vertex.WrappedOutput, currentEndorsements []*vertex.WrappedTx, newEndorsement *vertex.WrappedTx) {
	key := combinationHash(extend, currentEndorsements, newEndorsement)
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	cs.checked.Remove(key)
}

// reset empties the set for a new target slot.
func (cs *combinationSet) reset() {
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	cs.checked = set.New[combinationKey]()
}

func (cs *combinationSet) isChecked(extend vertex.WrappedOutput, currentEndorsements []*vertex.WrappedTx, newEndorsement *vertex.WrappedTx) bool {
	key := combinationHash(extend, currentEndorsements, newEndorsement)
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	return cs.checked.Contains(key)
}

// checkAndMark reports whether the combination was already checked and marks it if not, in one
// atomic step. Two factories racing on the same combination would otherwise both see it unchecked
// and both build it, which is exactly the duplicated work the shared set exists to prevent.
func (cs *combinationSet) checkAndMark(extend vertex.WrappedOutput, currentEndorsements []*vertex.WrappedTx, newEndorsement *vertex.WrappedTx) (alreadyChecked bool) {
	key := combinationHash(extend, currentEndorsements, newEndorsement)
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	if cs.checked.Contains(key) {
		return true
	}
	cs.checked.Insert(key)
	return false
}

func (cs *combinationSet) markChecked(extend vertex.WrappedOutput, currentEndorsements []*vertex.WrappedTx, newEndorsement *vertex.WrappedTx) {
	key := combinationHash(extend, currentEndorsements, newEndorsement)
	cs.mutex.Lock()
	defer cs.mutex.Unlock()
	cs.checked.Insert(key)
}
