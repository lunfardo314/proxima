//go:build js && wasm

// Package main is the syscall/js wrapper around ledger/txbuildercore,
// compiled with TinyGo to a WebAssembly binary usable from JS / React /
// any browser frontend:
//
//	tinygo build -target=wasm -o proxima_txb.wasm ./ledger/txbuildercore/wasm/
//
// It exposes a compose+sign transaction builder. Everything the wallet
// needs is supplied from the JS side: the compiled ledger library JSON,
// the consumed UTXO bytes, the ed25519 private key. The result is the
// raw canonical bytes of the signed transaction (returned as hex). No
// local validation — the host backend runs Stage-3 at submit time.
//
// See README.md in this directory for the JS-side glue + the full API
// reference, and claude/wasm_txbuilder.md for the refactor that made
// txbuildercore TinyGo-clean.
//
// ## Model
//
//   - The ledger library is a single package-global, initialised once
//     by InitLibrary(<library json string>).
//   - Multiple in-flight transactions are supported. Each is an
//     independent TxBuilder addressed by an int handle, kept in a
//     package-global map. NewTxBuilder(upgradeIndex) allocates one and
//     returns its handle; every builder op takes the handle as its
//     first argument.
//   - All byte payloads cross the JS boundary as hex strings; uint64
//     amounts cross as decimal strings (JS numbers lose precision above
//     2^53). Small indices/counts cross as JS numbers.
//
// All exports are installed as methods on the global object `proxima`.
// Every call returns a plain object: { ok: bool, err?: string, ... }.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"syscall/js"

	"github.com/lunfardo314/easyfl/engine"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// Package-global wallet state. Wasm is single-threaded, so no locking.
var (
	// lib is the compiled ledger library, set by InitLibrary. nil until
	// then; every builder/compose call checks it.
	lib *txbuildercore.Library[any]

	// libHash is the canonical library hash advertised by the host —
	// the `hash` field of the parsed JSON descriptor. The wallet's
	// bytecode emission is only accepted by a host running the matching
	// library version, so this is the value to compare against.
	libHash string

	// builders maps an int handle to a live TxBuilder. NewTxBuilder
	// allocates; FreeTxBuilder releases.
	builders   = map[int]*txbuildercore.TxBuilder{}
	nextHandle = 1
)

func main() {
	api := js.Global().Get("Object").New()
	for name, fn := range map[string]func([]js.Value) js.Value{
		// library / global
		"InitLibrary":       initLibrary,
		"LibraryHash":       libraryHash,
		"CompileExpression": compileExpression,
		// builder lifecycle
		"NewTxBuilder":  newTxBuilder,
		"FreeTxBuilder": freeTxBuilder,
		// generic compose
		"EncodeAmounts":      encodeAmounts,
		"EncodeIndexValues":  encodeIndexValues,
		"BuildOutput":        buildOutput,
		"DecodeTokenBalance": decodeTokenBalance,
		"ConsumeOutput":      consumeOutput,
		"ProduceOutput":      produceOutput,
		// convenience produce helpers (the common PRXI / tag-along path)
		"ProduceSigLockOutput":   produceSigLockOutput,
		"ProduceTagAlongOutput":  produceTagAlongOutput,
		"ProduceChainLockOutput": produceChainLockOutput,
		// unlocks / endorsements / tx-level
		"PutSignatureUnlock":      putSignatureUnlock,
		"PutUnlockReference":      putUnlockReference,
		"PutStandardInputUnlocks": putStandardInputUnlocks,
		"PushEndorsement":         pushEndorsement,
		"PushTxConstraint":        pushTxConstraint,
		// finalise + sign
		"SetTimestamp":           setTimestamp,
		"ComputeInputCommitment": computeInputCommitment,
		"SignED25519":            signED25519,
		"TxBytes":                txBytes,
		// key utilities
		"HolderIDFromPrivateKeyED25519": holderIDFromPrivateKeyED25519,
		"HolderIDFromPublicKeyED25519":  holderIDFromPublicKeyED25519,
	} {
		api.Set(name, wrap(fn))
	}
	js.Global().Set("proxima", api)

	// Keep the instance alive so JS can invoke the exports.
	select {}
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

// wrap adapts a (args)->Value function into a js.Func, recovering any
// panic into a { ok:false, err } object so a bad call from JS can never
// tear down the wasm instance.
func wrap(fn func([]js.Value) js.Value) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) (ret any) {
		defer func() {
			if r := recover(); r != nil {
				ret = errStr(fmt.Sprintf("panic: %v", r))
			}
		}()
		return fn(args)
	})
}

