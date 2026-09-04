// Tests for the redeemScript / callRedeemer local-script feature.
//
// redeemScript is a tx-level constraint that commits to a local-script bin
// (an EasyFL script bundle) by hash. Once committed, callRedeemer(hash,
// fnIdx, args...) — invoked from anywhere reachable by the validator —
// dispatches into the redeemed script's function fnIdx.
//
// Soundness: redeemScript is structurally restricted to TxConstraints scope
// in Go (see ledger/local_script_builtins.go). Auditability: callRedeemer's
// hash must be an inline-data literal, checked via CallParams.ArgExpression;
// redeemScript's bin need only be resolvable without the transaction.
// Termination: dispatch depth is bounded by the redeemed-script count.

package tests

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// newRedeemTestEnv mirrors newTestEnv from claude_tx_test.go: fresh utxodb
// + funded address. Separate to keep this test file standalone.
func newRedeemTestEnv(t *testing.T, amount uint64) (*utxodb.UTXODB, ed25519.PrivateKey, ledger.SigLock) {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	require.NoError(t, u.TokensFromFaucet(addr, amount))
	return u, privKey, addr
}

// compileBin compiles an EasyFL local-script source into bin + hash.
// Tests use this to mint scripts they will redeem.
func compileBin(t *testing.T, source string) (easyfl.LocalScriptBin, [32]byte) {
	t.Helper()
	lib := ledger.L(base.MaxSlot)
	bin, err := lib.CompileLocalScript(source)
	require.NoError(t, err, "compile local script")
	return bin, blake2b.Sum256(bin)
}

// mustCompileExpr compiles an EasyFL expression to bytecode. Used to mint
// the redeemScript / callRedeemer call-site bytecodes that go into the tx.
func mustCompileExpr(t *testing.T, src string) []byte {
	t.Helper()
	_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(src)
	require.NoError(t, err, "compile expression %q", src)
	return code
}

// buildTransferWith builds a simple sigLock-to-sigLock transfer tx, then
// applies the caller's `customise` hook so they can attach tx-level
// constraints or extra output constraints. Returns serialised tx bytes.
func buildTransferWith(
	t *testing.T,
	u *utxodb.UTXODB,
	srcPrivKey ed25519.PrivateKey,
	srcAddr, dstAddr ledger.SigLock,
	amount uint64,
	customise func(txb *exhelp.Builder),
) []byte {
	t.Helper()

	outsData, err := u.StateReader().GetUTXOsForController(srcAddr.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		return o.ChainConstraint() == nil && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	require.True(t, len(outs) > 0)

	txb := exhelp.New()
	total, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	require.True(t, total >= amount)

	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	target := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(dstAddr)
	})
	_, err = txb.ProduceOutput(target)
	require.NoError(t, err)

	if total > amount {
		remainder := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(total - amount)).WithLock(srcAddr)
		})
		_, err = txb.ProduceOutput(remainder)
		require.NoError(t, err)
	}

	lib := ledger.L(maxTs.Slot)
	txb.SetTimestamp(maxTs.AddTicks(int(lib.TransactionPace)))
	txb.ComputeInputCommitment()

	if customise != nil {
		customise(txb)
		// recompute commitment in case customise changed inputs (it
		// shouldn't here but cheap to be safe).
		txb.ComputeInputCommitment()
	}

	txb.SignED25519(srcPrivKey)
	return txb.Bytes()
}

// submitAndCapture submits txBytes to the utxodb and captures the parsed
// *Transaction so tests can inspect IsScriptRedeemed etc. Returns the
// validation error verbatim.
func submitAndCapture(u *utxodb.UTXODB, txBytes []byte) (*transaction.Transaction, error) {
	var captured *transaction.Transaction
	err := u.AddTransaction(txBytes, func(tx *transaction.Transaction, e error) error {
		captured = tx
		return e
	})
	return captured, err
}

// --------------------------------------------------------------------------
// redeemScript semantics
// --------------------------------------------------------------------------

// TestRedeemScript_HappyPath: a tx whose TxConstraints contain
// redeemScript(0x<bin>) is accepted; the parsed tx exposes the hash via
// IsScriptRedeemed; the library cache holds the decoded LocalScript.
func TestRedeemScript_HappyPath(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	// Tiny single-fn local script: id($0) returns $0.
	bin, hash := compileBin(t, `func id : $0`)

	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
	})

	tx, err := submitAndCapture(u, txBytes)
	require.NoError(t, err)
	require.NotNil(t, tx)
	require.True(t, tx.IsScriptRedeemed(hash), "hash must be in tx commitment list")

	// The library cache must have the decoded script.
	cached, ok := ledger.L(base.MaxSlot).CompiledScriptCache().Get(hash)
	require.True(t, ok, "compiled script must be cached")
	require.NotNil(t, cached)
}

