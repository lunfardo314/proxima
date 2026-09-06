// Package vrf implements ECVRF-EDWARDS25519-SHA512-TAI (RFC 9381): a
// verifiable random function over the Ed25519 group, reusing an Ed25519 key.
//
// Why a VRF and not a signature: the branch inflation bonus is derived from
// this output, so it must be a DETERMINISTIC, UNIQUE function of (public key,
// message) — a value the producer cannot resample. A plain Ed25519 signature is
// not unique (the signer picks the nonce, so unbounded valid signatures exist
// over one message), which let a sequencer grind the bonus. ECVRF's proof binds
// a single output beta to each (public key, message); that uniqueness is the
// anti-grinding property.
//
// Node-side only: Prove is used by the sequencer when it builds a branch;
// Verify is the ledger's stem constraint check. Neither is needed by the wasm
// wallet (it does not evaluate constraints), so this package — and its
// edwards25519 dependency — stays out of ledger/txbuildercore.
//
// Validated against the RFC 9381 Appendix B.4 test vectors (see vrf_test.go).
package vrf

import (
	"crypto/ed25519"
	"crypto/sha512"
	"crypto/subtle"
	"errors"

	"filippo.io/edwards25519"
)

const (
	suiteString = 0x03 // ECVRF-EDWARDS25519-SHA512-TAI (RFC 9381 §5.5)
	ptLen       = 32   // encoded point length
	cLen        = 16   // challenge length in bytes
	qLen        = 32   // encoded scalar length
	// ProofLen is the size of a proof: Gamma(32) || c(16) || s(32).
	ProofLen = ptLen + cLen + qLen // 80
	// OutputLen is the size of the VRF output beta (SHA-512).
	OutputLen = 64
)

// secretScalarAndPrefix derives the Ed25519 secret scalar x (Y = x*B is the
// public key) and the 32-byte nonce prefix from the 32-byte seed, exactly as
// RFC 8032 key expansion does.
func secretScalarAndPrefix(seed []byte) (*edwards25519.Scalar, []byte, error) {
	h := sha512.Sum512(seed)
	x, err := edwards25519.NewScalar().SetBytesWithClamping(h[:32])
	if err != nil {
		return nil, nil, err
	}
	prefix := make([]byte, 32)
	copy(prefix, h[32:])
	return x, prefix, nil
}

// Prove returns the ECVRF proof (ProofLen bytes) for alpha under sk.
func Prove(sk ed25519.PrivateKey, alpha []byte) ([]byte, error) {
	if len(sk) != ed25519.PrivateKeySize {
		return nil, errors.New("vrf.Prove: bad private key size")
	}
	seed := sk.Seed()
	pk := sk.Public().(ed25519.PublicKey)

	x, prefix, err := secretScalarAndPrefix(seed)
	if err != nil {
		return nil, err
	}
	// H = encode_to_curve(PK, alpha); Gamma = x*H
	h, err := encodeToCurveTAI(pk, alpha)
	if err != nil {
		return nil, err
	}
	hString := h.Bytes()
	gamma := new(edwards25519.Point).ScalarMult(x, h)

	// k = nonce_generation(sk, h_string); U = k*B, V = k*H
	k, err := nonceGeneration(prefix, hString)
	if err != nil {
		return nil, err
	}
	u := new(edwards25519.Point).ScalarBaseMult(k)
	v := new(edwards25519.Point).ScalarMult(k, h)

	yPoint, err := new(edwards25519.Point).SetBytes(pk)
	if err != nil {
		return nil, err
	}
	cBytes := challengeGeneration(yPoint, h, gamma, u, v) // 16 bytes
	c, err := scalarFromChallenge(cBytes)
	if err != nil {
		return nil, err
	}
	// s = k + c*x mod q
	s := edwards25519.NewScalar().MultiplyAdd(c, x, k)

	pi := make([]byte, 0, ProofLen)
	pi = append(pi, gamma.Bytes()...)
	pi = append(pi, cBytes...)
	pi = append(pi, s.Bytes()...)
	return pi, nil
}

// Verify checks pi against pk and alpha. On success it returns the VRF output
// beta (OutputLen bytes). On any failure it returns a non-nil error.
func Verify(pk ed25519.PublicKey, alpha, pi []byte) ([]byte, error) {
	if len(pk) != ed25519.PublicKeySize {
		return nil, errors.New("vrf.Verify: bad public key size")
	}
	yPoint, err := new(edwards25519.Point).SetBytes(pk)
	if err != nil {
		return nil, errors.New("vrf.Verify: invalid public key point")
	}
	gamma, cBytes, s, err := decodeProof(pi)
	if err != nil {
		return nil, err
	}
	c, err := scalarFromChallenge(cBytes)
	if err != nil {
		return nil, err
	}
	h, err := encodeToCurveTAI(pk, alpha)
	if err != nil {
		return nil, err
	}
	// U = s*B - c*Y
	cY := new(edwards25519.Point).ScalarMult(c, yPoint)
	sB := new(edwards25519.Point).ScalarBaseMult(s)
	u := new(edwards25519.Point).Subtract(sB, cY)
	// V = s*H - c*Gamma
	sH := new(edwards25519.Point).ScalarMult(s, h)
	cGamma := new(edwards25519.Point).ScalarMult(c, gamma)
	v := new(edwards25519.Point).Subtract(sH, cGamma)

	cPrime := challengeGeneration(yPoint, h, gamma, u, v)
	if subtle.ConstantTimeCompare(cPrime, cBytes) != 1 {
		return nil, errors.New("vrf.Verify: challenge mismatch")
	}
	return proofToHash(gamma), nil
}

