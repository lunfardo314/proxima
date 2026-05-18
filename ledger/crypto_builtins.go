// Crypto primitives exposed to EasyFL as embedded functions. These used
// to live in the easyfl base library as funCodes 73 (validSignatureED25519)
// and 74 (blake2b); they were moved to proxima on 2026-05-18 because no
// other easyfl consumer needs them — only the Proxima ledger does.
package ledger

import (
	"bytes"
	"crypto/ed25519"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
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
	par.Trace("blake2b: %d params -> %s", par.Arity(), easyfl_util.FmtLazy(ret[:]))
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
		par.Trace("validSignatureED25519: msg=%s, sig=%s, pubKey=%s -> true",
			easyfl_util.FmtLazy(msg), easyfl_util.FmtLazy(signature), easyfl_util.FmtLazy(pubKey))
		return par.AllocData(0xff)
	}
	par.Trace("validSignatureED25519: msg=%s, sig=%s, pubKey=%s -> false",
		easyfl_util.FmtLazy(msg), easyfl_util.FmtLazy(signature), easyfl_util.FmtLazy(pubKey))
	return nil
}