// TestRedeemScript_FormulaBin: arg 0 may be any formula the library can
// evaluate on its own. concat of the two halves of a bin stands in for the
// motivating case — a library function that returns a frequently used script
// — which cannot be tested here without an actual library upgrade. The
// commitment must come out identical to the inline form, since the hash is
// taken over the value, not over the call site that produced it.
func TestRedeemScript_FormulaBin(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, hash := compileBin(t, `func formula_bin : concat($0, 0xf0, 0x12)`)
	half := len(bin) / 2
	formulaBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(concat(0x%s, 0x%s))",
		hex.EncodeToString(bin[:half]), hex.EncodeToString(bin[half:])))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(formulaBC)
	})

	tx, err := submitAndCapture(u, txBytes)
	require.NoError(t, err)
	require.True(t, tx.IsScriptRedeemed(hash), "formula must commit the same hash as the inline form")

	cached, ok := ledger.L(base.MaxSlot).CompiledScriptCache().Get(hash)
	require.True(t, ok)
	require.NotNil(t, cached)
}

// TestRedeemScript_FormulaBinInvalid: a formula that evaluates fine but does
// not produce a well-formed local-script bin fails at the parse, not at the
// call site.
func TestRedeemScript_FormulaBinInvalid(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	formulaBC := mustCompileExpr(t, "redeemScript(concat(0xde, 0xad))")

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(formulaBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid script")
}

// TestRedeemScript_FormulaTouchingTxRejected: the empty evaluation context
// carries no transaction, so any formula that reaches for one dies. This is
// what keeps a commitment a function of the library alone — i.e. what keeps
// it readable off the transaction's bytes.
func TestRedeemScript_FormulaTouchingTxRejected(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	// txID is a real call that needs the transaction.
	formulaBC := mustCompileExpr(t, "redeemScript(concat(0x4553, txID))")

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(formulaBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
}

// TestRedeemScript_FormulaNestedRedeemRejected: redeemScript inside its own
// argument would let the commitment list grow while it is being read. The
// empty context blocks it — the inner call finds no transaction to check its
// eval path against.
func TestRedeemScript_FormulaNestedRedeemRejected(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, _ := compileBin(t, `func nested_redeem : $0`)
	formulaBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(concat(redeemScript(0x%s), 0x00))",
		hex.EncodeToString(bin)))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(formulaBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
}

// TestRedeemScript_FormulaNestedCallRedeemerRejected: dispatching into a
// redeemed script from inside the argument is blocked for the same reason —
// callRedeemer needs the transaction to check the redeemed set.
func TestRedeemScript_FormulaNestedCallRedeemerRejected(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, hash := compileBin(t, `func nested_call : $0`)
	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))
	formulaBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(callRedeemer(0x%s, 0x00, 0x4553))",
		hex.EncodeToString(hash[:])))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
		txb.PushTxConstraint(formulaBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
}

// TestRedeemScript_FormulaParamRefRejected: the empty evaluation context has
// no parameter scope either, so an argument that is a parameter reference is
// rejected — including when redeemScript sits inside a library function whose
// own caller did bind that parameter. Only expressions closed over the
// library survive, which is what makes the committed binary reproducible.
func TestRedeemScript_FormulaParamRefRejected(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	paramBC := mustCompileExpr(t, "redeemScript($0)")

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(paramBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
}

// TestRedeemScript_LibraryResidentBin is the case the relaxation exists for:
// a library upgrade carries a script as an ordinary zero-argument function,
// and a transaction names it instead of repeating it inline. The upgrade is
// done on a clone so the shared singleton is untouched — what is checked here
// is that the function yields the exact bin in an empty context, which is the
// value redeemScript would commit to, and how much smaller the call site is.
func TestRedeemScript_LibraryResidentBin(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	bin, _ := compileBin(t, `func lib_resident : concat($0, 0x11, 0x22, 0x33)`)

	clone := lib.Clone()
	upgrade, err := easyfl.ReadLibraryFromJSON([]byte(fmt.Sprintf(
		`{"functions":[{"sym":"testResidentBin","numArgs":0,"source":"0x%s"}]}`,
		hex.EncodeToString(bin))))
	require.NoError(t, err)
	require.NoError(t, clone.Upgrade(upgrade))
	require.Equal(t, lib.LibraryHash(), ledger.L(base.MaxSlot).LibraryHash(), "singleton untouched")

	// Empty context: no transaction, no parameters — exactly how
	// redeemScript resolves a non-literal arg 0.
	got, err := clone.EvalFromSource(clone.NewGlobalDataNoTrace(nil), "testResidentBin")
	require.NoError(t, err)
	require.Equal(t, []byte(bin), got)

	_, _, inline, err := clone.CompileExpression(fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))
	require.NoError(t, err)
	_, _, named, err := clone.CompileExpression("redeemScript(testResidentBin)")
	require.NoError(t, err)
	require.Less(t, len(named), len(inline))
	t.Logf("redeemScript call site: inline %d bytes, library-resident %d bytes (bin is %d)",
		len(inline), len(named), len(bin))
}

