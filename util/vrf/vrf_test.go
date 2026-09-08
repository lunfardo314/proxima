package vrf

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"math/big"
	"testing"

	"filippo.io/edwards25519"
	"github.com/stretchr/testify/require"
)

// RFC 9381 Appendix B.4 official test vectors for
// ECVRF-EDWARDS25519-SHA512-TAI. If Prove reproduces pi and Verify returns
// beta for these, the implementation is byte-exact with the standard.
// h and k are the RFC's intermediate values: H = encode_to_curve(PK, alpha) and
// the nonce k, so hash-to-curve and nonce derivation are pinned individually,
// not only through the final proof. example17 needs a second try-and-increment
// round (ctr = 1), so the retry path is covered.
var rfc9381TAIVectors = []struct {
	name, sk, pk, alpha, h, k, pi, beta string
}{
	{
		name:  "example16",
		sk:    "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
		pk:    "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a",
		alpha: "",
		h:     "91bbed02a99461df1ad4c6564a5f5d829d0b90cfc7903e7a5797bd658abf3318",
		k:     "8a49edbd1492a8ee09766befe50a7d563051bf3406cbffc20a88def030730f0f",
		pi:    "8657106690b5526245a92b003bb079ccd1a92130477671f6fc01ad16f26f723f26f8a57ccaed74ee1b190bed1f479d9727d2d0f9b005a6e456a35d4fb0daab1268a1b0db10836d9826a528ca76567805",
		beta:  "90cf1df3b703cce59e2a35b925d411164068269d7b2d29f3301c03dd757876ff66b71dda49d2de59d03450451af026798e8f81cd2e333de5cdf4f3e140fdd8ae",
	},
	{
		name:  "example17",
		sk:    "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
		pk:    "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c",
		alpha: "72",
		h:     "5b659fc3d4e9263fd9a4ed1d022d75eaacc20df5e09f9ea937502396598dc551",
		k:     "d8c3a66921444cb3427d5d989f9b315aa8ca3375e9ec4d52207711a1fdb44107",
		pi:    "f3141cd382dc42909d19ec5110469e4feae18300e94f304590abdced48aed5933bf0864a62558b3ed7f2fea45c92a465301b3bbf5e3e54ddf2d935be3b67926da3ef39226bbc355bdc9850112c8f4b02",
		beta:  "eb4440665d3891d668e7e0fcaf587f1b4bd7fbfe99d0eb2211ccec90496310eb5e33821bc613efb94db5e5b54c70a848a0bef4553a41befc57663b56373a5031",
	},
	{
		name:  "example18",
		sk:    "c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7",
		pk:    "fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025",
		alpha: "af82",
		h:     "bf4339376f5542811de615e3313d2b36f6f53c0acfebb482159711201192576a",
		k:     "5ffdbc72135d936014e8ab708585fda379405542b07e3bd2c0bd48437fbac60a",
		pi:    "9bc0f79119cc5604bf02d23b4caede71393cedfbb191434dd016d30177ccbf8096bb474e53895c362d8628ee9f9ea3c0e52c7a5c691b6c18c9979866568add7a2d41b00b05081ed0f58ee5e31b3a970e",
		beta:  "645427e5d00c62a23fb703732fa5d892940935942101e456ecca7bb217c61c452118fec1219202a0edcf038bb6373241578be7217ba85a2687f7a0310b2df19f",
	},
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func TestRFC9381Vectors(t *testing.T) {
	for _, v := range rfc9381TAIVectors {
		t.Run(v.name, func(t *testing.T) {
			seed := mustHex(t, v.sk)
			sk := ed25519.NewKeyFromSeed(seed)
			pk := ed25519.PublicKey(mustHex(t, v.pk))
			require.Equal(t, []byte(pk), []byte(sk.Public().(ed25519.PublicKey)), "derived PK must match vector")
			alpha := mustHex(t, v.alpha)

			pi, err := Prove(sk, alpha)
			require.NoError(t, err)
			require.Equal(t, v.pi, hex.EncodeToString(pi), "proof must equal the RFC vector")

			beta, err := Verify(pk, alpha, pi)
			require.NoError(t, err)
			require.Equal(t, v.beta, hex.EncodeToString(beta), "beta must equal the RFC vector")

			// ProofToHash must equal the verified beta.
			b2, err := ProofToHash(pi)
			require.NoError(t, err)
			require.Equal(t, beta, b2)

			// intermediate values: hash-to-curve and the nonce, per the RFC
			h, err := encodeToCurveTAI(pk, alpha)
			require.NoError(t, err)
			require.Equal(t, v.h, hex.EncodeToString(h.Bytes()), "H must equal the RFC vector")
			_, prefix, err := secretScalarAndPrefix(seed)
			require.NoError(t, err)
			k, err := nonceGeneration(prefix, h.Bytes())
			require.NoError(t, err)
			require.Equal(t, v.k, hex.EncodeToString(k.Bytes()), "nonce k must equal the RFC vector")
		})
	}
}

// vectorKey is a fresh key derived from an RFC vector seed, for tests that
// need a valid proof to mutate.
func vectorKey(t *testing.T, i int) (ed25519.PrivateKey, ed25519.PublicKey) {
	sk := ed25519.NewKeyFromSeed(mustHex(t, rfc9381TAIVectors[i].sk))
	return sk, sk.Public().(ed25519.PublicKey)
}

// TestDecodeProofStrictness pins ECVRF_decode_proof: only an 80-byte proof with
// a decodable Gamma and a canonical s is accepted, by Verify and by ProofToHash
// alike. A non-canonical s (s + q) encodes the same scalar and would otherwise
// make the proof bytes malleable.
func TestDecodeProofStrictness(t *testing.T) {
	sk, pk := vectorKey(t, 0)
	alpha := []byte("alpha")
	pi, err := Prove(sk, alpha)
	require.NoError(t, err)

	rejects := func(name string, bad []byte) {
		t.Helper()
		_, err := Verify(pk, alpha, bad)
		require.Error(t, err, "Verify must reject: %s", name)
		_, err = ProofToHash(bad)
		require.Error(t, err, "ProofToHash must reject: %s", name)
	}

	rejects("empty proof", nil)
	rejects("truncated proof", pi[:ProofLen-1])
	rejects("extended proof", append(append([]byte(nil), pi...), 0))

	// Gamma bytes that are not a point: y = 2 has no square root for x on this
	// curve, so string_to_point must return INVALID.
	notAPoint := append([]byte(nil), pi...)
	copy(notAPoint[:ptLen], append([]byte{2}, make([]byte, ptLen-1)...))
	_, err = new(edwards25519.Point).SetBytes(notAPoint[:ptLen])
	require.Error(t, err, "test premise: 0x02 || 0^31 is not a point encoding")
	rejects("Gamma not a point", notAPoint)

	// s + q: same scalar, non-canonical encoding
	sLE := pi[ptLen+cLen:]
	sBig := new(big.Int).SetBytes(reverse(sLE))
	q, ok := new(big.Int).SetString("1000000000000000000000000000000014def9dea2f79cd65812631a5cf5d3ed", 16)
	require.True(t, ok)
	sPlusQ := reverse(new(big.Int).Add(sBig, q).FillBytes(make([]byte, qLen)))
	nonCanonical := append(append([]byte(nil), pi[:ptLen+cLen]...), sPlusQ...)
	rejects("non-canonical s", nonCanonical)
}

func reverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = b[len(b)-1-i]
	}
	return out
}

