package tests

import (
	"bytes"
	"testing"

	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// The transaction ID does not cover the signature, so bytes with a corrupted signature still parse
// to the same ID: only VerifySignature tells them apart. The dagstore relies on it, since it stores
// transactions it has only parsed.
func TestVerifySignature(t *testing.T) {
	u, privKey, _ := newTestEnv(t, 1_000_000_000)
	_, _, dstAddr := u.GenerateAddress(2)
	tx, err := u.TransferTokensReturnTx(privKey, dstAddr, 100_000_000)
	require.NoError(t, err)

	sig, err := tx.VerifySignature()
	require.NoError(t, err)

	raw := bytes.Clone(tx.Bytes())
	sigData := sig.MustSignatureDataED25519()
	pos := bytes.Index(raw, sigData)
	require.Positive(t, pos, "signature not found in the transaction bytes")
	raw[pos] ^= 0x01

	forged, err := transaction.Parse(raw)
	require.NoError(t, err)
	require.Equal(t, tx.ID(), forged.ID(), "the ID changed with the signature")
	_, err = forged.VerifySignature()
	require.Error(t, err)
}