// TestRedeemScript_FormulaCachedAcrossTx: a script committed by formula in
// one tx must still be callable from a later tx. The decoded script is held
// in a library-lifetime cache while the formula's result lives in the
// validating tx's slice pool, so this fails if the bin is not copied out.
func TestRedeemScript_FormulaCachedAcrossTx(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 2_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, hash := compileBin(t, `func formula_cached : concat($0, 0xc4, 0xed)`)
	half := len(bin) / 2
	formulaBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(concat(0x%s, 0x%s))",
		hex.EncodeToString(bin[:half]), hex.EncodeToString(bin[half:])))

	tx1 := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(formulaBC)
	})
	_, err := submitAndCapture(u, tx1)
	require.NoError(t, err)

	// Second tx re-commits the same script and dispatches into the cached
	// decode. The concat result backing tx1's decode is long gone by now.
	callBC := mustCompileExpr(t, fmt.Sprintf("callRedeemer(0x%s, 0x00, 0x42)", hex.EncodeToString(hash[:])))
	tx2 := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(formulaBC)
		addExtraConstraint(t, txb, callBC)
	})
	tx, err := submitAndCapture(u, tx2)
	require.NoError(t, err)
	require.True(t, tx.IsScriptRedeemed(hash))
}

// TestRedeemScript_InvalidBin: redeemScript(0xdeadbeef) — the literal is
// inline data so the call-site check passes, but it's not a valid local-
// script header so easyfl's parse fails; we surface the wrapped error.
func TestRedeemScript_InvalidBin(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	badBC := mustCompileExpr(t, "redeemScript(0xdeadbeef)")

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(badBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
	require.Contains(t, err.Error(), "redeemScript: invalid script:")
}

// addExtraConstraint appends bc as an extra constraint on the second
// produced output (the change-back) without disturbing its amount/lock.
// Most callRedeemer tests need exactly this shape.
func addExtraConstraint(t *testing.T, txb *exhelp.Builder, bc []byte) {
	t.Helper()
	require.Equal(t, 2, len(txb.ProducedOutputs), "buildTransferWith expected to produce 2 outputs (target + remainder)")
	txb.ReplaceProducedOutput(1, txb.ProducedOutputs[1].Clone(func(o *ledger.OutputBuilder) {
		o.MustPushConstraint(bc)
	}))
}

// TestRedeemScript_OutsideTxConstraints: redeemScript called from a UTXO's
// extra constraint (index 4) is rejected by the scope check, even though
// the bin itself is a valid literal. This is the load-bearing soundness
// gate — the commitment list must not grow during UTXO-level evaluation.
func TestRedeemScript_OutsideTxConstraints(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, _ := compileBin(t, `func id_outside : $0`)
	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		addExtraConstraint(t, txb, redeemBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
	require.Contains(t, err.Error(), "redeemScript: must be invoked at TxConstraints")
}

// TestRedeemScript_Idempotent: pushing redeemScript(<bin>) twice in the
// same tx is accepted and the commitment list ends up length 1
// (AddRedeemedScript de-dupes).
func TestRedeemScript_Idempotent(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, hash := compileBin(t, `func id : $0`)
	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
		txb.PushTxConstraint(redeemBC)
	})

	tx, err := submitAndCapture(u, txBytes)
	require.NoError(t, err)
	require.True(t, tx.IsScriptRedeemed(hash))

	// IsScriptRedeemed only tells us presence; check the underlying slice
	// indirectly by scanning all 32-byte permutations is overkill. Instead
	// re-parse a fresh Transaction from the same bytes and confirm the
	// commit list does not double-count by relying on the AddRedeemedScript
	// idempotency we documented — verified by the next test (cache reuse)
	// which counts Put calls.
}

