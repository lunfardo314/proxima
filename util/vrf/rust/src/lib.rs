//! ECVRF-EDWARDS25519-SHA512-TAI (RFC 9381) over the Ed25519 group, reusing an
//! Ed25519 key. The Rust twin of Proxima's Go package `util/vrf`: same
//! functions, same byte formats, same acceptance rules, so a proof made on one
//! side verifies on the other and the outputs agree byte for byte. The Go
//! package's test suite checks that equivalence against this crate's
//! `vrf_tool` binary.
//!
//! The ledger needs a VRF rather than a signature because the output must be a
//! deterministic, unique function of (public key, message): a signer can
//! produce unboundedly many valid signatures over one message, but only one
//! VRF output verifies.

use curve25519_dalek::edwards::{CompressedEdwardsY, EdwardsPoint};
use curve25519_dalek::scalar::Scalar;
use curve25519_dalek::traits::Identity;
use sha2::{Digest, Sha512};
use subtle::ConstantTimeEq;

const SUITE: u8 = 0x03;
const PT_LEN: usize = 32;
const C_LEN: usize = 16;
const Q_LEN: usize = 32;
/// Size of a proof: Gamma(32) || c(16) || s(32).
pub const PROOF_LEN: usize = PT_LEN + C_LEN + Q_LEN;
/// Size of the VRF output beta (SHA-512).
pub const OUTPUT_LEN: usize = 64;
/// Size of an Ed25519 private key as Go's crypto/ed25519 holds it: seed || public key.
pub const PRIVATE_KEY_LEN: usize = 64;
pub const PUBLIC_KEY_LEN: usize = 32;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Error {
    BadPrivateKeySize,
    BadPublicKeySize,
    InvalidPublicKeyPoint,
    SmallOrderPublicKey,
    BadProofLength,
    InvalidGamma,
    NonCanonicalS,
    ChallengeMismatch,
    EncodeToCurveFailed,
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Error::BadPrivateKeySize => "bad private key size",
            Error::BadPublicKeySize => "bad public key size",
            Error::InvalidPublicKeyPoint => "invalid public key point",
            Error::SmallOrderPublicKey => "small-order public key",
            Error::BadProofLength => "bad proof length",
            Error::InvalidGamma => "invalid Gamma point",
            Error::NonCanonicalS => "non-canonical s scalar",
            Error::ChallengeMismatch => "challenge mismatch",
            Error::EncodeToCurveFailed => "encode_to_curve failed",
        })
    }
}

impl std::error::Error for Error {}

/// An expanded private key: the secret scalar x, the nonce prefix and the
/// public key, derived once. It splits ECVRF_prove in two so that a caller
/// computing many outputs (a miner) does only the work that determines the
/// output per message, and completes a proof only for the message it needs.
pub struct Prover {
    x: Scalar,
    prefix: [u8; 32],
    pk: [u8; PUBLIC_KEY_LEN],
    y: EdwardsPoint,
}

/// What `Prover::output` computed for one message and `proof_for` completes:
/// H = encode_to_curve(PK, alpha) and Gamma = x*H.
pub struct ProofState {
    h: EdwardsPoint,
    gamma: EdwardsPoint,
}

impl Prover {
    /// Expands the 64-byte Ed25519 private key (seed || public key).
    pub fn new(private_key: &[u8]) -> Result<Prover, Error> {
        if private_key.len() != PRIVATE_KEY_LEN {
            return Err(Error::BadPrivateKeySize);
        }
        let mut seed = [0u8; 32];
        seed.copy_from_slice(&private_key[..32]);
        Ok(Prover::from_seed(&seed))
    }

    /// Expands a 32-byte seed exactly as RFC 8032 key expansion does: the
    /// clamped lower half of SHA-512(seed) is the scalar, the upper half the
    /// nonce prefix.
    pub fn from_seed(seed: &[u8; 32]) -> Prover {
        let h = Sha512::digest(seed);
        let mut lo = [0u8; 32];
        lo.copy_from_slice(&h[..32]);
        lo[0] &= 248;
        lo[31] &= 127;
        lo[31] |= 64;
        let x = Scalar::from_bytes_mod_order(lo);
        let mut prefix = [0u8; 32];
        prefix.copy_from_slice(&h[32..]);
        let y = EdwardsPoint::mul_base(&x);
        Prover { x, prefix, pk: y.compress().to_bytes(), y }
    }

