// Tests for the blake2b and validSignatureED25519 EasyFL builtins.
// These builtins lived in the easyfl base library until 2026-05-18;
// they were moved into proxima because no other easyfl consumer needed
// them. The tests came with them.
package tests

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// TestBlake2bBuiltin verifies the blake2b(...) embedded function returns
// the same 32-byte blake2b-256 hash as the Go reference implementation,
// for both single-arg and concat-of-many-args cases.
func TestBlake2bBuiltin(t *testing.T) {
	lib := ledger.L(base.MaxSlot)

	// len(blake2b(1)) == 32.
	lib.MustEqual("len(blake2b(1))", "u64/32")

	// blake2b(1) matches Go reference.
	h := blake2b.Sum256([]byte{1})
	lib.MustEqual("blake2b(1)", fmt.Sprintf("0x%s", hex.EncodeToString(h[:])))

	// Varargs: blake2b(0x01, 0x02, 0x03) == blake2b.Sum256({0x01,0x02,0x03}).
	h3 := blake2b.Sum256([]byte{0x01, 0x02, 0x03})
	lib.MustEqual("blake2b(0x01, 0x02, 0x03)", fmt.Sprintf("0x%s", hex.EncodeToString(h3[:])))

	// Empty arg list still hashes the empty string.
	h0 := blake2b.Sum256(nil)
	lib.MustEqual("blake2b()", fmt.Sprintf("0x%s", hex.EncodeToString(h0[:])))
}

// TestValidSignatureED25519 covers the four cases of the
// validSignatureED25519(msg, sig, pubKey) builtin: correct
// signature accepted; tampered message / signature / pubkey rejected;
// nil-args panic with "bad public key length".
func TestValidSignatureED25519(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))

	pubKey, privKey, err := ed25519.GenerateKey(rnd)
	require.NoError(t, err)
	const msg = "message to be signed"

	t.Run("ok", func(t *testing.T) {
		sig := ed25519.Sign(privKey, []byte(msg))
		res, err := lib.EvalFromSource(nil, "validSignatureED25519($0,$1,$2)", []byte(msg), sig, pubKey)
		require.NoError(t, err)
		require.True(t, len(res) > 0, "valid signature must return non-empty")
	})
	t.Run("wrong msg", func(t *testing.T) {
		sig := ed25519.Sign(privKey, []byte(msg))
		res, err := lib.EvalFromSource(nil, "validSignatureED25519($0,$1,$2)", []byte(msg+"klmn"), sig, pubKey)
		require.NoError(t, err)
		require.Equal(t, 0, len(res), "tampered message must return empty")
	})
	t.Run("wrong sig", func(t *testing.T) {
		sig := ed25519.Sign(privKey, []byte(msg))
		sig[5]++
		res, err := lib.EvalFromSource(nil, "validSignatureED25519($0,$1,$2)", []byte(msg), sig, pubKey)
		require.NoError(t, err)
		require.Equal(t, 0, len(res), "tampered signature must return empty")
	})
	t.Run("wrong pubkey", func(t *testing.T) {
		sig := ed25519.Sign(privKey, []byte(msg))
		pk := easyfl_util.Concat([]byte(pubKey))
		pk[3]++
		res, err := lib.EvalFromSource(nil, "validSignatureED25519($0,$1,$2)", []byte(msg), sig, pk)
		require.NoError(t, err)
		require.Equal(t, 0, len(res), "tampered pubKey must return empty")
	})
	t.Run("nil args panic", func(t *testing.T) {
		_, err := lib.EvalFromSource(nil, "validSignatureED25519($0,$1,$2)", nil, nil, nil)
		easyfl_util.RequireErrorWith(t, err, "bad public key length")
	})
}