// ok builds a success object, merging in the given fields.
func ok(kv map[string]any) js.Value {
	o := js.Global().Get("Object").New()
	o.Set("ok", true)
	for k, v := range kv {
		o.Set(k, js.ValueOf(v))
	}
	return o
}

// errStr / errVal build a failure object.
func errStr(msg string) js.Value {
	o := js.Global().Get("Object").New()
	o.Set("ok", false)
	o.Set("err", msg)
	return o
}
func errVal(err error) js.Value { return errStr(err.Error()) }

// builderOf resolves the handle in args[argIdx] to a live builder.
func builderOf(args []js.Value, argIdx int) (*txbuildercore.TxBuilder, error) {
	if len(args) <= argIdx {
		return nil, errors.New("missing builder handle")
	}
	h := args[argIdx].Int()
	txb, found := builders[h]
	if !found {
		return nil, fmt.Errorf("unknown builder handle %d", h)
	}
	return txb, nil
}

// hexArg decodes a hex string argument.
func hexArg(args []js.Value, i int) ([]byte, error) {
	if len(args) <= i {
		return nil, fmt.Errorf("missing argument %d", i)
	}
	return hex.DecodeString(args[i].String())
}

// u64Arg parses a decimal-string uint64 argument (JS numbers are unsafe
// above 2^53, so amounts must arrive as strings).
func u64Arg(args []js.Value, i int) (uint64, error) {
	if len(args) <= i {
		return 0, fmt.Errorf("missing argument %d", i)
	}
	return strconv.ParseUint(args[i].String(), 10, 64)
}

// holderIDArg decodes a 32-byte holder ID hex argument.
func holderIDArg(args []js.Value, i int) (base.HolderID, error) {
	var id base.HolderID
	b, err := hexArg(args, i)
	if err != nil {
		return id, err
	}
	if len(b) != len(id) {
		return id, fmt.Errorf("holderID must be %d bytes, got %d", len(id), len(b))
	}
	copy(id[:], b)
	return id, nil
}

// hexArrayArg reads a JS array of hex strings into a [][]byte.
func hexArrayArg(args []js.Value, i int) ([][]byte, error) {
	if len(args) <= i {
		return nil, fmt.Errorf("missing argument %d", i)
	}
	v := args[i]
	n := v.Length()
	ret := make([][]byte, n)
	for j := 0; j < n; j++ {
		b, err := hex.DecodeString(v.Index(j).String())
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", j, err)
		}
		ret[j] = b
	}
	return ret, nil
}

// ---------------------------------------------------------------------
// library / global
// ---------------------------------------------------------------------

// InitLibrary(libraryJSON string) -> { ok, hash } | { ok:false, err }
// Parses the compiled ledger-library JSON and installs it as the
// package-global. Must be called once before any compose op.
func initLibrary(args []js.Value) js.Value {
	if len(args) < 1 {
		return errStr("InitLibrary: expected library JSON string")
	}
	var desc engine.LibraryFromJSON
	if err := json.Unmarshal([]byte(args[0].String()), &desc); err != nil {
		return errVal(fmt.Errorf("InitLibrary: parse JSON: %w", err))
	}
	l, err := txbuildercore.NewLibrary(&desc)
	if err != nil {
		return errVal(fmt.Errorf("InitLibrary: %w", err))
	}
	lib = l
	libHash = desc.Hash
	return ok(map[string]any{"hash": libHash})
}

// LibraryHash() -> { ok, hash }
// The canonical library hash the host advertises (the parsed JSON's
// `hash` field). Empty when the library JSON was non-compiled.
func libraryHash(_ []js.Value) js.Value {
	if lib == nil {
		return errStr("LibraryHash: library not initialised")
	}
	return ok(map[string]any{"hash": libHash})
}

// CompileExpression(source string) -> { ok, bytecode }
// Escape hatch: compiles an arbitrary EasyFL source expression to
// bytecode hex. Lets JS assemble any constraint the convenience helpers
// don't cover (delegation, foundry, redeemers, …) and feed it to
// BuildOutput / ProduceOutput / PushTxConstraint.
func compileExpression(args []js.Value) js.Value {
	if lib == nil {
		return errStr("CompileExpression: library not initialised")
	}
	if len(args) < 1 {
		return errStr("CompileExpression: expected source string")
	}
	code, err := lib.CompileExpression(args[0].String())
	if err != nil {
		return errVal(err)
	}
	return ok(map[string]any{"bytecode": hex.EncodeToString(code)})
}

