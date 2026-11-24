package txstore

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
)

// Keys of the txstore are raw txids, and a txid starts with the 4-byte big-endian slot, so key
// order is slot order and a slot is a key prefix. The scans below need neither the state nor the
// library.

// Iterable is a txstore that can be scanned by key prefix; *SimpleTxBytesStore is one.
type Iterable interface {
	Iterator(prefix []byte) common.KVIterator
}

// BranchesInSlot returns the branch transactions stored at slot; the branch flag is in the txid.
func BranchesInSlot(src Iterable, slot uint32) []base.TransactionID {
	var ret []base.TransactionID
	src.Iterator(base.Slot2Bytes(slot)).IterateKeys(func(k []byte) bool {
		if txid, err := base.TransactionIDFromBytes(k); err == nil && txid.IsBranchTransaction() {
			ret = append(ret, txid)
		}
		return true
	})
	return ret
}

// OldestSlot returns the lowest slot present.
func OldestSlot(src Iterable) (uint32, bool) {
	return extremeSlot(src, false)
}

// LatestSlot returns the highest slot present.
func LatestSlot(src Iterable) (uint32, bool) {
	return extremeSlot(src, true)
}

// extremeSlot finds the highest or lowest slot present without relying on the iteration order,
// which a store need not keep: the slot is recovered one byte at a time, at each position the
// largest (or smallest) byte value that still yields a key, at most 4*256 seeks.
func extremeSlot(src Iterable, highest bool) (uint32, bool) {
	prefix := make([]byte, 0, base.SlotByteLength)
	for pos := 0; pos < base.SlotByteLength; pos++ {
		found := false
		for i := 0; i < 256; i++ {
			b := byte(i)
			if highest {
				b = byte(255 - i)
			}
			cand := append(append(make([]byte, 0, len(prefix)+1), prefix...), b)
			if prefixHasKey(src, cand) {
				prefix, found = cand, true
				break
			}
		}
		if !found {
			return 0, false // empty store
		}
	}
	return binary.BigEndian.Uint32(prefix), true
}

// prefixHasKey reports whether any key starts with prefix; it stops at the first, one seek.
func prefixHasKey(src Iterable, prefix []byte) bool {
	found := false
	src.Iterator(prefix).IterateKeys(func([]byte) bool {
		found = true
		return false
	})
	return found
}

// DeleteSlot deletes every transaction stored at slot, in batched deletes rather than a prefix
// drop, which would stop the store's writes while it runs. Returns how many it deleted.
func (s *SimpleTxBytesStore) DeleteSlot(slot uint32) (int, error) {
	batchable, ok := s.s.(common.BatchedUpdatable)
	if !ok {
		return 0, fmt.Errorf("DeleteSlot: the store does not support batched writes")
	}
	const batchSize = 10_000
	var keys [][]byte
	s.Iterator(base.Slot2Bytes(slot)).IterateKeys(func(k []byte) bool {
		keys = append(keys, k)
		return true
	})
	for start := 0; start < len(keys); start += batchSize {
		w := batchable.BatchedWriter()
		for _, k := range keys[start:min(start+batchSize, len(keys))] {
			w.Set(k, nil) // an empty value deletes the key
		}
		if err := w.Commit(); err != nil {
			return start, err
		}
	}
	return len(keys), nil
}
