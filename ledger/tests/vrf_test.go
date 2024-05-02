package tests

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
	"github.com/yoseplee/vrf"
)

func TestVrfPackage(t *testing.T) {
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	s := ledger.Slot(12345)
	pi, _, err := vrf.Prove(pubKey, privKey, s.Bytes())
	require.NoError(t, err)
	t.Logf("pi (%d): %s", len(pi), hex.EncodeToString(pi))
	ok, err := vrf.Verify(pubKey, pi, s.Bytes())
	require.NoError(t, err)
	require.True(t, ok)

	sWrong := ledger.Slot(123)
	ok, err = vrf.Verify(pubKey, pi, sWrong.Bytes())
	require.NoError(t, err)
	require.False(t, ok)

	pi[0] += 1
	ok, err = vrf.Verify(pubKey, pi, s.Bytes())
	require.True(t, !ok || err != nil)
	require.False(t, ok)
}

func TestVrfLibExtension(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		ledger.L().MustEqual("vrfVerify(nil, nil, nil)", "false")
	})
	t.Run("ok", func(t *testing.T) {
		pubKey, privKey, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		slot := ledger.Slot(12345)
		proof, _, err := vrf.Prove(pubKey, privKey, slot.Bytes())
		require.NoError(t, err)

		ok, err := vrf.Verify(pubKey, proof, slot.Bytes())
		require.NoError(t, err)
		require.True(t, ok)

		src := fmt.Sprintf("vrfVerify(0x%s, 0x%s, 0x%s)",
			hex.EncodeToString(pubKey),
			hex.EncodeToString(proof),
			hex.EncodeToString(slot.Bytes()),
		)
		_, _, bytecode, err := ledger.L().CompileExpression(src)
		t.Logf("bytecode size: %d", len(bytecode))

		t.Logf("src: %s", src)
		ledger.L().MustTrue(src)
	})
	t.Run("fail 1", func(t *testing.T) {
		pubKey, privKey, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		slot := ledger.Slot(12345)
		proof, _, err := vrf.Prove(pubKey, privKey, slot.Bytes())
		require.NoError(t, err)
		ok, err := vrf.Verify(pubKey, proof, slot.Bytes())
		require.NoError(t, err)
		require.True(t, ok)

		// modify proof
		proof[0] += 1
		ok, err = vrf.Verify(pubKey, proof, slot.Bytes())
		require.True(t, !ok || err != nil)

		src := fmt.Sprintf("vrfVerify(0x%s, 0x%s, 0x%s)",
			hex.EncodeToString(pubKey),
			hex.EncodeToString(proof),
			hex.EncodeToString(slot.Bytes()),
		)
		t.Logf("src: %s", src)
		ledger.L().MustEqual(src, "false")
	})
	t.Run("fail 2", func(t *testing.T) {
		pubKey, privKey, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		slot := ledger.Slot(12345)
		proof, _, err := vrf.Prove(pubKey, privKey, slot.Bytes())
		require.NoError(t, err)
		ok, err := vrf.Verify(pubKey, proof, slot.Bytes())
		require.NoError(t, err)
		require.True(t, ok)

		// modify slot
		slotWrong := ledger.Slot(12346)
		ok, err = vrf.Verify(pubKey, proof, slotWrong.Bytes())
		require.True(t, !ok || err != nil)

		src := fmt.Sprintf("vrfVerify(0x%s, 0x%s, 0x%s)",
			hex.EncodeToString(pubKey),
			hex.EncodeToString(proof),
			hex.EncodeToString(slotWrong.Bytes()),
		)
		t.Logf("src: %s", src)
		ledger.L().MustEqual(src, "false")
	})
}
