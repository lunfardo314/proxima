package vrf

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// RFC 9381 Appendix B.4 official test vectors for
// ECVRF-EDWARDS25519-SHA512-TAI. If Prove reproduces pi and Verify returns
// beta for these, the implementation is byte-exact with the standard.
var rfc9381TAIVectors = []struct {
	name, sk, pk, alpha, pi, beta string
}{
	{
		name:  "example16",
		sk:    "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
		pk:    "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a",
		alpha: "",
		pi:    "8657106690b5526245a92b003bb079ccd1a92130477671f6fc01ad16f26f723f26f8a57ccaed74ee1b190bed1f479d9727d2d0f9b005a6e456a35d4fb0daab1268a1b0db10836d9826a528ca76567805",
		beta:  "90cf1df3b703cce59e2a35b925d411164068269d7b2d29f3301c03dd757876ff66b71dda49d2de59d03450451af026798e8f81cd2e333de5cdf4f3e140fdd8ae",
	},
	{
		name:  "example17",
		sk:    "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
		pk:    "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c",
		alpha: "72",
		pi:    "f3141cd382dc42909d19ec5110469e4feae18300e94f304590abdced48aed5933bf0864a62558b3ed7f2fea45c92a465301b3bbf5e3e54ddf2d935be3b67926da3ef39226bbc355bdc9850112c8f4b02",
		beta:  "eb4440665d3891d668e7e0fcaf587f1b4bd7fbfe99d0eb2211ccec90496310eb5e33821bc613efb94db5e5b54c70a848a0bef4553a41befc57663b56373a5031",
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
		})
	}
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
