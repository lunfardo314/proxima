// Crypto primitives exposed to EasyFL as embedded functions. These used
// to live in the easyfl base library as funCodes 73 (validSignatureED25519)
// and 74 (blake2b); they were moved to proxima on 2026-05-18 because no
// other easyfl consumer needs them — only the Proxima ledger does.
package ledger

import (
	"bytes"
	"crypto/ed25519"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util/vrf"
	"golang.org/x/crypto/blake2b"
)

// evalBlake2b implements the variadic `blake2b(...)` EasyFL embedded
// function: concatenates all arguments and returns the blake2b-256 hash
// of the concatenation (32 bytes).
func evalBlake2b(par *easyfl.CallParams[*EvalContext]) []byte {
	var buf bytes.Buffer
	for i := byte(0); i < par.Arity(); i++ {
		buf.Write(par.Arg(i))
	}
	ret := blake2b.Sum256(buf.Bytes())
	return par.AllocData(ret[:]...)
}

// evalValidSignatureED25519 implements
// `validSignatureED25519(message, signature, pubKey)`: returns a
// non-empty value (0xFF) iff ed25519.Verify(pubKey, message, signature)
// succeeds; empty (nil) otherwise.
func evalValidSignatureED25519(par *easyfl.CallParams[*EvalContext]) []byte {
	msg := par.Arg(0)
	signature := par.Arg(1)
	pubKey := par.Arg(2)

	if ed25519.Verify(pubKey, msg, signature) {
		return par.AllocData(0xff)
	}
	return nil
}

// evalVrfVerify implements the embedded `vrfVerify(publicKey, message, proof)`:
// returns 0xFF iff proof is a valid ECVRF-EDWARDS25519-SHA512-TAI proof for
// message under publicKey (RFC 9381), empty otherwise. Used by the stem
// constraint to gate the branch inflation bonus. Unlike a plain signature, a
// valid VRF proof binds a unique output to (publicKey, message), so the bonus
// (derived from that output) cannot be ground.
func evalVrfVerify(par *easyfl.CallParams[*EvalContext]) []byte {
	pubKey := par.Arg(0)
	message := par.Arg(1)
	proof := par.Arg(2)
	if len(pubKey) != ed25519.PublicKeySize {
		return nil
	}
	if _, err := vrf.Verify(ed25519.PublicKey(pubKey), message, proof); err != nil {
		return nil
	}
	return par.AllocData(0xff)
}

// evalVrfProofToHash implements the embedded `vrfProofToHash(proof)`: returns
// the ECVRF output beta (64 bytes) derived from the proof, or empty if the
// proof does not decode. The branch inflation bonus is a function of beta. This
// only decodes (cheap) and does not verify — verification happens once in the
// stem constraint (evalVrfVerify), after which beta is the unique output.
func evalVrfProofToHash(par *easyfl.CallParams[*EvalContext]) []byte {
	beta, err := vrf.ProofToHash(par.Arg(0))
	if err != nil {
		return nil
	}
	return par.AllocData(beta...)
}