// ---------------------------------------------------------------------
// builder lifecycle
// ---------------------------------------------------------------------

// NewTxBuilder(upgradeIndex number) -> { ok, handle }
func newTxBuilder(args []js.Value) js.Value {
	if lib == nil {
		return errStr("NewTxBuilder: library not initialised")
	}
	var upgradeIndex uint16
	if len(args) >= 1 {
		upgradeIndex = uint16(args[0].Int())
	}
	h := nextHandle
	nextHandle++
	builders[h] = txbuildercore.New(upgradeIndex)
	return ok(map[string]any{"handle": h})
}

// FreeTxBuilder(handle) -> { ok }
func freeTxBuilder(args []js.Value) js.Value {
	if len(args) < 1 {
		return errStr("FreeTxBuilder: missing handle")
	}
	delete(builders, args[0].Int())
	return ok(nil)
}

// ---------------------------------------------------------------------
// generic compose
// ---------------------------------------------------------------------

// EncodeAmounts([amountStr, ...]) -> { ok, bytecode }
// Serialises the amounts vector (output slot 0). Index 0 is the token
// balance, 1 is inflation, 2+ are frozen-coverage epochs; trailing
// zeros are elided. For the common "balance only" case pass a single
// element.
func encodeAmounts(args []js.Value) js.Value {
	if len(args) < 1 {
		return errStr("EncodeAmounts: expected array of decimal-string amounts")
	}
	v := args[0]
	n := v.Length()
	amounts := make([]uint64, n)
	for j := 0; j < n; j++ {
		a, err := strconv.ParseUint(v.Index(j).String(), 10, 64)
		if err != nil {
			return errVal(fmt.Errorf("element %d: %w", j, err))
		}
		amounts[j] = a
	}
	return ok(map[string]any{"bytecode": hex.EncodeToString(txbuildercore.EncodeAmounts(amounts...))})
}

// EncodeIndexValues([hex, ...]) -> { ok, bytecode }
// Serialises the index-values tuple (output slot 1). Master/sender
// holder at position 0, kind-specific extras after.
func encodeIndexValues(args []js.Value) js.Value {
	vals, err := hexArrayArg(args, 0)
	if err != nil {
		return errVal(fmt.Errorf("EncodeIndexValues: %w", err))
	}
	return ok(map[string]any{"bytecode": hex.EncodeToString(txbuildercore.EncodeIndexValuesTuple(vals))})
}

// BuildOutput([constraintHex, ...]) -> { ok, output }
// Assembles an output tuple from constraint bytecodes in slot order
// (slot 0 amounts, slot 1 index-values, slot 2 lock, …). The fully
// generic output composer — pair with CompileExpression / EncodeAmounts
// / EncodeIndexValues for any output shape.
func buildOutput(args []js.Value) js.Value {
	constraints, err := hexArrayArg(args, 0)
	if err != nil {
		return errVal(fmt.Errorf("BuildOutput: %w", err))
	}
	b := txbuildercore.NewOutputBuilder()
	for i, c := range constraints {
		b.PutConstraint(c, byte(i))
	}
	return ok(map[string]any{"output": hex.EncodeToString(b.Output().Bytes())})
}

// DecodeTokenBalance(outputHex) -> { ok, amount }
// Token balance (amounts slot 0) of an output, as a decimal string.
// Total the consumed inputs with this to compute the change output.
func decodeTokenBalance(args []js.Value) js.Value {
	b, err := hexArg(args, 0)
	if err != nil {
		return errVal(fmt.Errorf("DecodeTokenBalance: %w", err))
	}
	bal, err := txbuildercore.DecodeTokenBalance(b)
	if err != nil {
		return errVal(fmt.Errorf("DecodeTokenBalance: %w", err))
	}
	return ok(map[string]any{"amount": strconv.FormatUint(bal, 10)})
}

// ConsumeOutput(handle, outputHex, outputIDHex) -> { ok, index }
func consumeOutput(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	outBytes, err := hexArg(args, 1)
	if err != nil {
		return errVal(fmt.Errorf("ConsumeOutput: outputHex: %w", err))
	}
	oidBytes, err := hexArg(args, 2)
	if err != nil {
		return errVal(fmt.Errorf("ConsumeOutput: outputIDHex: %w", err))
	}
	oid, err := base.OutputIDFromBytes(oidBytes)
	if err != nil {
		return errVal(fmt.Errorf("ConsumeOutput: %w", err))
	}
	idx := txb.ConsumeOutput(outBytes, oid)
	return ok(map[string]any{"index": int(idx)})
}

