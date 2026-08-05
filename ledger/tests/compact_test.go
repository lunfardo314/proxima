// End-to-end tests for txbuildercore.MakeCompactTransaction — the shared
// compose step behind `proxi node compact` and the miner's payout
// consolidation.
//
// The point of these tests is that the wallet-side classifier and the
// wallet-side builder agree with what the ledger actually accepts: a set
// ClassifySpendable calls SpendSimple must settle when swept, across all three
// lock kinds compacting handles (sigLock, sendWithDeadline master-reclaim,
// tagAlong sender-reclaim) mixed in ONE transaction.

package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

const (
	cpInitAmount = 1_000_000_000_000
	cpSWDAmount  = 500_000_000
	cpTagFee     = 500
	cpAccept     = uint32(60)
	cpCleanup    = uint32(1100)
)

type compactEnv struct {
	u          *utxodb.UTXODB
	priv       ed25519.PrivateKey
	addr       ledger.SigLock
	holderID   base.HolderID
	lib        *txbuildercore.Library[any]
	seedSlot   uint32                 // slot the mixed outputs were created at
	mixed      []*ledger.OutputWithID // tagAlong, SWD, plain sigLock change (in that order)
	tagAlongID base.ChainID
}

// makeCompactEnv produces, in one wallet-signed transaction, the three kinds of
// output compacting is meant to sweep back up: a tag-along fee to some
// sequencer, a sendWithDeadline the wallet is master of, and ordinary sigLock
// change.
func makeCompactEnv(t *testing.T) *compactEnv {
	t.Helper()
	env := &compactEnv{}
	env.u = utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := env.u.GenerateAddressesWithFaucetAmount(23, 2, cpInitAmount)
	env.priv, env.addr = privKeys[0], addrs[0]
	env.holderID = base.HolderID(ledger.SigLockFromED25519PrivateKey(env.priv))
	env.lib = walletLibFromGlobal(t)
	env.tagAlongID = base.RandomChainID()
	targetID := base.HolderID(ledger.SigLockFromED25519PrivateKey(privKeys[1]))

	outs, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addr.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, outs)
	in := outs[0]
	ts := in.ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(in.Output, in.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	tagOut := ledger.NewTagAlongOutput(cpTagFee, env.tagAlongID, env.holderID)
	swdOut := ledger.NewSendWithDeadlineOutput(cpSWDAmount, &ledger.SendWithDeadlineLock{
		MasterID:        env.holderID,
		TargetID:        targetID,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: cpAccept,
		CleanupSlots:    cpCleanup,
	})
	changeOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(in.Output.TokenBalance() - cpTagFee - cpSWDAmount).WithLock(env.addr)
	})

	produced := []*ledger.Output{tagOut, swdOut, changeOut}
	idxs := make([]byte, 0, len(produced))
	for _, o := range produced {
		idx, perr := txb.ProduceOutput(o)
		require.NoError(t, perr)
		idxs = append(idxs, idx)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(env.priv)

	txBytes, txid, failed, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "seed tx must validate:\n%s", failed)
	require.NoError(t, env.u.AddTransaction(txBytes))

	env.seedSlot = ts.Slot
	for i, idx := range idxs {
		env.mixed = append(env.mixed, &ledger.OutputWithID{
			ID:     base.MustNewOutputID(txid, idx),
			Output: produced[i],
		})
	}
	return env
}

// classifySimple asserts every seeded output is SpendSimple at targetSlot and
// returns them as builder inputs — the same gate `proxi node compact` applies
// before composing.
func (env *compactEnv) classifySimple(t *testing.T, targetSlot uint32) []txbuildercore.CompactInput {
	t.Helper()
	lib := ledger.L(base.MaxSlot)
	ret := make([]txbuildercore.CompactInput, 0, len(env.mixed))
	for _, o := range env.mixed {
		cls, err := txbuildercore.ClassifySpendable(lib, o.Output.Bytes(), o.ID.Slot(), env.holderID, targetSlot, lib.TagAlongSlots)
		require.NoError(t, err)
		require.Equal(t, txbuildercore.SpendSimple, cls, "output %s must be simply claimable", o.ID.StringShort())
		ret = append(ret, txbuildercore.CompactInput{OutputBytes: o.Output.Bytes(), ID: o.ID})
	}
	return ret
}

