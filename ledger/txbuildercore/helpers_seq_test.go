package txbuildercore_test

// Byte-identity tests for the txbuildercore Phase-A wallet helpers:
// sequencer-request output composition + ensureStopDelegation. Each
// test composes a request output via the wallet helper and asserts
// the bytes match the corresponding sequencer-side helper in
// sequencer/txbuilder_seq.

import (
	"testing"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/stretchr/testify/require"
)

// fixtures used by every test below.
func seqRequestFixtures(t *testing.T) (lib *txbuildercore.Library[any], target base.ChainID, sender ledger.SigLock, senderID base.HolderID) {
	t.Helper()
	lib = txbuildercoreLibFromGlobal(t)
	for i := range target {
		target[i] = byte(i + 100)
	}
	var raw base.HolderID
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	sender = ledger.SigLock(raw)
	senderID = raw
	return
}

// TestNewSequencerRequestOutput_Withdraw mirrors the
// sequencer/txbuilder_seq.NewWithdrawRequestOutput composition.
func TestNewSequencerRequestOutput_Withdraw(t *testing.T) {
	lib, target, sender, senderID := seqRequestFixtures(t)

	const fee uint64 = 500
	const amount uint64 = 1_000_000
	// Tag-along target picked as withdraw target source (same kind of
	// arbitrary controller for the test).
	withdrawTarget := ledger.SigLock(senderID)

	// Wallet path.
	params := smallkv.New()
	params.Set(txbuilder_seq.FieldWithdrawAmount, easyfl_util.TrimmedLeadingZeroUint64(amount))
	params.Set(txbuilder_seq.FieldWithdrawTarget, []byte(withdrawTarget.Source()))
	walletOut, err := lib.NewSequencerRequestOutput(
		fee, target, senderID,
		txbuilder_seq.RequestCodeWithdrawFromSeq, &params,
	)
	require.NoError(t, err)

	// Server path.
	serverOut := txbuilder_seq.NewWithdrawRequestOutput(target, sender, fee, amount, withdrawTarget)

	require.Equal(t, serverOut.Bytes(), walletOut.Bytes())
}

// TestNewSequencerRequestOutput_SetSeqData mirrors
// NewSeqDataCommandOutput.
func TestNewSequencerRequestOutput_SetSeqData(t *testing.T) {
	lib, target, sender, senderID := seqRequestFixtures(t)

	const fee uint64 = 500
	newParams := seqdata.SequencerData{} // zero is fine; bytes are deterministic

	// Wallet path.
	params := smallkv.New()
	params.Set(txbuilder_seq.FieldSetSequencerDataBinary, newParams.Bytes())
	walletOut, err := lib.NewSequencerRequestOutput(
		fee, target, senderID,
		txbuilder_seq.RequestCodeSetSequencerData, &params,
	)
	require.NoError(t, err)

	// Server path.
	serverOut := txbuilder_seq.NewSeqDataCommandOutput(target, sender, fee, &newParams)

	require.Equal(t, serverOut.Bytes(), walletOut.Bytes())
}

// TestNewSequencerRequestOutput_AskStopDelegation mirrors
// NewAskStopDelegationReqOutput. This is the case with a slot-4
// `ensureStopDelegation` extra constraint.
func TestNewSequencerRequestOutput_AskStopDelegation(t *testing.T) {
	lib, target, sender, senderID := seqRequestFixtures(t)

	const fee uint64 = 500
	var delegationID base.ChainID
	for i := range delegationID {
		delegationID[i] = byte(i + 200)
	}

	// both allowance forms must be byte-identical between the two paths: 0 is
	// encoded as empty inline data, a real value as a trimmed uint64
	for _, allowance := range []uint64{0, 4_200_000} {
		// Wallet path.
		extra, err := lib.NewEnsureStopDelegationConstraint(delegationID, allowance)
		require.NoError(t, err)
		params := smallkv.New()
		params.Set(txbuilder_seq.FieldRevokeDelegationID, delegationID[:])
		walletOut, err := lib.NewSequencerRequestOutput(
			fee, target, senderID,
			txbuilder_seq.RequestCodeAskStopDelegation, &params,
			extra,
		)
		require.NoError(t, err)

		// Server path.
		serverOut := txbuilder_seq.NewAskStopDelegationReqOutput(target, sender, delegationID, fee, allowance)

		require.Equal(t, serverOut.Bytes(), walletOut.Bytes())
	}
}

// TestNewEnsureStopDelegationConstraint_ByteIdentity checks the
// ensureStopDelegation bytecode standalone matches the ledger.* path.
func TestNewEnsureStopDelegationConstraint_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	var chainID base.ChainID
	for i := range chainID {
		chainID[i] = byte(i + 50)
	}
	for _, allowance := range []uint64{0, 1, 4_200_000} {
		walletBin, err := lib.NewEnsureStopDelegationConstraint(chainID, allowance)
		require.NoError(t, err)
		serverBin := (&ledger.EnsureStopDelegation{ChainID: chainID, Allowance: allowance}).Bytes()
		require.Equal(t, serverBin, walletBin)
	}
}
