package vrf

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"filippo.io/edwards25519"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// Equivalence of this package with its Rust twin in rust/ (crate proxima-vrf),
// checked through the crate's vrf_tool binary: the same key and message must
// give the same proof bytes and the same output on both sides, each side must
// verify the other's proofs, and both must reject the same malformed inputs.
// Skipped when cargo is not installed.

func hexOrDash(b []byte) string {
	if len(b) == 0 {
		return "-"
	}
	return hex.EncodeToString(b)
}

func TestRustEquivalence(t *testing.T) {
	bin := testutil.CargoBin(t, "rust", "vrf_tool")
	tool := testutil.StartLineTool(t, bin, "")
	call := func(format string, args ...any) string {
		return tool.Call(t, fmt.Sprintf(format, args...))
	}

	// the RFC vectors, through every tool command
	for _, v := range rfc9381TAIVectors {
		alpha := hexOrDash(mustHex(t, v.alpha))
		require.Equal(t, v.beta, call("output %s %s", v.sk, alpha), v.name)
		require.Equal(t, v.pi, call("prove %s %s", v.sk, alpha), v.name)
		require.Equal(t, v.beta, call("verify %s %s %s", v.pk, alpha, v.pi), v.name)
		require.Equal(t, v.beta, call("hash %s", v.pi), v.name)
	}

	// random keys and messages: Go proves, Rust must produce the identical
	// proof and accept Go's; Rust's output must be Go's output
	for i := 0; i < 30; i++ {
		pk, sk, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		for _, n := range []int{0, 1, 32, 100, 1000} {
			alpha := make([]byte, n)
			rand.Read(alpha)
			pi, err := Prove(sk, alpha)
			require.NoError(t, err)
			beta, err := Verify(pk, alpha, pi)
			require.NoError(t, err)

			piHex, betaHex := hex.EncodeToString(pi), hex.EncodeToString(beta)
			require.Equal(t, piHex, call("prove %x %s", sk.Seed(), hexOrDash(alpha)))
			require.Equal(t, piHex, call("prove64 %x %s", []byte(sk), hexOrDash(alpha)))
			require.Equal(t, betaHex, call("output %x %s", sk.Seed(), hexOrDash(alpha)))
			require.Equal(t, betaHex, call("verify %x %s %s", pk, hexOrDash(alpha), piHex))
			require.Equal(t, betaHex, call("hash %s", piHex))
		}
	}

	// both sides reject the same tampering
	sk := ed25519.NewKeyFromSeed(mustHex(t, rfc9381TAIVectors[0].sk))
	pk := sk.Public().(ed25519.PublicKey)
	alpha := []byte("hello")
	pi, err := Prove(sk, alpha)
	require.NoError(t, err)
	rejectsBoth := func(what string, pk ed25519.PublicKey, alpha, pi []byte) {
		_, err := Verify(pk, alpha, pi)
		require.Error(t, err, "Go: %s", what)
		resp := call("verify %x %s %s", pk, hexOrDash(alpha), hexOrDash(pi))
		require.True(t, strings.HasPrefix(resp, "ERR"), "Rust: %s: %s", what, resp)
	}
	rejectsBoth("wrong message", pk, []byte("HELLO"), pi)
	for _, idx := range []int{0, ptLen, ptLen + cLen} {
		bad := append([]byte(nil), pi...)
		bad[idx] ^= 0x01
		rejectsBoth(fmt.Sprintf("flipped byte %d", idx), pk, alpha, bad)
	}
	rejectsBoth("truncated", pk, alpha, pi[:ProofLen-1])
	rejectsBoth("extended", pk, alpha, append(append([]byte(nil), pi...), 0))
	otherPk, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rejectsBoth("other key", otherPk, alpha, pi)
	// s + q: non-canonical scalar
	sLE := pi[ptLen+cLen:]
	s, err := edwards25519.NewScalar().SetCanonicalBytes(sLE)
	require.NoError(t, err)
	_ = s
	q := []byte{0xed, 0xd3, 0xf5, 0x5c, 0x1a, 0x63, 0x12, 0x58, 0xd6, 0x9c, 0xf7, 0xa2, 0xde, 0xf9, 0xde, 0x14, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x10}
	sPlusQ := make([]byte, 32)
	carry := 0
	for i := 0; i < 32; i++ {
		v := int(sLE[i]) + int(q[i]) + carry
		sPlusQ[i] = byte(v)
		carry = v >> 8
	}
	rejectsBoth("non-canonical s", pk, alpha, append(append([]byte(nil), pi[:ptLen+cLen]...), sPlusQ...))

	// small-order keys with the forged proof that passes without the key check
	for _, badPk := range smallOrderKeys(t) {
		rejectsBoth("small-order key", badPk, alpha, forgeSmallOrderProof(t, badPk, alpha))
	}
}

// smallOrderKeys are the two small-order points with a canonical encoding
// that a public key field could carry: the identity and the order-2 point.
func smallOrderKeys(t *testing.T) [][]byte {
	orderTwo := make([]byte, 32) // y = p - 1 = 2^255 - 20, little-endian, sign bit 0
	orderTwo[0] = 0xec
	for i := 1; i < 31; i++ {
		orderTwo[i] = 0xff
	}
	orderTwo[31] = 0x7f
	p2, err := new(edwards25519.Point).SetBytes(orderTwo)
	require.NoError(t, err)
	require.Equal(t, 1, new(edwards25519.Point).MultByCofactor(p2).Equal(edwards25519.NewIdentityPoint()), "test premise: (0,-1) has small order")
	return [][]byte{edwards25519.NewIdentityPoint().Bytes(), orderTwo}
}

// forgeSmallOrderProof is the proof that verifies under a small-order key
// when ECVRF_validate_key is missing: Gamma = identity, U = k*B, V = k*H, s = k.
func forgeSmallOrderProof(t *testing.T, pk, alpha []byte) []byte {
	identity := edwards25519.NewIdentityPoint()
	h, err := encodeToCurveTAI(pk, alpha)
	require.NoError(t, err)
	kb := sha512.Sum512([]byte("any nonce"))
	k, err := edwards25519.NewScalar().SetUniformBytes(kb[:])
	require.NoError(t, err)
	u := new(edwards25519.Point).ScalarBaseMult(k)
	v := new(edwards25519.Point).ScalarMult(k, h)
	y, err := new(edwards25519.Point).SetBytes(pk)
	require.NoError(t, err)
	c := challengeGeneration(y, h, identity, u, v)
	return append(append(identity.Bytes(), c...), k.Bytes()...)
}