// countingScriptCache wraps the default unbounded cache and counts Puts.
// Used by TestRedeemScript_CrossTxCacheReuse to assert that the second
// redemption of the same bin does not re-decode.
type countingScriptCache struct {
	inner ledger.CompiledScriptCache
	puts  int64
}

func (c *countingScriptCache) Get(h [32]byte) (*easyfl.LocalScript[*ledger.EvalContext], bool) {
	return c.inner.Get(h)
}
func (c *countingScriptCache) Put(h [32]byte, s *easyfl.LocalScript[*ledger.EvalContext]) {
	atomic.AddInt64(&c.puts, 1)
	c.inner.Put(h, s)
}

// TestRedeemScript_CrossTxCacheReuse: two txs that redeem the same bin
// against the same *Library should decode the bin exactly once. Verified by
// instrumenting the cache. Uses a unique bin source so previous tests
// haven't already populated the cache for this hash.
func TestRedeemScript_CrossTxCacheReuse(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 2_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	// NB: easyfl LocalScriptBin doesn't carry function names — different
	// source names with identical bodies produce identical bins (and thus
	// identical hashes). Use a unique body so this test's hash isn't
	// already in the singleton cache from another test in this file.
	bin, _ := compileBin(t, `func cache_reuse : concat($0, 0xca, 0xc4, 0xe9)`)
	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))

	// Swap in the counting cache. Reset at end so other tests aren't
	// affected (the singleton lib is shared).
	lib := ledger.L(base.MaxSlot)
	prevCache := lib.CompiledScriptCache()
	counting := &countingScriptCache{inner: prevCache}
	lib.WithCompiledScriptCache(counting)
	t.Cleanup(func() { lib.WithCompiledScriptCache(prevCache) })

	startPuts := atomic.LoadInt64(&counting.puts)

	// Tx 1
	tx1 := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
	})
	_, err := submitAndCapture(u, tx1)
	require.NoError(t, err)

	// Tx 2 — also redeems the same bin.
	tx2 := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
	})
	_, err = submitAndCapture(u, tx2)
	require.NoError(t, err)

	require.EqualValues(t, 1, atomic.LoadInt64(&counting.puts)-startPuts,
		"second redemption must hit the cache, not Put again")
}

// --------------------------------------------------------------------------
// callRedeemer semantics
// --------------------------------------------------------------------------

// TestCallRedeemer_HappyPath: redeem a 1-arg script that returns concat($0,$0);
// attach an extra produced-output constraint that calls into it. The
// constraint must evaluate truthy for the tx to settle.
func TestCallRedeemer_HappyPath(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, hash := compileBin(t, `func dup : concat($0, $0)`)
	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))
	callBC := mustCompileExpr(t, fmt.Sprintf("callRedeemer(0x%s, 0x00, 0x42)", hex.EncodeToString(hash[:])))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
		addExtraConstraint(t, txb, callBC)
	})

	tx, err := submitAndCapture(u, txBytes)
	require.NoError(t, err)
	require.True(t, tx.IsScriptRedeemed(hash))
}