// TestVerifyRejectsWrongInputs covers the input side of Verify: a key that is
// the wrong length or not a point, a message that differs by a prefix, suffix
// or being empty, and the proof of one vector under another vector's key.
func TestVerifyRejectsWrongInputs(t *testing.T) {
	sk, pk := vectorKey(t, 1)
	alpha := []byte("predecessor||slot||nonce")
	pi, err := Prove(sk, alpha)
	require.NoError(t, err)

	_, err = Verify(pk[:31], alpha, pi)
	require.Error(t, err, "short public key")
	_, err = Verify(append([]byte{2}, make([]byte, 31)...), alpha, pi)
	require.Error(t, err, "public key that is not a point")

	for _, bad := range [][]byte{nil, alpha[:len(alpha)-1], append(append([]byte(nil), alpha...), 0), append([]byte{0}, alpha...)} {
		_, err = Verify(pk, bad, pi)
		require.Error(t, err, "message %x must not verify", bad)
	}

	// a valid proof of the same message under another key
	sk2, pk2 := vectorKey(t, 2)
	pi2, err := Prove(sk2, alpha)
	require.NoError(t, err)
	_, err = Verify(pk, alpha, pi2)
	require.Error(t, err)
	_, err = Verify(pk2, alpha, pi)
	require.Error(t, err)
}

