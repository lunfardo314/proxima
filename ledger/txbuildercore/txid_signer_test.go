package txbuildercore_test

import (
	"crypto/ed25519"
	"encoding/binary"
	"testing"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// The transaction ID from TxVersionSignerBoundID on (kb/library_upgrade6_mining.md):
// the essence starts with the signer's holder ID and length-prefixes every element,
// so the ID commits to the signer and to element boundaries. Older versions keep the
// old essence, so stored IDs do not change.

// signedTx builds a minimal transaction of the given version with one output and
// one transaction-level constraint, signed by priv, and returns its wire bytes.
func signedTx(t *testing.T, version uint16, priv ed25519.PrivateKey) []byte {
	txb := txbuildercore.New(version)
	out := txbuildercore.NewOutputBuilder()
	out.MustPushConstraint([]byte{0x01})
	out.MustPushConstraint(nil)
	out.MustPushConstraint([]byte{0x80})
	txb.ProduceOutput(out.Bytes())
	txb.PushTxConstraint([]byte{0x81, 0x07})
	txb.SetTimestamp(base.T(100, 1))
	txb.SignED25519(priv)
	return txb.Bytes()
}

// elementsOf returns the top-level elements of the wire bytes.
func elementsOf(t *testing.T, raw []byte) [][]byte {
	tu, err := tuples.TupleFromBytes(raw, txbuildercore.MaxNumConstraints)
	require.NoError(t, err)
	ret := make([][]byte, tu.NumElements())
	for i := range ret {
		ret[i], err = tu.At(i)
		require.NoError(t, err)
	}
	return ret
}

// withElements re-encodes the wire bytes with some elements replaced.
func withElements(t *testing.T, raw []byte, replace map[byte][]byte) []byte {
	elems := elementsOf(t, raw)
	for i, b := range replace {
		elems[i] = b
	}
	return tuples.MakeTupleFromDataElements(elems...).Bytes()
}

// expectedID computes the version 6 ID from the spec's formula, independently of
// the implementation: blake2b(holder ID || (uint32 length || element)...) without
// the signature element, its first 6 bytes overwritten by the timestamp and the
// output count.
func expectedID(t *testing.T, raw []byte, signer base.HolderID) base.TransactionID {
	elems := elementsOf(t, raw)
	h, _ := blake2b.New256(nil)
	h.Write(signer[:])
	for i, e := range elems {
		if byte(i) == txbuildercore.TxSignatureData {
			continue
		}
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(e)))
		h.Write(l[:])
		h.Write(e)
	}
	var ret base.TransactionID
	copy(ret[:], h.Sum(nil))
	copy(ret[:], elems[txbuildercore.TxTimestamp])
	outs, err := tuples.TupleFromBytes(elems[txbuildercore.TxOutputs])
	require.NoError(t, err)
	ret[base.LedgerTimeByteLength] = byte(outs.NumElements() - 1)
	return ret
}

func TestTxIDSignerBound(t *testing.T) {
	_, priv1, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, priv2, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	v6 := uint16(txbuildercore.TxVersionSignerBoundID)

	t.Run("version 6 follows the formula and the signature verifies", func(t *testing.T) {
		raw := signedTx(t, v6, priv1)
		id, err := txbuildercore.TxIDFromBytes(raw)
		require.NoError(t, err)
		require.Equal(t, expectedID(t, raw, base.HolderIDFromED25519PrivateKey(priv1)), id)

		sig, err := base.SignatureFromBytes(elementsOf(t, raw)[txbuildercore.TxSignatureData])
		require.NoError(t, err)
		require.True(t, ed25519.Verify(sig.MustPubicKeyED25519(), id[:], sig.MustSignatureDataED25519()))
	})

	t.Run("version 6: two signers, two IDs; version 5: one", func(t *testing.T) {
		id1, err := txbuildercore.TxIDFromBytes(signedTx(t, v6, priv1))
		require.NoError(t, err)
		id2, err := txbuildercore.TxIDFromBytes(signedTx(t, v6, priv2))
		require.NoError(t, err)
		require.NotEqual(t, id1, id2)

		old1, err := txbuildercore.TxIDFromBytes(signedTx(t, v6-1, priv1))
		require.NoError(t, err)
		old2, err := txbuildercore.TxIDFromBytes(signedTx(t, v6-1, priv2))
		require.NoError(t, err)
		require.Equal(t, old1, old2, "before version 6 the ID does not depend on the signer")
	})

	t.Run("bytes moved across an element boundary: same ID before version 6, another from it", func(t *testing.T) {
		for _, version := range []uint16{v6 - 1, v6} {
			raw := signedTx(t, version, priv1)
			elems := elementsOf(t, raw)
			moved := withElements(t, raw, map[byte][]byte{
				txbuildercore.TxEndorsements: append(append([]byte{}, elems[txbuildercore.TxEndorsements]...), elems[txbuildercore.TxConstraints]...),
				txbuildercore.TxConstraints:  nil,
			})
			id, err := txbuildercore.TxIDFromBytes(raw)
			require.NoError(t, err)
			movedID, err := txbuildercore.TxIDFromBytes(moved)
			require.NoError(t, err)
			if version < v6 {
				require.Equal(t, id, movedID)
			} else {
				require.NotEqual(t, id, movedID)
			}
		}
	})

	t.Run("version 6 refuses signature data not in its exact encoding", func(t *testing.T) {
		raw := signedTx(t, v6, priv1)
		sig := elementsOf(t, raw)[txbuildercore.TxSignatureData]
		for name, bad := range map[string][]byte{
			"padded":       append(append([]byte{}, sig...), 0xee),
			"short":        sig[:len(sig)-1],
			"unknown type": append([]byte{0x7f}, sig[1:]...),
			"empty":        nil,
		} {
			_, err := txbuildercore.TxIDFromBytes(withElements(t, raw, map[byte][]byte{txbuildercore.TxSignatureData: bad}))
			require.Error(t, err, name)
		}
		// before version 6 the signature data does not enter the ID at all
		old := signedTx(t, v6-1, priv1)
		oldSig := elementsOf(t, old)[txbuildercore.TxSignatureData]
		_, err := txbuildercore.TxIDFromBytes(withElements(t, old, map[byte][]byte{txbuildercore.TxSignatureData: append(append([]byte{}, oldSig...), 0xee)}))
		require.NoError(t, err)
	})
}