    pub fn public_key(&self) -> &[u8; PUBLIC_KEY_LEN] {
        &self.pk
    }

    /// ECVRF_prove up to Gamma followed by ECVRF_proof_to_hash: beta, the
    /// unique output for (PK, alpha), and the state `proof_for` needs.
    pub fn output(&self, alpha: &[u8]) -> ([u8; OUTPUT_LEN], ProofState) {
        let h = encode_to_curve_tai(&self.pk, alpha);
        let gamma = &self.x * &h;
        (proof_to_hash_point(&gamma), ProofState { h, gamma })
    }

    /// The rest of ECVRF_prove: the nonce k from the secret prefix and H,
    /// U = k*B, V = k*H, the challenge c and s = k + c*x.
    pub fn proof_for(&self, st: &ProofState) -> [u8; PROOF_LEN] {
        let k = nonce_generation(&self.prefix, &st.h.compress().to_bytes());
        let u = EdwardsPoint::mul_base(&k);
        let v = &k * &st.h;
        let c_bytes = challenge_generation(&self.y, &st.h, &st.gamma, &u, &v);
        let c = scalar_from_challenge(&c_bytes);
        let s = c * self.x + k;

        let mut pi = [0u8; PROOF_LEN];
        pi[..PT_LEN].copy_from_slice(st.gamma.compress().as_bytes());
        pi[PT_LEN..PT_LEN + C_LEN].copy_from_slice(&c_bytes);
        pi[PT_LEN + C_LEN..].copy_from_slice(&s.to_bytes());
        pi
    }

    /// The whole ECVRF_prove for one message.
    pub fn prove(&self, alpha: &[u8]) -> [u8; PROOF_LEN] {
        let (_, st) = self.output(alpha);
        self.proof_for(&st)
    }
}

/// The ECVRF proof for alpha under the 64-byte private key.
pub fn prove(private_key: &[u8], alpha: &[u8]) -> Result<[u8; PROOF_LEN], Error> {
    Ok(Prover::new(private_key)?.prove(alpha))
}

/// Checks pi against pk and alpha and returns beta. Rejects a small-order
/// public key (ECVRF_validate_key): such a key has no secret scalar, yet a
/// forged proof under it would verify with a constant output.
pub fn verify(pk: &[u8], alpha: &[u8], pi: &[u8]) -> Result<[u8; OUTPUT_LEN], Error> {
    if pk.len() != PUBLIC_KEY_LEN {
        return Err(Error::BadPublicKeySize);
    }
    let mut pk_bytes = [0u8; PUBLIC_KEY_LEN];
    pk_bytes.copy_from_slice(pk);
    let y = CompressedEdwardsY(pk_bytes).decompress().ok_or(Error::InvalidPublicKeyPoint)?;
    if y.mul_by_cofactor() == EdwardsPoint::identity() {
        return Err(Error::SmallOrderPublicKey);
    }
    let (gamma, c_bytes, s) = decode_proof(pi)?;
    let c = scalar_from_challenge(&c_bytes);
    let h = encode_to_curve_tai(&pk_bytes, alpha);
    // U = s*B - c*Y, V = s*H - c*Gamma
    let u = EdwardsPoint::mul_base(&s) - c * y;
    let v = s * h - c * gamma;
    let c_prime = challenge_generation(&y, &h, &gamma, &u, &v);
    if !bool::from(c_prime.ct_eq(&c_bytes)) {
        return Err(Error::ChallengeMismatch);
    }
    Ok(proof_to_hash_point(&gamma))
}

/// beta from a proof WITHOUT verifying it. Callers that need the guarantee that
/// beta is the unique output for (pk, alpha) must `verify`.
pub fn proof_to_hash(pi: &[u8]) -> Result<[u8; OUTPUT_LEN], Error> {
    let (gamma, _, _) = decode_proof(pi)?;
    Ok(proof_to_hash_point(&gamma))
}

