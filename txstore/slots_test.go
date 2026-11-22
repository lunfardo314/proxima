package txstore

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// LatestSlot recovers the highest slot by descending the txid
// key prefix one byte at a time. Backed by a real SimpleTxBytesStore over an
// in-memory KV so the key layout under test is the production one.
func TestLatestAndOldestSlot(t *testing.T) {
	newStore := func(slots ...uint32) *SimpleTxBytesStore {
		st := NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		for i, s := range slots {
			id := base.RandomTransactionID(false, byte(i+1), base.T(s, 5))
			// key must be the raw txid: persist under an explicit id
			_, err := st.PersistTxBytes([]byte{byte(i)}, id)
			require.NoError(t, err)
		}
		return st
	}

	t.Run("empty store", func(t *testing.T) {
		_, found := LatestSlot(newStore())
		require.False(t, found)
		_, found = OldestSlot(newStore())
		require.False(t, found)
	})

	// Values chosen to exercise every byte position of the big-endian slot,
	// including a carry-free high byte and slot 0.
	for _, c := range []struct {
		name  string
		slots []uint32
		want  uint32
		least uint32
	}{
		{"single slot 0", []uint32{0}, 0, 0},
		{"ascending", []uint32{1, 2, 3}, 3, 1},
		{"unordered insert", []uint32{300, 7, 166984, 12}, 166984, 7},
		{"high byte differs", []uint32{0x00FFFFFF, 0x01000000}, 0x01000000, 0x00FFFFFF},
		{"max-ish slot", []uint32{5, 0xFFFFFFF}, 0xFFFFFFF, 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, found := LatestSlot(newStore(c.slots...))
			require.True(t, found)
			require.EqualValues(t, c.want, got)
			got, found = OldestSlot(newStore(c.slots...))
			require.True(t, found)
			require.EqualValues(t, c.least, got)
		})
	}
}