// ProofToHash derives beta from a proof WITHOUT verifying it. Callers that need
// the guarantee that beta is the unique output for (pk, alpha) must Verify.
func ProofToHash(pi []byte) ([]byte, error) {
	gamma, _, _, err := decodeProof(pi)
	if err != nil {
		return nil, err
	}
	return proofToHash(gamma), nil
}

// encodeToCurveTAI is ECVRF_encode_to_curve_try_and_increment (RFC 9381
// §5.4.1.1): hash (suite || 0x01 || PK || alpha || ctr || 0x00), interpret the
// 32-byte digest as a point, clear the cofactor; retry with the next ctr until
// a valid, non-identity point is found.
func encodeToCurveTAI(salt, alpha []byte) (*edwards25519.Point, error) {
	identity := edwards25519.NewIdentityPoint()
	for ctr := 0; ctr < 256; ctr++ {
		hb := make([]byte, 0, 3+len(salt)+len(alpha))
		hb = append(hb, suiteString, 0x01)
		hb = append(hb, salt...)
		hb = append(hb, alpha...)
		hb = append(hb, byte(ctr), 0x00)
		digest := sha512.Sum512(hb)
		p, err := new(edwards25519.Point).SetBytes(digest[:ptLen])
		if err != nil {
			continue // not a valid point encoding, try next ctr
		}
		p.MultByCofactor(p)
		if p.Equal(identity) == 1 {
			continue
		}
		return p, nil
	}
	return nil, errors.New("vrf: encode_to_curve failed (no valid point in 256 tries)")
}

// nonceGeneration is ECVRF_nonce_generation_RFC8032 (RFC 9381 §5.4.2.2):
// k = SHA512(prefix || h_string) reduced mod q.
func nonceGeneration(prefix, hString []byte) (*edwards25519.Scalar, error) {
	buf := make([]byte, 0, len(prefix)+len(hString))
	buf = append(buf, prefix...)
	buf = append(buf, hString...)
	digest := sha512.Sum512(buf)
	return edwards25519.NewScalar().SetUniformBytes(digest[:])
}

// challengeGeneration is ECVRF_challenge_generation (RFC 9381 §5.4.3):
// c = first cLen bytes of SHA512(suite || 0x02 || P1..P5 || 0x00).
func challengeGeneration(p1, p2, p3, p4, p5 *edwards25519.Point) []byte {
	buf := make([]byte, 0, 2+5*ptLen+1)
	buf = append(buf, suiteString, 0x02)
	buf = append(buf, p1.Bytes()...)
	buf = append(buf, p2.Bytes()...)
	buf = append(buf, p3.Bytes()...)
	buf = append(buf, p4.Bytes()...)
	buf = append(buf, p5.Bytes()...)
	buf = append(buf, 0x00)
	digest := sha512.Sum512(buf)
	out := make([]byte, cLen)
	copy(out, digest[:cLen])
	return out
}

// proofToHash is ECVRF_proof_to_hash (RFC 9381 §5.2):
// beta = SHA512(suite || 0x03 || point_to_string(cofactor*Gamma) || 0x00).
func proofToHash(gamma *edwards25519.Point) []byte {
	cg := new(edwards25519.Point).MultByCofactor(gamma)
	buf := make([]byte, 0, 2+ptLen+1)
	buf = append(buf, suiteString, 0x03)
	buf = append(buf, cg.Bytes()...)
	buf = append(buf, 0x00)
	digest := sha512.Sum512(buf)
	out := make([]byte, OutputLen)
	copy(out, digest[:])
	return out
}

// scalarFromChallenge turns the little-endian cLen-byte challenge into a scalar
// (zero-padded to 32 bytes; value < 2^128 < q, so always canonical).
func scalarFromChallenge(cBytes []byte) (*edwards25519.Scalar, error) {
	b := make([]byte, 32)
	copy(b, cBytes)
	return edwards25519.NewScalar().SetCanonicalBytes(b)
}

// decodeProof is ECVRF_decode_proof: split pi into (Gamma, c, s), rejecting a
// non-canonical Gamma or s (a non-canonical s would admit proof malleability).
func decodeProof(pi []byte) (*edwards25519.Point, []byte, *edwards25519.Scalar, error) {
	if len(pi) != ProofLen {
		return nil, nil, nil, errors.New("vrf: bad proof length")
	}
	gamma, err := new(edwards25519.Point).SetBytes(pi[:ptLen])
	if err != nil {
		return nil, nil, nil, errors.New("vrf: invalid Gamma point")
	}
	cBytes := make([]byte, cLen)
	copy(cBytes, pi[ptLen:ptLen+cLen])
	s, err := edwards25519.NewScalar().SetCanonicalBytes(pi[ptLen+cLen:])
	if err != nil {
		return nil, nil, nil, errors.New("vrf: non-canonical s scalar")
	}
	return gamma, cBytes, s, nil
}