/// ECVRF_encode_to_curve_try_and_increment: hash (suite || 0x01 || PK || alpha
/// || ctr || 0x00), read the first 32 bytes as a point, clear the cofactor;
/// retry with the next ctr until a valid, non-identity point is found.
fn encode_to_curve_tai(salt: &[u8; 32], alpha: &[u8]) -> EdwardsPoint {
    for ctr in 0u8..=255 {
        let mut hasher = Sha512::new();
        hasher.update([SUITE, 0x01]);
        hasher.update(salt);
        hasher.update(alpha);
        hasher.update([ctr, 0x00]);
        let digest = hasher.finalize();
        let mut b = [0u8; PT_LEN];
        b.copy_from_slice(&digest[..PT_LEN]);
        if let Some(p) = CompressedEdwardsY(b).decompress() {
            let p = p.mul_by_cofactor();
            if p != EdwardsPoint::identity() {
                return p;
            }
        }
    }
    // 256 consecutive failures have probability 2^-256; the Go side errors
    // here and so would any caller, but there is nothing sensible to return
    panic!("{}", Error::EncodeToCurveFailed)
}

/// ECVRF_nonce_generation_RFC8032: k = SHA-512(prefix || h_string) mod q.
fn nonce_generation(prefix: &[u8; 32], h_string: &[u8; PT_LEN]) -> Scalar {
    let mut hasher = Sha512::new();
    hasher.update(prefix);
    hasher.update(h_string);
    let digest: [u8; 64] = hasher.finalize().into();
    Scalar::from_bytes_mod_order_wide(&digest)
}

/// ECVRF_challenge_generation: the first 16 bytes of
/// SHA-512(suite || 0x02 || P1..P5 || 0x00).
fn challenge_generation(p1: &EdwardsPoint, p2: &EdwardsPoint, p3: &EdwardsPoint, p4: &EdwardsPoint, p5: &EdwardsPoint) -> [u8; C_LEN] {
    let mut hasher = Sha512::new();
    hasher.update([SUITE, 0x02]);
    for p in [p1, p2, p3, p4, p5] {
        hasher.update(p.compress().as_bytes());
    }
    hasher.update([0x00u8]);
    let digest = hasher.finalize();
    let mut c = [0u8; C_LEN];
    c.copy_from_slice(&digest[..C_LEN]);
    c
}

/// ECVRF_proof_to_hash: SHA-512(suite || 0x03 || point_to_string(cofactor*Gamma) || 0x00).
fn proof_to_hash_point(gamma: &EdwardsPoint) -> [u8; OUTPUT_LEN] {
    let cg = gamma.mul_by_cofactor().compress();
    let mut hasher = Sha512::new();
    hasher.update([SUITE, 0x03]);
    hasher.update(cg.as_bytes());
    hasher.update([0x00u8]);
    hasher.finalize().into()
}

/// The little-endian 16-byte challenge as a scalar: below 2^128 < q, always canonical.
fn scalar_from_challenge(c: &[u8; C_LEN]) -> Scalar {
    let mut b = [0u8; 32];
    b[..C_LEN].copy_from_slice(c);
    Scalar::from_bytes_mod_order(b)
}

/// ECVRF_decode_proof: (Gamma, c, s), rejecting a non-canonical Gamma or s
/// (a non-canonical s would admit proof malleability).
fn decode_proof(pi: &[u8]) -> Result<(EdwardsPoint, [u8; C_LEN], Scalar), Error> {
    if pi.len() != PROOF_LEN {
        return Err(Error::BadProofLength);
    }
    let mut g = [0u8; PT_LEN];
    g.copy_from_slice(&pi[..PT_LEN]);
    let gamma = CompressedEdwardsY(g).decompress().ok_or(Error::InvalidGamma)?;
    let mut c = [0u8; C_LEN];
    c.copy_from_slice(&pi[PT_LEN..PT_LEN + C_LEN]);
    let mut sb = [0u8; Q_LEN];
    sb.copy_from_slice(&pi[PT_LEN + C_LEN..]);
    let s = Option::<Scalar>::from(Scalar::from_canonical_bytes(sb)).ok_or(Error::NonCanonicalS)?;
    Ok((gamma, c, s))
}

