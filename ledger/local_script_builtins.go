package ledger

import (
	"bytes"

	"github.com/lunfardo314/easyfl"
	"golang.org/x/crypto/blake2b"
)

// HashSize is the length of a local-script content hash (blake2b-256).
const HashSize = 32

// SymRedeemScript is the public symbol of the tx-level redeemScript constraint.
const SymRedeemScript = "redeemScript"

// SymCallRedeemer is the public symbol of the callRedeemer dispatch builtin.
const SymCallRedeemer = "callRedeemer"

// evalRedeemScript implements the redeemScript(<bin>) tx-level constraint.
//
// Soundness contract: this fn is only valid at the TxConstraints path. The
// scope check is deliberately done in Go because EasyFL has no library-private
// visibility — wrapping it in an EasyFL function would not actually prevent
// callers from invoking the embedded fn directly under another name.
//
// Auditability contract: arg 0 must be an inline-data literal. The literal
// is read from the call-site expression tree without evaluating, so a
// formula bin is rejected before any compute is spent on it.
//
// Side effects: appends blake2b(bin) to the tx-scope commitment list and
// puts the decoded *LocalScript[*EvalContext] into the library cache.
func evalRedeemScript(par *easyfl.CallParams[*EvalContext]) []byte {
	ctx := par.DataContext()

	// Soundness: redeemScript only fires from the tx-level constraints
	// list. Anywhere else (UTXO unlock, redeemed-script body, sequencer
	// constraint, ...) and the commitment list could grow during eval,
	// breaking the commit-then-invoke separation.
	if !bytes.HasPrefix(ctx.EvalPath(), PathToTxConstraints) {
		par.TracePanic("redeemScript: must be invoked at TxConstraints (path %x), not %x",
			PathToTxConstraints, ctx.EvalPath())
	}

	// Auditability: arg 0 must be an inline-data literal so the commitment
	// set is statically readable from the spending tx's bytecode.
	expr := par.ArgExpression(0)
	if !expr.IsInlineData() {
		par.TracePanic("redeemScript: arg 0 (bin) must be inline-data literal")
	}
	bin := expr.InlineData()

	h := blake2b.Sum256(bin)
	cache := ctx.GetLibrary().CompiledScriptCache()
	if _, ok := cache.Get(h); !ok {
		s, err := ctx.GetLibrary().LocalScriptFromBytes(easyfl.LocalScriptBin(bin))
		if err != nil {
			par.TracePanic("redeemScript: invalid script: %v", err)
		}
		cache.Put(h, s)
	}
	ctx.TxContext().AddRedeemedScript(h)

	// Truthy non-empty result so the constraint passes.
	return par.AllocData(0x01)
}

// evalCallRedeemer implements callRedeemer(<hash>, <fnIdx>, args...).
//
// Auditability + termination: arg 0 (hash) must be an inline-data 32-byte
// literal. The structural easyfl argument that cross-script composition is
// recursion-free (a hash literal can only exist after the callee binary is
// finalised) is what bounds runtime depth. Allowing a formula hash would
// permit a script to compute its own hash at runtime and call into itself,
// which easyfl's static cycle check cannot see through.
//
// Authority: the redeemed set on the *Transaction is the binding gate;
// the library cache is purely a decode-result cache.
func evalCallRedeemer(par *easyfl.CallParams[*EvalContext]) []byte {
	if par.Arity() < 2 {
		par.TracePanic("callRedeemer: requires at least <hash> and <fnIdx>")
	}

	// Auditability/termination: arg 0 must be a 32-byte inline-data literal.
	hashExpr := par.ArgExpression(0)
	if !hashExpr.IsInlineData() {
		par.TracePanic("callRedeemer: arg 0 (hash) must be inline-data literal")
	}
	hashBytes := hashExpr.InlineData()
	if len(hashBytes) != HashSize {
		par.TracePanic("callRedeemer: arg 0 must be %d-byte literal, got %d",
			HashSize, len(hashBytes))
	}
	var h [HashSize]byte
	copy(h[:], hashBytes)

	idxBytes := par.Arg(1)
	if len(idxBytes) != 1 {
		par.TracePanic("callRedeemer: idx must be 1 byte, got %d", len(idxBytes))
	}
	idx := int(idxBytes[0])

	ctx := par.DataContext()
	if !ctx.TxContext().IsScriptRedeemed(h) {
		par.TracePanic("callRedeemer: script %x is not redeemed", h)
	}

	s, ok := ctx.GetLibrary().CompiledScriptCache().Get(h)
	if !ok {
		// Cache invariant: any redeemed hash must be in the cache. Hitting
		// this branch means a custom CompiledScriptCache evicted while the
		// hash was still pinned by the tx — i.e. operator misconfiguration.
		par.TracePanic("callRedeemer: compiled script %x missing from cache (cache misconfigured)", h)
	}
	if idx < 0 || idx >= s.NumFunctions() {
		par.TracePanic("callRedeemer: fnIdx %d out of range (n=%d)", idx, s.NumFunctions())
	}

	// Forwarded args: positions 2..N evaluated to bytes.
	n := int(par.Arity()) - 2
	args := make([][]byte, n)
	for i := 0; i < n; i++ {
		args[i] = par.Arg(byte(i + 2))
	}
	out, err := s.Eval(par.GlobalData(), idx, args...)
	if err != nil {
		par.TracePanic("callRedeemer: eval failed: %v", err)
	}
	return par.AllocData(out...)
}