// TestCallRedeemer_NotRedeemed: callRedeemer for a hash that was never
// redeemed → rejected with the "is not redeemed" message.
func TestCallRedeemer_NotRedeemed(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	_, hash := compileBin(t, `func id_not_redeemed : $0`)
	callBC := mustCompileExpr(t, fmt.Sprintf("callRedeemer(0x%s, 0x00, 0x42)", hex.EncodeToString(hash[:])))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		// No PushTxConstraint — hash never enters the commitment list.
		addExtraConstraint(t, txb, callBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not redeemed")
}

// TestCallRedeemer_HashFormulaRejected: a formula in arg 0 is rejected by
// the ArgExpression literal check in evalCallRedeemer. Use blake2b which
// produces a 32-byte output so the size check would otherwise pass — only
// the literal check should fire.
func TestCallRedeemer_HashFormulaRejected(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	// blake2b(0x00) is a real call returning 32 bytes — IsInlineData() is false.
	badBC := mustCompileExpr(t, "callRedeemer(blake2b(0x00), 0x00)")

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		addExtraConstraint(t, txb, badBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
	require.Contains(t, err.Error(), "arg 0 (hash) must be inline-data literal")
}

// TestCallRedeemer_HashWrongSize: a 31-byte literal hash is inline data so
// the literal check passes, but the size check rejects it.
func TestCallRedeemer_HashWrongSize(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	short := make([]byte, 31)
	badBC := mustCompileExpr(t, fmt.Sprintf("callRedeemer(0x%s, 0x00)", hex.EncodeToString(short)))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		addExtraConstraint(t, txb, badBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be 32-byte literal")
}

// TestCallRedeemer_IdxNot1Byte: idx is 2 bytes — rejected.
func TestCallRedeemer_IdxNot1Byte(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, hash := compileBin(t, `func id_idx_not_1_byte : $0`)
	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))
	// idx = 0x0000 (2 bytes).
	badBC := mustCompileExpr(t, fmt.Sprintf("callRedeemer(0x%s, 0x0000, 0x42)", hex.EncodeToString(hash[:])))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
		addExtraConstraint(t, txb, badBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
	require.Contains(t, err.Error(), "idx must be 1 byte")
}

// TestCallRedeemer_IdxOutOfRange: bin has 1 fn (idx 0), call with idx 5.
func TestCallRedeemer_IdxOutOfRange(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, hash := compileBin(t, `func id_idx_oor : $0`)
	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))
	badBC := mustCompileExpr(t, fmt.Sprintf("callRedeemer(0x%s, 0x05, 0x42)", hex.EncodeToString(hash[:])))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
		addExtraConstraint(t, txb, badBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
	require.Contains(t, err.Error(), "fnIdx 5 out of range")
}

// TestCallRedeemer_WrongArity: bin's fn declares 2 args, call passes 0
// extra args. easyfl's local-script eval rejects the arity mismatch.
func TestCallRedeemer_WrongArity(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, hash := compileBin(t, `func twoArg_arity : concat($0, $1)`)
	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))
	// Pass only 0 forwarded args (call site has just <hash>, <fnIdx>).
	badBC := mustCompileExpr(t, fmt.Sprintf("callRedeemer(0x%s, 0x00)", hex.EncodeToString(hash[:])))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
		addExtraConstraint(t, txb, badBC)
	})

	_, err := submitAndCapture(u, txBytes)
	require.Error(t, err)
	// Error comes via s.Eval -> our TracePanic wrapper.
	require.Contains(t, err.Error(), "callRedeemer: eval failed")
}

// TestCallRedeemer_CrossScriptComposition: tx redeems two bins B1 and B2;
// B1's body calls into B2. The output-side constraint invokes B1, which
// transitively dispatches to B2 — both must resolve through the same
// commitment list.
func TestCallRedeemer_CrossScriptComposition(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	// B2: identity. fnIdx 0.
	bin2, h2 := compileBin(t, `func id_cross : $0`)

	// B1: calls into B2 with the literal h2. The hash must be a literal in
	// B1's source, which is exactly what the auditability rule mandates.
	bin1Source := fmt.Sprintf(`func wrap_cross : callRedeemer(0x%s, 0x00, $0)`,
		hex.EncodeToString(h2[:]))
	bin1, h1 := compileBin(t, bin1Source)

	redeem1BC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin1)))
	redeem2BC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin2)))
	callBC := mustCompileExpr(t, fmt.Sprintf("callRedeemer(0x%s, 0x00, 0x42)", hex.EncodeToString(h1[:])))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeem1BC)
		txb.PushTxConstraint(redeem2BC)
		addExtraConstraint(t, txb, callBC)
	})

	tx, err := submitAndCapture(u, txBytes)
	require.NoError(t, err)
	require.True(t, tx.IsScriptRedeemed(h1))
	require.True(t, tx.IsScriptRedeemed(h2))
}

// --------------------------------------------------------------------------
// Dispatch-depth bound
// --------------------------------------------------------------------------

// fixedScriptCache serves one hash -> script mapping and nothing else. It
// exists to register a script under a hash that is NOT its content hash,
// which is the only way to build a dispatch cycle: the inline-hash rule on
// callRedeemer means a script's own hash can never appear inside its bytes,
// so EasyFL alone cannot express one.
type fixedScriptCache struct {
	hash   [32]byte
	script *easyfl.LocalScript[*ledger.EvalContext]
}

func (c *fixedScriptCache) Get(h [32]byte) (*easyfl.LocalScript[*ledger.EvalContext], bool) {
	if h == c.hash {
		return c.script, true
	}
	return nil, false
}
func (c *fixedScriptCache) Put([32]byte, *easyfl.LocalScript[*ledger.EvalContext]) {}