// TestCompactMixedLockKinds sweeps a tag-along, a sendWithDeadline the wallet
// masters, and plain change in one transaction, at a slot where every window
// has opened. The reference unlocks the builder puts on inputs 1..n are inert
// on the two conditional locks — the ledger falls back to the signer check —
// so this also pins that mixing kinds stays valid now that reference unlock is
// narrowed to the plain sigLock.
func TestCompactMixedLockKinds(t *testing.T) {
	env := makeCompactEnv(t)
	lib := ledger.L(base.MaxSlot)
	// Δ past both the SWD acceptance window and the sequencer's tag-along window.
	targetSlot := env.seedSlot + cpAccept + lib.TagAlongSlots

	inputs := env.classifySimple(t, targetSlot)
	txBytes, txid, consumed, err := txbuildercore.MakeCompactTransaction(env.lib, lib.Constants, txbuildercore.CompactParams{
		Inputs:           inputs,
		WalletPrivateKey: env.priv,
		TagAlongSeqID:    env.tagAlongID,
		TagAlongFee:      cpTagFee,
		TargetSlot:       targetSlot,
	})
	require.NoError(t, err)
	require.Len(t, consumed, len(env.mixed))
	require.NoError(t, env.u.AddTransaction(txBytes), "compact tx over mixed lock kinds must settle")

	// One sigLock output carrying everything but the newly paid tag-along fee.
	swept, err := env.u.SugaredStateReader().GetOutputWithID(base.MustNewOutputID(txid, 0))
	require.NoError(t, err)
	require.EqualValues(t, cpInitAmount-cpTagFee, swept.Output.TokenBalance())
}

// A tag-along still inside the sequencer's exclusive window is not the
// sender's to take, so the classifier withholds it — and the ledger refuses it
// even if a caller composes the sweep anyway.
func TestCompactTagAlongWithheldInsideSequencerWindow(t *testing.T) {
	env := makeCompactEnv(t)
	lib := ledger.L(base.MaxSlot)
	early := env.seedSlot + lib.TagAlongSlots - 1

	tagOut := env.mixed[0]
	cls, err := txbuildercore.ClassifySpendable(lib, tagOut.Output.Bytes(), tagOut.ID.Slot(), env.holderID, early, lib.TagAlongSlots)
	require.NoError(t, err)
	require.Equal(t, txbuildercore.SpendNotForAccount, cls,
		"the sender has no claim while the target sequencer can still take the fee")

	txBytes, _, _, err := txbuildercore.MakeCompactTransaction(env.lib, lib.Constants, txbuildercore.CompactParams{
		Inputs:           []txbuildercore.CompactInput{{OutputBytes: tagOut.Output.Bytes(), ID: tagOut.ID}},
		WalletPrivateKey: env.priv,
		TagAlongSeqID:    env.tagAlongID,
		TagAlongFee:      0,
		TargetSlot:       early,
	})
	require.NoError(t, err)
	require.Error(t, env.u.AddTransaction(txBytes),
		"the ledger must reject a sender reclaim inside the sequencer's tag-along window")
}

// The builder's timestamp must clear the newest consumed input by the
// transaction pace: sweeping an output produced later in the same slot than
// the aimed-at tick would otherwise yield a transaction invalid on arrival.
func TestCompactTimestampClearsNewestInput(t *testing.T) {
	env := makeCompactEnv(t)
	lib := ledger.L(base.MaxSlot)

	// Aim at the very slot the inputs were created in. Assert first that a
	// fixed tick really would land at or before the input, so this test cannot
	// quietly go vacuous if the seed timing shifts.
	change := env.mixed[2]
	require.False(t, base.T(env.seedSlot, 10).After(change.ID.Timestamp()),
		"precondition: a fixed-tick timestamp must be at or before the input for this test to bite")

	txBytes, txid, _, err := txbuildercore.MakeCompactTransaction(env.lib, lib.Constants, txbuildercore.CompactParams{
		Inputs:           []txbuildercore.CompactInput{{OutputBytes: change.Output.Bytes(), ID: change.ID}},
		WalletPrivateKey: env.priv,
		TagAlongSeqID:    env.tagAlongID,
		TagAlongFee:      cpTagFee,
		TargetSlot:       env.seedSlot,
	})
	require.NoError(t, err)
	require.True(t, txid.Timestamp().After(change.ID.Timestamp()),
		"compact timestamp must be strictly after the consumed input")
	require.NoError(t, env.u.AddTransaction(txBytes), "same-slot compact must still settle")
}