// ProduceOutput(handle, outputHex) -> { ok, index }
func produceOutput(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	outBytes, err := hexArg(args, 1)
	if err != nil {
		return errVal(fmt.Errorf("ProduceOutput: %w", err))
	}
	return ok(map[string]any{"index": int(txb.ProduceOutput(outBytes))})
}

// ---------------------------------------------------------------------
// convenience produce helpers
// ---------------------------------------------------------------------

// ProduceSigLockOutput(handle, amountStr, holderIDHex) -> { ok, index }
func produceSigLockOutput(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	amount, err := u64Arg(args, 1)
	if err != nil {
		return errVal(fmt.Errorf("ProduceSigLockOutput: amount: %w", err))
	}
	holderID, err := holderIDArg(args, 2)
	if err != nil {
		return errVal(fmt.Errorf("ProduceSigLockOutput: holderID: %w", err))
	}
	out, err := txbuildercore.NewSigLockOutput(lib, amount, holderID)
	if err != nil {
		return errVal(err)
	}
	return ok(map[string]any{"index": int(txb.ProduceOutput(out.Bytes()))})
}

// ProduceTagAlongOutput(handle, feeStr, targetSeqIDHex, senderIDHex) -> { ok, index }
func produceTagAlongOutput(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	fee, err := u64Arg(args, 1)
	if err != nil {
		return errVal(fmt.Errorf("ProduceTagAlongOutput: fee: %w", err))
	}
	targetBytes, err := hexArg(args, 2)
	if err != nil {
		return errVal(fmt.Errorf("ProduceTagAlongOutput: targetSeqID: %w", err))
	}
	target, err := base.ChainIDFromBytes(targetBytes)
	if err != nil {
		return errVal(fmt.Errorf("ProduceTagAlongOutput: targetSeqID: %w", err))
	}
	sender, err := holderIDArg(args, 3)
	if err != nil {
		return errVal(fmt.Errorf("ProduceTagAlongOutput: senderID: %w", err))
	}
	out, err := txbuildercore.NewTagAlongOutput(lib, fee, target, sender)
	if err != nil {
		return errVal(err)
	}
	return ok(map[string]any{"index": int(txb.ProduceOutput(out.Bytes()))})
}

// ProduceChainLockOutput(handle, amountStr, chainIDHex) -> { ok, index }
func produceChainLockOutput(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	amount, err := u64Arg(args, 1)
	if err != nil {
		return errVal(fmt.Errorf("ProduceChainLockOutput: amount: %w", err))
	}
	chainBytes, err := hexArg(args, 2)
	if err != nil {
		return errVal(fmt.Errorf("ProduceChainLockOutput: chainID: %w", err))
	}
	chainID, err := base.ChainIDFromBytes(chainBytes)
	if err != nil {
		return errVal(fmt.Errorf("ProduceChainLockOutput: chainID: %w", err))
	}
	out, err := txbuildercore.NewChainLockOutput(lib, amount, chainID)
	if err != nil {
		return errVal(err)
	}
	return ok(map[string]any{"index": int(txb.ProduceOutput(out.Bytes()))})
}

// ---------------------------------------------------------------------
// unlocks / endorsements / tx-level
// ---------------------------------------------------------------------

// PutSignatureUnlock(handle, inputIndex) -> { ok }
func putSignatureUnlock(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	if len(args) < 2 {
		return errStr("PutSignatureUnlock: missing inputIndex")
	}
	txb.PutSignatureUnlock(byte(args[1].Int()))
	return ok(nil)
}

// PutUnlockReference(handle, inputIndex, constraintIndex, referencedInputIndex) -> { ok }
func putUnlockReference(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	if len(args) < 4 {
		return errStr("PutUnlockReference: expected (handle, inputIndex, constraintIndex, referencedInputIndex)")
	}
	if err := txb.PutUnlockReference(byte(args[1].Int()), byte(args[2].Int()), byte(args[3].Int())); err != nil {
		return errVal(err)
	}
	return ok(nil)
}

