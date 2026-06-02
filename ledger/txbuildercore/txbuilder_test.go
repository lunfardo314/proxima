package txbuildercore_test

// Smoke tests for the wasm-wallet-facing txbuildercore.TxBuilder. These don't
// exercise validator semantics — they just verify the raw compose
// surface produces the wire format the server-side parsers expect.

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestTxBuilder_Empty checks the initial state of a fresh builder:
// no inputs, no outputs, sequencer-output-index marked None so the
// serializer omits the sequencer-data slot.
func TestTxBuilder_Empty(t *testing.T) {
	txb := txbuildercore.New(0)
	require.Equal(t, 0, txb.NumInputs())
	require.Equal(t, 0, txb.NumOutputs())
	require.Equal(t, txbuildercore.SequencerOutputIndexNone, txb.TxData.SequencerOutputIndex)

	// Serialise — empty tx should round-trip through the tuple
	// machinery without panicking.
	raw := txb.Bytes()
	require.NotEmpty(t, raw)
}

// TestTxBuilder_ConsumeProduce exercises the basic compose flow:
// register one input, one output, lay down a signature unlock at
// input 0, set timestamp, compute input commitment, serialise.
// Verifies the produced bytes parse back as a tuple of the expected
// shape.
func TestTxBuilder_ConsumeProduce(t *testing.T) {
	txb := txbuildercore.New(0)

	// Build an empty-shell consumed output: amounts | empty-index-values | empty-lock.
	consumed := txbuildercore.NewOutputBuilder()
	consumed.MustPushConstraint([]byte{0x01, 0x02, 0x03}) // pretend amounts
	consumed.MustPushConstraint(nil)                       // index-values
	consumed.MustPushConstraint([]byte{0x80})              // pretend lock (inline-data short prefix)
	consumedBytes := consumed.Bytes()

	var txid base.TransactionID
	oid := base.MustNewOutputID(txid, 0)
	require.Equal(t, byte(0), txb.ConsumeOutput(consumedBytes, oid))
	require.Equal(t, 1, txb.NumInputs())

	// Produced output (same shape).
	produced := txbuildercore.NewOutputBuilder()
	produced.MustPushConstraint([]byte{0x04, 0x05, 0x06})
	produced.MustPushConstraint(nil)
	produced.MustPushConstraint([]byte{0x80})
	producedBytes := produced.Bytes()
	require.Equal(t, byte(0), txb.ProduceOutput(producedBytes))
	require.Equal(t, 1, txb.NumOutputs())

	txb.PutSignatureUnlock(0)
	txb.SetTimestamp(base.T(0, 1))
	txb.ComputeInputCommitment()

	raw := txb.Bytes()
	require.NotEmpty(t, raw)

	// Parse back as the outer transaction-tree tuple and confirm
	// the slot count matches the wire-format constant.
	tree, err := tuples.TupleFromBytes(raw, txbuildercore.MaxNumConstraints)
	require.NoError(t, err)
	require.Equal(t, int(txbuildercore.TxTreeTupleNumElements), tree.NumElements())
}

// TestTxBuilder_SignED25519 verifies the signing path: derive the tx
// ID from the tree, sign it, and check the wire SignatureData layout
// (sig-type byte || sig || pubkey) plus signature verification.
func TestTxBuilder_SignED25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	txb := txbuildercore.New(0)

	// One produced output (TxIDFromTree requires nUTXO > 0).
	out := txbuildercore.NewOutputBuilder()
	out.MustPushConstraint([]byte{0x01})
	out.MustPushConstraint(nil)
	out.MustPushConstraint([]byte{0x80})
	txb.ProduceOutput(out.Bytes())

	txb.SetTimestamp(base.T(0, 1))
	txb.SignED25519(priv)

	sd := txb.TxData.SignatureData
	require.Len(t, sd, 1+ed25519.SignatureSize+ed25519.PublicKeySize)
	require.Equal(t, base.SignatureTypeED25519, sd[0])

	sig := sd[1 : 1+ed25519.SignatureSize]
	pubFromSD := sd[1+ed25519.SignatureSize:]
	require.True(t, ed25519.PublicKey(pubFromSD).Equal(pub))

	// Re-derive the txid from the bytes the wallet would emit and
	// verify the sig against that.
	raw := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(raw)
	require.NoError(t, err)
	require.True(t, ed25519.Verify(pub, txid[:], sig))
}

// TestTxBuilder_UnlockReference checks that PutUnlockReference rejects
// non-strictly-decreasing references (the validator enforces this; we
// catch it client-side at compose time).
func TestTxBuilder_UnlockReference(t *testing.T) {
	txb := txbuildercore.New(0)
	var txid base.TransactionID
	oid0 := base.MustNewOutputID(txid, 0)
	oid1 := base.MustNewOutputID(txid, 1)
	txb.ConsumeOutput([]byte{0x80}, oid0)
	txb.ConsumeOutput([]byte{0x80}, oid1)

	// Valid: input 1 references input 0.
	require.NoError(t, txb.PutUnlockReference(1, txbuildercore.ConstraintIndexLock, 0))

	// Invalid: input 1 references input 1 (not strictly less).
	require.Error(t, txb.PutUnlockReference(1, txbuildercore.ConstraintIndexLock, 1))
}
