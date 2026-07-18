package txstore

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	proxitxstore "github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// readTxsave parses a .txsave chunk back into (txid, raw) pairs using the
// documented layout: txid (32) | size (5, big-endian) | raw bytes. Doubles as
// the reference decoder for the format.
func readTxsave(t *testing.T, path string) []struct {
	ID  base.TransactionID
	Raw []byte
} {
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var out []struct {
		ID  base.TransactionID
		Raw []byte
	}
	for pos := 0; pos < len(data); {
		require.LessOrEqual(t, pos+base.TransactionIDLength+txsaveSizeFieldLength, len(data), "truncated header")
		id, err := base.TransactionIDFromBytes(data[pos : pos+base.TransactionIDLength])
		require.NoError(t, err)
		pos += base.TransactionIDLength

		// widen the 5-byte big-endian size into a uint64
		var sz [8]byte
		copy(sz[3:], data[pos:pos+txsaveSizeFieldLength])
		n := int(binary.BigEndian.Uint64(sz[:]))
		pos += txsaveSizeFieldLength

		require.LessOrEqual(t, pos+n, len(data), "truncated payload")
		out = append(out, struct {
			ID  base.TransactionID
			Raw []byte
		}{ID: id, Raw: data[pos : pos+n]})
		pos += n
	}
	return out
}

// Writes records in descending slot order (the order the audit frontier loop
// emits them) and checks the on-disk format, chunk naming and rotation.
func TestTxsaveChunkWriter(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "dump")

	// 1 byte limit => rotate at the first slot change after any write, so each
	// slot lands in its own chunk. Exercises the rotation path deterministically.
	cw := newTxsaveChunkWriter(prefix, 0)
	cw.limitBytes = 1

	type rec struct {
		id  base.TransactionID
		raw []byte
	}
	var written []rec
	// Two transactions per slot, slots descending 30, 20, 10.
	for _, slot := range []uint32{30, 20, 10} {
		for i := 0; i < 2; i++ {
			id := base.RandomTransactionID(false, byte(i+1), base.T(slot, 5))
			raw := make([]byte, 16+i)
			for j := range raw {
				raw[j] = byte(slot) + byte(j)
			}
			n := cw.writeRecord(id, raw)
			require.EqualValues(t, base.TransactionIDLength+txsaveSizeFieldLength+len(raw), n)
			written = append(written, rec{id: id, raw: raw})
		}
	}
	cw.closeChunk()

	// One chunk per slot; each named <prefix>-<S1>-<S2>.txsave with S1 == S2
	// here because every slot exceeded the 1-byte limit on its own.
	require.Len(t, cw.files, 3)
	for i, slot := range []uint32{30, 20, 10} {
		require.Equal(t, prefix+"-"+itoa(slot)+"-"+itoa(slot)+".txsave", cw.files[i])
	}

	// No .tmp files may survive a clean close.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), ".tmp")
	}

	// Round-trip: concatenating the chunks in order reproduces exactly what
	// was written, ids and payloads byte-for-byte.
	var got []rec
	for _, f := range cw.files {
		for _, r := range readTxsave(t, f) {
			got = append(got, rec{id: r.ID, raw: r.Raw})
		}
	}
	require.Len(t, got, len(written))
	for i := range written {
		require.Equal(t, written[i].id, got[i].id, "txid mismatch at %d", i)
		require.Equal(t, written[i].raw, got[i].raw, "payload mismatch at %d", i)
	}
}

// A slot must never be split across two chunks even when the limit is
// exceeded mid-slot: rotation is deferred until the slot actually changes.
func TestTxsaveChunkWriterKeepsSlotWhole(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "dump")
	cw := newTxsaveChunkWriter(prefix, 0)
	cw.limitBytes = 1 // exceeded by the very first record

	// 5 transactions all in slot 7, then one in slot 6.
	for i := 0; i < 5; i++ {
		id := base.RandomTransactionID(false, byte(i+1), base.T(7, 3))
		cw.writeRecord(id, []byte{1, 2, 3})
	}
	id := base.RandomTransactionID(false, 1, base.T(6, 3))
	cw.writeRecord(id, []byte{9})
	cw.closeChunk()

	require.Len(t, cw.files, 2)
	require.Equal(t, prefix+"-7-7.txsave", cw.files[0])
	require.Equal(t, prefix+"-6-6.txsave", cw.files[1])

	// All 5 slot-7 records stayed together in the first chunk.
	require.Len(t, readTxsave(t, cw.files[0]), 5)
	require.Len(t, readTxsave(t, cw.files[1]), 1)
}

// With a limit larger than the payload, everything lands in a single chunk
// spanning the full slot range S1..S2.
func TestTxsaveChunkWriterSingleChunkSpansRange(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "dump")
	cw := newTxsaveChunkWriter(prefix, 500) // 500 MB, never reached

	for _, slot := range []uint32{9, 8, 7} {
		id := base.RandomTransactionID(false, 1, base.T(slot, 1))
		cw.writeRecord(id, []byte{byte(slot)})
	}
	cw.closeChunk()

	require.Len(t, cw.files, 1)
	require.Equal(t, prefix+"-9-7.txsave", cw.files[0])
	require.Len(t, readTxsave(t, cw.files[0]), 3)
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// findLatestSlotInTxStore recovers the highest slot by descending the txid
// key prefix one byte at a time. Backed by a real SimpleTxBytesStore over an
// in-memory KV so the key layout under test is the production one.
func TestFindLatestSlotInTxStore(t *testing.T) {
	newStore := func(slots ...uint32) *proxitxstore.SimpleTxBytesStore {
		st := proxitxstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		for i, s := range slots {
			id := base.RandomTransactionID(false, byte(i+1), base.T(s, 5))
			// key must be the raw txid: persist under an explicit id
			_, err := st.PersistTxBytes([]byte{byte(i)}, id)
			require.NoError(t, err)
		}
		return st
	}

	t.Run("empty store", func(t *testing.T) {
		_, found := findLatestSlotInTxStore(newStore())
		require.False(t, found)
	})

	// Values chosen to exercise every byte position of the big-endian slot,
	// including a carry-free high byte and slot 0.
	for _, c := range []struct {
		name  string
		slots []uint32
		want  uint32
	}{
		{"single slot 0", []uint32{0}, 0},
		{"ascending", []uint32{1, 2, 3}, 3},
		{"unordered insert", []uint32{300, 7, 166984, 12}, 166984},
		{"high byte differs", []uint32{0x00FFFFFF, 0x01000000}, 0x01000000},
		{"max-ish slot", []uint32{5, 0xFFFFFFF}, 0xFFFFFFF},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, found := findLatestSlotInTxStore(newStore(c.slots...))
			require.True(t, found)
			require.EqualValues(t, c.want, got)
		})
	}
}
