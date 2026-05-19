package txcore_test

// Byte-identity tests for the Phase-E redeemer wallet helpers:
// redeemScript / callRedeemer bytecode emission plus the
// LocalScriptHash pure function. A round-trip test confirms the
// emitted bytes survive the tx serialise / re-parse cycle.

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txcore"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// TestNewRedeemScriptConstraint_ByteIdentity compiles a tiny local
// script and verifies the wallet-emitted redeemScript bytecode
// matches the inline reference compile of the same source.
func TestNewRedeemScriptConstraint_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	bin, err := lib.Inner.CompileLocalScript("func _f : 0")
	require.NoError(t, err)
	require.NotEmpty(t, bin)

	walletBin, err := lib.NewRedeemScriptConstraint(bin)
	require.NoError(t, err)

	refSrc := fmt.Sprintf("%s(0x%s)", txcore.RedeemScriptName, hex.EncodeToString(bin))
	refBin, err := lib.CompileExpression(refSrc)
	require.NoError(t, err)
	require.Equal(t, refBin, walletBin)
}

// TestLocalScriptHash_Determinism checks the pure blake2b.Sum256
// wrapper produces the expected canonical hash for a fixed input.
func TestLocalScriptHash_Determinism(t *testing.T) {
	bin := []byte{0xde, 0xad, 0xbe, 0xef}
	got := txcore.LocalScriptHash(bin)
	want := blake2b.Sum256(bin)
	require.Equal(t, want, got)
}

// TestNewCallRedeemerConstraint_ByteIdentity exercises both the
// no-args and variadic-args forms; the wallet bytecode must match
// the inline reference compile in either case.
func TestNewCallRedeemerConstraint_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	var hash [32]byte
	for i := range hash {
		hash[i] = byte(i + 1)
	}
	const fnIdx byte = 0x07

	// No-args form.
	{
		walletBin, err := lib.NewCallRedeemerConstraint(hash, fnIdx)
		require.NoError(t, err)
		refSrc := fmt.Sprintf("%s(0x%s, 0x%02x)", txcore.CallRedeemerName, hex.EncodeToString(hash[:]), fnIdx)
		refBin, err := lib.CompileExpression(refSrc)
		require.NoError(t, err)
		require.Equal(t, refBin, walletBin)
	}

	// Variadic-args form: two typed literals.
	{
		walletBin, err := lib.NewCallRedeemerConstraint(hash, fnIdx, "z64/12345", "0xdeadbeef")
		require.NoError(t, err)
		refSrc := fmt.Sprintf("%s(0x%s, 0x%02x, z64/12345, 0xdeadbeef)",
			txcore.CallRedeemerName, hex.EncodeToString(hash[:]), fnIdx)
		refBin, err := lib.CompileExpression(refSrc)
		require.NoError(t, err)
		require.Equal(t, refBin, walletBin)
	}
}

// TestRedeemerRoundTrip_ViaTxBuilder confirms that a redeemScript
// constraint pushed as a tx-level constraint, plus a callRedeemer
// constraint pushed on a produced output, survive tx serialisation
// and re-parsing byte-for-byte. This is the wallet's primary
// integration concern — the host validator's resolver will pick up
// the published bin as long as the wire bytes round-trip cleanly.
func TestRedeemerRoundTrip_ViaTxBuilder(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	// 1) Compile a real 1-function local script.
	bin, err := lib.Inner.CompileLocalScript("func _square : mul($0, $0)")
	require.NoError(t, err)
	require.NotEmpty(t, bin)
	hash := txcore.LocalScriptHash(bin)

	// 2) Build the wallet bytecode for both constraints.
	redeemBin, err := lib.NewRedeemScriptConstraint(bin)
	require.NoError(t, err)
	callBin, err := lib.NewCallRedeemerConstraint(hash, 0, "z64/7")
	require.NoError(t, err)

	// 3) Assemble a minimal tx: one produced output carrying the
	//    callRedeemer constraint at slot 3, plus the redeemScript
	//    constraint at tx level.
	txb := txcore.New(0)

	out := txcore.NewOutputBuilder()
	out.PutConstraint(txcore.EncodeTokenBalance(1_000), txcore.ConstraintIndexAmounts)
	out.PutConstraint(txcore.EncodeIndexValuesTuple([][]byte{make([]byte, 32)}), txcore.ConstraintIndexIndexValues)
	out.PutConstraint([]byte{0x80}, txcore.ConstraintIndexLock)
	out.MustPushConstraint(callBin)
	txb.ProduceOutput(out.Bytes())

	txb.PushTxConstraint(redeemBin)
	txb.SetTimestamp(base.T(0, 1))

	raw := txb.Bytes()
	require.NotEmpty(t, raw)

	// 4) Parse the tx tree back. txb.Bytes() returns the
	//    TransactionTuple subtree directly (not wrapped in the outer
	//    2-tuple), matching the convention in txbuilder_test.go.
	tree, err := tuples.TupleFromBytes(raw, txcore.MaxNumConstraints)
	require.NoError(t, err)
	require.Equal(t, int(txcore.TxTreeTupleNumElements), tree.NumElements())

	txcBytes, err := tree.At(int(txcore.TxConstraints))
	require.NoError(t, err)
	txcSub, err := tuples.TupleFromBytes(txcBytes, txcore.MaxNumConstraints)
	require.NoError(t, err)
	require.Equal(t, 1, txcSub.NumElements())
	gotRedeem, err := txcSub.At(0)
	require.NoError(t, err)
	require.Equal(t, redeemBin, gotRedeem)

	// 5) Reach the produced output's constraint at slot 3 and confirm
	//    it round-trips byte-for-byte.
	outsBytes, err := tree.At(int(txcore.TxOutputs))
	require.NoError(t, err)
	outsSub, err := tuples.TupleFromBytes(outsBytes, txcore.MaxNumConstraints)
	require.NoError(t, err)
	require.Equal(t, 1, outsSub.NumElements())
	out0Bytes, err := outsSub.At(0)
	require.NoError(t, err)
	out0, err := tuples.TupleFromBytes(out0Bytes, txcore.MaxNumConstraints)
	require.NoError(t, err)
	gotCall, err := out0.At(3)
	require.NoError(t, err)
	require.Equal(t, callBin, gotCall)
}
