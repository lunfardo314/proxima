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
// Auditability contract: arg 0 resolves to the script binary without
// reference to the transaction — see redeemScriptArg. A reader with the
// library in hand can therefore enumerate a tx's commitments from its
// bytecode alone, which is what makes the commitment set auditable.
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

	lib := ctx.GetLibrary()
	bin := redeemScriptArg(par, lib)

	h := blake2b.Sum256(bin)
	cache := lib.CompiledScriptCache()
	if _, ok := cache.Get(h); !ok {
		// Copy: easyfl builds the decoded script's expression trees over
		// the caller's slice (only the wire form is cloned), and the
		// cache outlives both the tx and the eval-time slice pool the
		// formula path allocates in.
		s, err := lib.LocalScriptFromBytes(easyfl.LocalScriptBin(bytes.Clone(bin)))
		if err != nil {
			par.TracePanic("redeemScript: invalid script: %v", err)
		}
		cache.Put(h, s)
	}
	ctx.TxContext().AddRedeemedScript(h)

	// Truthy non-empty result so the constraint passes.
	return par.AllocData(0x01)
}

// redeemScriptArg resolves arg 0 of redeemScript to the local-script binary.
//
// Inline data is taken verbatim from the call-site expression tree. Anything
// else is a formula, evaluated in an empty context: no transaction (nil data
// context) and no parameter scope. Both omissions are load-bearing rather
// than incidental — a formula that reaches for either dies on a nil
// dereference or an out-of-range var scope, and the panic is caught by the
// validator like any other constraint failure. What survives is exactly the
// set of expressions whose value is a function of the library alone, so the
// binary a transaction commits to stays reproducible from its bytes.
//
// The point is size: a library upgrade can carry a frequently used script as
// an ordinary function, and transactions then name it instead of repeating
// several kilobytes of it inline.
//
// Note that arg 0 of callRedeemer is deliberately NOT relaxed the same way.
// Its inline-hash rule is what keeps cross-script dispatch acyclic: a script
// binary is fixed before its hash exists, so a hash literal can never name
// the script it sits in. A library function returning a hash breaks that —
// the funCode a script references is known before the library assigns the
// symbol a value, so a script calling callRedeemer(<that symbol>, ...) can be
// built as a fixed point that dispatches into itself.
func redeemScriptArg(par *easyfl.CallParams[*EvalContext], lib *Library) []byte {
	expr := par.ArgExpression(0)
	if expr.IsInlineData() {
		return expr.InlineData()
	}
	return easyfl.EvalExpressionInPool(lib.NewGlobalDataNoTrace(nil), par.Spool(), expr)
}

// evalCallRedeemer implements callRedeemer(<hash>, <fnIdx>, args...).
//
// Auditability: arg 0 (hash) must be an inline-data 32-byte literal, so a
// reader can tell from the bytecode alone which scripts a call site can
// reach.
//
// Termination: bounded by the dispatch-depth check below. easyfl has no step
// counter and its cycle checks work by symbol, so neither can see through a
// content hash; the depth counter is what makes non-termination impossible
// rather than merely unconstructible.
//
// Authority: the redeemed set on the *Transaction is the binding gate;
// the library cache is purely a decode-result cache.
func evalCallRedeemer(par *easyfl.CallParams[*EvalContext]) []byte {
	if par.Arity() < 2 {
		par.TracePanic("callRedeemer: requires at least <hash> and <fnIdx>")
	}

	// Auditability: arg 0 must be a 32-byte inline-data literal.
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

	// Termination: every frame dispatches into one of the scripts this tx
	// committed, so a chain that visits only distinct scripts can never be
	// deeper than the commitment list. A deeper one has revisited a script,
	// which means the dispatch graph has a cycle and the chain does not
	// terminate. The bound is therefore exact — it refuses cycles and
	// nothing else, and needs no tunable.
	//
	// Today a cycle is also unconstructible, because a script's hash cannot
	// appear inside its own bytes; this check is what keeps termination true
	// if arg 0 is ever allowed to name a hash indirectly (a library symbol,
	// say), which would let a fixed point be built funCode-first. Without it
	// the failure is stack exhaustion — a fatal runtime error no recover
	// intercepts, so it takes the node down rather than rejecting the tx.
	if ctx.redeemerDepth >= ctx.TxContext().NumRedeemedScripts() {
		par.TracePanic("callRedeemer: dispatch depth %d exceeds the %d script(s) redeemed by this tx (cycle)",
			ctx.redeemerDepth+1, ctx.TxContext().NumRedeemedScripts())
	}
	ctx.redeemerDepth++
	defer func() { ctx.redeemerDepth-- }()

	// Forwarded args: positions 2..N evaluated to bytes.
	n := int(par.Arity()) - 2
	args := make([][]byte, n)
	for i := 0; i < n; i++ {
		args[i] = par.Arg(byte(i + 2))
	}
	// Thread the outer eval's slice pool through into the redeemed script
	// (EvalInPool) so nested allocations land in one pool — and so the
	// result is owned by that same pool and can be returned directly
	// without a defensive copy via par.AllocData. Cuts allocations and
	// the segment-pool churn dramatically for deeply nested covenants
	// such as chess (chess() → chessGame → chessValidator).
	out, err := s.EvalInPool(par.GlobalData(), par.Spool(), idx, args...)
	if err != nil {
		par.TracePanic("callRedeemer: eval failed: %v", err)
	}
	return out
}
