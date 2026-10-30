package factory

import (
	"bytes"
	"encoding/binary"
	"sort"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/crypto/blake2b"
)

// combinationSet tracks which (extend, {endorse1, endorse2, ...}) combinations
// have been checked. Endorsement order is irrelevant — the key is a deterministic
// hash of the sorted set of endorsed transaction IDs plus the extend output ID.
type combinationSet struct {
	checked set.Set[combinationKey]
}

type combinationKey [8]byte

func newCombinationSet() combinationSet {
	return combinationSet{
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

func (cs *combinationSet) isChecked(extend vertex.WrappedOutput, currentEndorsements []*vertex.WrappedTx, newEndorsement *vertex.WrappedTx) bool {
	key := combinationHash(extend, currentEndorsements, newEndorsement)
	return cs.checked.Contains(key)
}

func (cs *combinationSet) markChecked(extend vertex.WrappedOutput, currentEndorsements []*vertex.WrappedTx, newEndorsement *vertex.WrappedTx) {
	key := combinationHash(extend, currentEndorsements, newEndorsement)
	cs.checked.Insert(key)
}