// PutStandardInputUnlocks(handle, n) -> { ok }
// Input 0 uses the signature; inputs 1..n-1 reference input 0's lock.
func putStandardInputUnlocks(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	if len(args) < 2 {
		return errStr("PutStandardInputUnlocks: missing n")
	}
	if err := txb.PutStandardInputUnlocks(args[1].Int()); err != nil {
		return errVal(err)
	}
	return ok(nil)
}

// PushEndorsement(handle, txidHex) -> { ok }
func pushEndorsement(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	b, err := hexArg(args, 1)
	if err != nil {
		return errVal(fmt.Errorf("PushEndorsement: %w", err))
	}
	txid, err := base.TransactionIDFromBytes(b)
	if err != nil {
		return errVal(fmt.Errorf("PushEndorsement: %w", err))
	}
	txb.PushEndorsements(txid)
	return ok(nil)
}

// PushTxConstraint(handle, bytecodeHex) -> { ok }
func pushTxConstraint(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	b, err := hexArg(args, 1)
	if err != nil {
		return errVal(fmt.Errorf("PushTxConstraint: %w", err))
	}
	txb.PushTxConstraint(b)
	return ok(nil)
}

// ---------------------------------------------------------------------
// finalise + sign
// ---------------------------------------------------------------------

// SetTimestamp(handle, slot, tick) -> { ok }
func setTimestamp(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	if len(args) < 3 {
		return errStr("SetTimestamp: expected (handle, slot, tick)")
	}
	txb.SetTimestamp(base.T(uint32(args[1].Int()), byte(args[2].Int())))
	return ok(nil)
}

// ComputeInputCommitment(handle) -> { ok }
func computeInputCommitment(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	txb.ComputeInputCommitment()
	return ok(nil)
}

// SignED25519(handle, privKeyHex) -> { ok }
// privKeyHex is either the 32-byte ed25519 seed or the full 64-byte
// private key. Signs the current builder state; call after all compose
// ops + ComputeInputCommitment.
func signED25519(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	priv, err := privKeyArg(args, 1)
	if err != nil {
		return errVal(fmt.Errorf("SignED25519: %w", err))
	}
	txb.SignED25519(priv)
	return ok(nil)
}

// TxBytes(handle) -> { ok, tx }
// Returns the raw canonical bytes of the (signed) transaction as hex —
// the value the wallet POSTs to the host's submit endpoint.
func txBytes(args []js.Value) js.Value {
	txb, err := builderOf(args, 0)
	if err != nil {
		return errVal(err)
	}
	return ok(map[string]any{"tx": hex.EncodeToString(txb.Bytes())})
}

// ---------------------------------------------------------------------
// key utilities
// ---------------------------------------------------------------------

// privKeyArg decodes a 32-byte seed or 64-byte full ed25519 key.
func privKeyArg(args []js.Value, i int) (ed25519.PrivateKey, error) {
	b, err := hexArg(args, i)
	if err != nil {
		return nil, err
	}
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	default:
		return nil, fmt.Errorf("private key must be %d (seed) or %d (full) bytes, got %d",
			ed25519.SeedSize, ed25519.PrivateKeySize, len(b))
	}
}

// HolderIDFromPrivateKeyED25519(privKeyHex) -> { ok, holderID, publicKey }
// The holder ID hashes (sigType || publicKey), so it is signature-type
// specific — this is the ED25519 form.
func holderIDFromPrivateKeyED25519(args []js.Value) js.Value {
	priv, err := privKeyArg(args, 0)
	if err != nil {
		return errVal(fmt.Errorf("HolderIDFromPrivateKeyED25519: %w", err))
	}
	pub := priv.Public().(ed25519.PublicKey)
	id := base.HolderIDFromPublicKey(base.SignatureTypeED25519, pub)
	return ok(map[string]any{
		"holderID":  hex.EncodeToString(id[:]),
		"publicKey": hex.EncodeToString(pub),
	})
}

// HolderIDFromPublicKeyED25519(publicKeyHex) -> { ok, holderID }
// ED25519 form — the holder ID embeds the signature type.
func holderIDFromPublicKeyED25519(args []js.Value) js.Value {
	pub, err := hexArg(args, 0)
	if err != nil {
		return errVal(fmt.Errorf("HolderIDFromPublicKeyED25519: %w", err))
	}
	if len(pub) != ed25519.PublicKeySize {
		return errStr(fmt.Sprintf("HolderIDFromPublicKeyED25519: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(pub)))
	}
	id := base.HolderIDFromPublicKey(base.SignatureTypeED25519, pub)
	return ok(map[string]any{"holderID": hex.EncodeToString(id[:])})
}
