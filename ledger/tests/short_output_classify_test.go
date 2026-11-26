package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// Slot arguments of the classifiers; their values do not matter here.
const (
	scoCreateSlot = 1
	scoTargetSlot = 2
	scoWindow     = 30
)

func classifierLibrary(t testing.TB) *txbuildercore.Library[any] {
	t.Helper()
	desc, err := easyfl.ReadLibraryFromJSON(ledger.L(base.MaxSlot).DefinitionsJSON())
	require.NoError(t, err)
	lib, err := txbuildercore.NewLibrary(desc)
	require.NoError(t, err)
	return lib
}

// TestClassifiersOutputWithoutLock: the txbuildercore classifiers get output
// bytes from a node. Bytes that parse as an output but have no lock slot must
// give an error or a negative answer, not a panic.
func TestClassifiersOutputWithoutLock(t *testing.T) {
	lib := classifierLibrary(t)
	for _, data := range [][]byte{{0x00, 0x00}, {0x00, 0x02}} {
		_, err := txbuildercore.OutputFromBytes(data)
		require.NoError(t, err, "the bytes must parse as an output for the test to mean anything")

		kind, err := lib.ClassifyLock(data, base.HolderID{})
		require.Error(t, err)
		require.EqualValues(t, txbuildercore.LockKindOther, kind)

		_, err = txbuildercore.ClassifyCleanable(lib, data, scoCreateSlot, scoTargetSlot, scoWindow)
		require.Error(t, err)

		_, err = txbuildercore.ClassifySpendable(lib, data, scoCreateSlot, base.HolderID{}, scoTargetSlot, scoWindow)
		require.Error(t, err)

		_, ok := txbuildercore.SWDAcceptanceSlots(lib, data)
		require.False(t, ok)
	}
}

// FuzzClassifiersNoPanic: arbitrary bytes never make a classifier panic.
func FuzzClassifiersNoPanic(f *testing.F) {
	lib := classifierLibrary(f)
	f.Add([]byte{0x00, 0x02})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = lib.ClassifyLock(data, base.HolderID{})
		_, _ = txbuildercore.ClassifyCleanable(lib, data, scoCreateSlot, scoTargetSlot, scoWindow)
		_, _ = txbuildercore.ClassifySpendable(lib, data, scoCreateSlot, base.HolderID{}, scoTargetSlot, scoWindow)
		_, _ = txbuildercore.SWDAcceptanceSlots(lib, data)
	})
}
