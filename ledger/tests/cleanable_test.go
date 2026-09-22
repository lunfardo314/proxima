// Tests for txbuildercore.ClassifyCleanable and the slot-chunk scan behind
// `proxi node utxo-cleanup`.
//
// Cleanable means "decayed into the lock's PUBLIC window", where any signer may
// consume the output. That is the complement of spendable-by-role: the two must
// not overlap on outputs that still belong to someone, and cleanup must never
// reach an unconditional lock.

package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

const clCreate = uint32(1000) // output createSlot used throughout

func classifyClean(t *testing.T, o *ledger.Output, targetSlot uint32) txbuildercore.CleanClass {
	t.Helper()
	lib := ledger.L(base.MaxSlot)
	cls, err := txbuildercore.ClassifyCleanable(lib, o.Bytes(), clCreate, targetSlot, lib.TagAlongReclaimSlots)
	require.NoError(t, err)
	return cls
}

func clSWD(master, target base.HolderID, cleanupSlots uint32) *ledger.Output {
	return ledger.NewSendWithDeadlineOutput(scAmount, &ledger.SendWithDeadlineLock{
		MasterID:        master,
		TargetID:        target,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: scAccept,
		CleanupSlots:    cleanupSlots,
	})
}

// An unconditional lock never decays into anyone's reach, however old it gets.
func TestCleanableSigLockNeverPublic(t *testing.T) {
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(scAmount).WithLock(ledger.SigLockRandom())
	})
	require.Equal(t, txbuildercore.CleanNotPublic, classifyClean(t, o, clCreate+1_000_000))
}

// sendWithDeadline carries its own cleanup deadline in its lock arguments, so
// the boundary is per-output rather than a global constant.
func TestCleanableSWDBoundary(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	o := clSWD(master, target, scCleanup)

	require.Equal(t, txbuildercore.CleanNotPublic, classifyClean(t, o, clCreate+scCleanup-1),
		"one slot before the cleanup deadline it is still the master's")
	require.Equal(t, txbuildercore.CleanSimple, classifyClean(t, o, clCreate+scCleanup),
		"at the cleanup deadline it becomes anybody's")
}

// Two outputs with different cleanup deadlines must be judged independently.
func TestCleanableSWDPerOutputDeadline(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	short := clSWD(master, target, scAccept+1000)
	long := clSWD(master, target, scAccept+2000)

	at := clCreate + scAccept + 1500
	require.Equal(t, txbuildercore.CleanSimple, classifyClean(t, short, at))
	require.Equal(t, txbuildercore.CleanNotPublic, classifyClean(t, long, at))
}

// returnToSender survives the public deadline: it keys off the signer, not the
// window, so a non-master sweeper still owes the receipt. Cleanup must not
// treat these as plain dust.
func TestCleanableSWDWithReturnToSenderNeedsReturn(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	rtsBin, err := ledger.ReturnToSenderBytecode(scAmount / 2)
	require.NoError(t, err)
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(scAmount).WithLock(&ledger.SendWithDeadlineLock{
			MasterID: master, TargetID: target,
			TargetType:      ledger.SendWithDeadlineTargetSigLock,
			AcceptanceSlots: scAccept, CleanupSlots: scCleanup,
		})
		o.PutConstraint(rtsBin, 3)
	})
	require.Equal(t, txbuildercore.CleanNeedsReturn, classifyClean(t, o, clCreate+scCleanup+10))
}

// An unrecognised extra constraint means the consume structure is unknown, so
// the output is left alone rather than swept blind.
func TestCleanableUnknownExtraLeftAlone(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(scAmount).WithLock(&ledger.SendWithDeadlineLock{
			MasterID: master, TargetID: target,
			TargetType:      ledger.SendWithDeadlineTargetSigLock,
			AcceptanceSlots: scAccept, CleanupSlots: scCleanup,
		})
		o.PutConstraint(easyfl.InlineDataBytecode([]byte{0x07}), 3)
	})
	require.Equal(t, txbuildercore.CleanUnknown, classifyClean(t, o, clCreate+scCleanup+10))
}