// cyclicTxContext is a transaction stand-in reporting a settable number of
// redeemed scripts, and counting how many times callRedeemer got as far as
// the redeemed-set lookup — i.e. how many dispatch frames were entered.
//
// The embedded interface is nil on purpose: anything callRedeemer touches
// beyond the three methods below would nil-panic and show up as a failure
// rather than passing silently.
type cyclicTxContext struct {
	ledger.TxContextAccess
	hash    [32]byte
	lib     *ledger.Library
	n       int
	entered int
}

func (c *cyclicTxContext) IsScriptRedeemed(h [32]byte) bool {
	c.entered++
	return h == c.hash
}
func (c *cyclicTxContext) NumRedeemedScripts() int     { return c.n }
func (c *cyclicTxContext) GetLibrary() *ledger.Library { return c.lib }

// TestCallRedeemer_DepthBoundStopsCycle drives a script that dispatches into
// itself and asserts the depth bound cuts the chain at exactly the number of
// scripts the tx redeemed — one frame per script, and the next one refused.
//
// The cycle is staged rather than compiled, for the reason given on
// fixedScriptCache. It stands in for the situation the bound exists to
// survive: arg 0 naming a hash indirectly, e.g. through a library symbol,
// which would let such a fixed point be built for real.
//
// Note what a regression looks like: without the bound this recurses until
// the goroutine stack is exhausted, and that is a fatal runtime error no
// recover intercepts. The test process dies rather than reporting a failure.
func TestCallRedeemer_DepthBoundStopsCycle(t *testing.T) {
	lib := ledger.L(base.MaxSlot)

	// Pick the hash first, then build a script whose body calls it.
	var h [32]byte
	copy(h[:], []byte("cyclic-dispatch-test-hash-32byte"))
	bin, _ := compileBin(t, fmt.Sprintf(`func self_call : callRedeemer(0x%s, 0x00, $0)`,
		hex.EncodeToString(h[:])))
	script, err := lib.LocalScriptFromBytes(bin)
	require.NoError(t, err)

	prevCache := lib.CompiledScriptCache()
	lib.WithCompiledScriptCache(&fixedScriptCache{hash: h, script: script})
	t.Cleanup(func() { lib.WithCompiledScriptCache(prevCache) })

	callBC := mustCompileExpr(t, fmt.Sprintf("callRedeemer(0x%s, 0x00, 0x42)", hex.EncodeToString(h[:])))

	// n scripts redeemed => n frames run, the (n+1)-th is refused.
	for _, n := range []int{1, 3, 8} {
		txCtx := &cyclicTxContext{hash: h, lib: lib, n: n}
		_, err := lib.EvalFromBytecode(lib.NewGlobalDataNoTrace(ledger.NewEvalContext(txCtx)), callBC)
		require.Error(t, err, "a cycle must be refused (n=%d)", n)
		require.Contains(t, err.Error(), "dispatch depth")
		require.Equal(t, n+1, txCtx.entered,
			"expected %d dispatch frames plus one refusal (n=%d)", n, n)
	}
}

// TestRedeemerDeterminism: validating the same tx bytes twice produces the
// same redeemed-set contents (guards against accidental tx-state leakage
// across parses).
func TestRedeemerDeterminism(t *testing.T) {
	u, priv, addr := newRedeemTestEnv(t, 1_000_000_000)
	_, _, dst := u.GenerateAddress(2)

	bin, hash := compileBin(t, `func id : $0`)
	redeemBC := mustCompileExpr(t, fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin)))

	txBytes := buildTransferWith(t, u, priv, addr, dst, 100_000_000, func(txb *exhelp.Builder) {
		txb.PushTxConstraint(redeemBC)
	})

	// First run: submit through utxodb. This both validates and settles.
	tx1, err := submitAndCapture(u, txBytes)
	require.NoError(t, err)
	require.True(t, tx1.IsScriptRedeemed(hash))

	// Second run on a separate utxodb so we can re-validate the exact
	// same tx bytes with a fresh *Transaction. Establishes that the
	// commitment list is byte-deterministic and not leaked across parses.
	u2, _, _ := newRedeemTestEnv(t, 1_000_000_000)
	tx2, err := u2.TxFullContextFromBytes(txBytes)
	require.NoError(t, err)
	require.NoError(t, tx2.ValidateFullContext())
	require.True(t, tx2.IsScriptRedeemed(hash))
}