#[cfg(test)]
mod tests {
    use super::*;

    // RFC 9381 Appendix B.4, the TAI suite: (seed, public key, alpha, pi, beta).
    // Matching pi byte for byte pins hash-to-curve, nonce generation, the
    // challenge and the scalar arithmetic; beta pins proof_to_hash.
    const VECTORS: [(&str, &str, &str, &str, &str); 3] = [
        (
            "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
            "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a",
            "",
            "8657106690b5526245a92b003bb079ccd1a92130477671f6fc01ad16f26f723f26f8a57ccaed74ee1b190bed1f479d9727d2d0f9b005a6e456a35d4fb0daab1268a1b0db10836d9826a528ca76567805",
            "90cf1df3b703cce59e2a35b925d411164068269d7b2d29f3301c03dd757876ff66b71dda49d2de59d03450451af026798e8f81cd2e333de5cdf4f3e140fdd8ae",
        ),
        (
            "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
            "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c",
            "72",
            "f3141cd382dc42909d19ec5110469e4feae18300e94f304590abdced48aed5933bf0864a62558b3ed7f2fea45c92a465301b3bbf5e3e54ddf2d935be3b67926da3ef39226bbc355bdc9850112c8f4b02",
            "eb4440665d3891d668e7e0fcaf587f1b4bd7fbfe99d0eb2211ccec90496310eb5e33821bc613efb94db5e5b54c70a848a0bef4553a41befc57663b56373a5031",
        ),
        (
            "c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7",
            "fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025",
            "af82",
            "9bc0f79119cc5604bf02d23b4caede71393cedfbb191434dd016d30177ccbf8096bb474e53895c362d8628ee9f9ea3c0e52c7a5c691b6c18c9979866568add7a2d41b00b05081ed0f58ee5e31b3a970e",
            "645427e5d00c62a23fb703732fa5d892940935942101e456ecca7bb217c61c452118fec1219202a0edcf038bb6373241578be7217ba85a2687f7a0310b2df19f",
        ),
    ];

    fn seed(hex_seed: &str) -> [u8; 32] {
        let mut s = [0u8; 32];
        s.copy_from_slice(&hex::decode(hex_seed).unwrap());
        s
    }

    #[test]
    fn rfc9381_vectors() {
        for (sk, pk, alpha, pi, beta) in VECTORS {
            let p = Prover::from_seed(&seed(sk));
            assert_eq!(hex::encode(p.public_key()), pk);
            let alpha = hex::decode(alpha).unwrap();
            let (out, st) = p.output(&alpha);
            assert_eq!(hex::encode(out), beta, "beta");
            let proof = p.proof_for(&st);
            assert_eq!(hex::encode(proof), pi, "pi");
            assert_eq!(hex::encode(verify(p.public_key(), &alpha, &proof).unwrap()), beta, "verify");
            assert_eq!(hex::encode(proof_to_hash(&proof).unwrap()), beta, "proof_to_hash");
        }
    }

    #[test]
    fn rejects_tampering() {
        let p = Prover::from_seed(&seed(VECTORS[0].0));
        let alpha = b"hello";
        let pi = p.prove(alpha);
        assert_eq!(verify(p.public_key(), b"HELLO", &pi), Err(Error::ChallengeMismatch));
        for idx in [0, PT_LEN, PT_LEN + C_LEN] {
            let mut bad = pi;
            bad[idx] ^= 1;
            assert!(verify(p.public_key(), alpha, &bad).is_err(), "byte {idx}");
        }
        assert_eq!(verify(p.public_key(), alpha, &pi[..PROOF_LEN - 1]), Err(Error::BadProofLength));
        assert_eq!(prove(&[0u8; 32], alpha), Err(Error::BadPrivateKeySize));
    }

    #[test]
    fn rejects_small_order_key() {
        let identity = EdwardsPoint::identity().compress().to_bytes();
        let p = Prover::from_seed(&seed(VECTORS[1].0));
        let pi = p.prove(b"x");
        assert_eq!(verify(&identity, b"x", &pi), Err(Error::SmallOrderPublicKey));
    }
}