// tagAlong's public deadline is a ledger constant. Before it, the fee is still
// the sender's to reclaim — exactly the window `proxi node compact` covers —
// so the two commands must not both claim the same output.
func TestCleanableTagAlongBoundary(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	sender := base.HolderID(ledger.SigLockRandom())
	o := ledger.NewTagAlongOutput(scAmount, base.RandomChainID(), sender)

	require.Equal(t, txbuildercore.CleanNotPublic, classifyClean(t, o, clCreate+lib.TagAlongReclaimSlots-1),
		"still inside the sender's reclaim window")
	require.Equal(t, txbuildercore.CleanSimple, classifyClean(t, o, clCreate+lib.TagAlongReclaimSlots))

	// The sender keeps its own claim past the deadline (compact still sweeps
	// it); cleanup claiming it too is the intended overlap, and whoever gets
	// there first wins.
	cls, err := txbuildercore.ClassifySpendable(lib, o.Bytes(), clCreate, sender,
		clCreate+lib.TagAlongReclaimSlots, lib.TagAlongSlots)
	require.NoError(t, err)
	require.Equal(t, txbuildercore.SpendSimple, cls)
}

// =============================================================================
// Slot-chunk scan
// =============================================================================

// TestIterateUTXOsInSlotChunk pins the prefix arithmetic the scan depends on:
// one traversal must cover the whole 256-slot chunk and nothing outside it.
func TestIterateUTXOsInSlotChunk(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(31, 2, cpInitAmount)
	priv, addr := privKeys[0], addrs[0]

	// Spread outputs across a chunk boundary by paying self at chosen slots.
	seed, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, seed)
	in := seed[0]

	produceAt := func(in *ledger.OutputWithID, slot uint32) *ledger.OutputWithID {
		txb := exhelp.New()
		_, err := txb.ConsumeOutput(in.Output, in.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(in.Output.TokenBalance()).WithLock(addr)
		})
		_, err = txb.ProduceOutput(out)
		require.NoError(t, err)
		txb.SetTimestamp(base.T(slot, 1))
		txb.ComputeInputCommitment()
		txb.SignED25519(priv)
		txBytes, txid, failed, err := txbtest.BuildAndValidate(txb)
		require.NoError(t, err, "produce tx must validate:\n%s", failed)
		require.NoError(t, u.AddTransaction(txBytes))
		return &ledger.OutputWithID{ID: base.MustNewOutputID(txid, 0), Output: out}
	}

	// Last slot of chunk 1, then first slot of chunk 2.
	const lastOfChunk1 = 2*256 - 1
	const firstOfChunk2 = 2 * 256
	require.EqualValues(t, 1, multistate.SlotChunk(lastOfChunk1))
	require.EqualValues(t, 2, multistate.SlotChunk(firstOfChunk2))

	o1 := produceAt(in, lastOfChunk1)
	o2 := produceAt(o1, firstOfChunk2)

	collect := func(chunk uint32) []base.OutputID {
		var ids []base.OutputID
		require.NoError(t, u.SugaredStateReader().IterateUTXOsInSlotChunk(chunk, func(oid base.OutputID, _ []byte) bool {
			ids = append(ids, oid)
			return true
		}))
		return ids
	}

	// o1 was spent by the tx that produced o2, so only o2 survives in state.
	require.NotContains(t, collect(1), o1.ID, "spent output must be gone from its chunk")
	require.Contains(t, collect(2), o2.ID, "live output must be found in its own chunk")
	require.NotContains(t, collect(1), o2.ID, "chunk 1 must not leak into chunk 2")
	require.Empty(t, collect(3), "a chunk beyond any output must scan empty")
}

// The callback's false return must cut the traversal immediately — that is what
// keeps a small batch cheap on a state full of dust. The test first locates a
// chunk holding more than one output, so a cut is actually observable.
func TestIterateUTXOsInSlotChunkCutsEarly(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	u.GenerateAddressesWithFaucetAmount(37, 4, cpInitAmount)
	rdr := u.SugaredStateReader()

	countIn := func(chunk uint32) int {
		n := 0
		require.NoError(t, rdr.IterateUTXOsInSlotChunk(chunk, func(_ base.OutputID, _ []byte) bool {
			n++
			return true
		}))
		return n
	}

	var chunk uint32
	total := 0
	for c := uint32(0); c <= 4; c++ {
		if n := countIn(c); n > 1 {
			chunk, total = c, n
			break
		}
	}
	require.Greater(t, total, 1, "need a chunk holding several outputs for the cut to be observable")

	seen := 0
	require.NoError(t, rdr.IterateUTXOsInSlotChunk(chunk, func(_ base.OutputID, _ []byte) bool {
		seen++
		return false
	}))
	require.Equal(t, 1, seen, "iteration must stop at the first false, not scan the whole chunk")
}