// TestVerifyRejectsSmallOrderKey: a small-order public key has no secret scalar,
// and without ECVRF_validate_key a proof under it verifies with Gamma = identity
// and a constant output for every message. The identity and the order-2 point
// (0, -1) are the two such keys with a canonical encoding.
func TestVerifyRejectsSmallOrderKey(t *testing.T) {
	identity := edwards25519.NewIdentityPoint()
	orderTwo := make([]byte, 32) // y = p - 1 = 2^255 - 20, little-endian, sign bit 0
	orderTwo[0] = 0xec
	for i := 1; i < 31; i++ {
		orderTwo[i] = 0xff
	}
	orderTwo[31] = 0x7f
	p2, err := new(edwards25519.Point).SetBytes(orderTwo)
	require.NoError(t, err)
	require.Equal(t, 1, new(edwards25519.Point).MultByCofactor(p2).Equal(identity), "test premise: (0,-1) has small order")

	alpha := []byte("alpha")
	for _, pk := range [][]byte{identity.Bytes(), orderTwo} {
		// forge the proof that verifies when the key check is absent:
		// Gamma = identity, U = k*B, V = k*H, s = k
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
		pi := append(append(identity.Bytes(), c...), k.Bytes()...)

		_, err = Verify(pk, alpha, pi)
		require.ErrorContains(t, err, "small-order public key")
	}
}

// TestProveVerifyRandom is a round-trip over random keys and messages: every
// proof verifies, ProofToHash agrees with Verify, and outputs are distinct
// across messages and across keys.
func TestProveVerifyRandom(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		pk, sk, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		alpha := make([]byte, i%64)
		_, err = rand.Read(alpha)
		require.NoError(t, err)

		pi, err := Prove(sk, alpha)
		require.NoError(t, err)
		require.Len(t, pi, ProofLen)
		beta, err := Verify(pk, alpha, pi)
		require.NoError(t, err)
		require.Len(t, beta, OutputLen)
		b2, err := ProofToHash(pi)
		require.NoError(t, err)
		require.Equal(t, beta, b2)

		key := string(beta)
		_, dup := seen[key]
		require.False(t, dup, "outputs must be distinct across (key, message)")
		seen[key] = struct{}{}

		// same key, message extended by one byte: a different output
		beta3, err := Verify(pk, append(alpha, 0), must(Prove(sk, append(alpha, 0))))
		require.NoError(t, err)
		require.NotEqual(t, beta, beta3)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// TestProveRejectsBadKey: Prove takes the 64-byte ed25519 private key only.
func TestProveRejectsBadKey(t *testing.T) {
	_, err := Prove(make([]byte, 32), []byte("alpha"))
	require.Error(t, err)
	_, err = Prove(nil, []byte("alpha"))
	require.Error(t, err)
}

func TestVerifyRejectsTamperedProof(t *testing.T) {
	sk := ed25519.NewKeyFromSeed(mustHex(t, rfc9381TAIVectors[0].sk))
	pk := sk.Public().(ed25519.PublicKey)
	alpha := []byte("hello")
	pi, err := Prove(sk, alpha)
	require.NoError(t, err)

	// wrong message
	_, err = Verify(pk, []byte("HELLO"), pi)
	require.Error(t, err)

	// flip a byte in each region (Gamma, c, s)
	for _, idx := range []int{0, ptLen, ptLen + cLen} {
		bad := append([]byte(nil), pi...)
		bad[idx] ^= 0x01
		_, err = Verify(pk, alpha, bad)
		require.Error(t, err, "tampered byte at %d must fail", idx)
	}

	// wrong public key
	_, otherSk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = Verify(otherSk.Public().(ed25519.PublicKey), alpha, pi)
	require.Error(t, err)
}

// TestUniquenessNoGrinding is the property the whole change exists for: for a
// fixed (key, message) the VRF output is unique. Unlike a raw Ed25519 signature
// — where a signer can produce unbounded distinct valid signatures by varying
// the nonce, each hashing to a different value — Prove is deterministic and any
// accepted proof yields the same beta, so a sequencer cannot resample the bonus.
func TestUniquenessNoGrinding(t *testing.T) {
	sk := ed25519.NewKeyFromSeed(mustHex(t, rfc9381TAIVectors[1].sk))
	pk := sk.Public().(ed25519.PublicKey)
	alpha := []byte("predecessor-proof||slot")

	pi0, err := Prove(sk, alpha)
	require.NoError(t, err)
	beta0, err := Verify(pk, alpha, pi0)
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		pi, err := Prove(sk, alpha)
		require.NoError(t, err)
		require.True(t, bytes.Equal(pi, pi0), "Prove must be deterministic")
		beta, err := Verify(pk, alpha, pi)
		require.NoError(t, err)
		require.True(t, bytes.Equal(beta, beta0), "beta must be unique for (key, message)")
	}
}
