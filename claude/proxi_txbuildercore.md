# Refactor proxi CLI to wasm-style wallet pattern

## Context

Today proxi tx-construction sites (~17 commands) reach into the
typed `ledger/txbuilder` wrapper and the ledger singleton (`ledger.L()`).
The user wants each site to look like an **external wasm wallet**:

1. Download `library.json` from the node API and build a local
   `*txbuildercore.Library`. Do NOT use the `ledger.L()` singleton.
2. Build transactions with `txbuildercore.TxBuilder` + the existing
   Phase A–E helpers. Do NOT use the sugared `ledger/txbuilder` package.
3. If a helper is missing, propose + create one — gated by user approval.
4. Submit via `/api/v1/submit_tx` with `validate_only=false`.
5. On failure, print error + full `LinesHR()` of the failing tx.
6. With `--verbose`, always print `LinesHR()` (success or failure).

This is the follow-up the submit-endpoint refactor (commit `7bbfa218`)
left for "proxi adaptation". Out of scope when that endpoint shipped;
in scope now.

## Design decisions (locked)

- **Two-version `Lines*` API**: refactor `ledger/transaction/util.go`
  so each printer has a raw form taking `*txbuildercore.Library` AND a
  backward-compat form that internally pulls `ledger.L(slot)`. Wallet
  callers use the raw forms; existing server / test callers keep the
  zero-arg signature.
- **`client.GetLibrary(slot *uint32) (*txbuildercore.Library, error)`** —
  new method on `api/client`. Wraps the existing `GetLedgerDefinition`,
  parses through `engine.LibraryFromJSON`, returns a ready-to-use
  wallet library.
- **Pretty-printer on failure**: always `LinesHR(lib, ...)` (full
  detail). Same on `--verbose` success.
- **`validate_only=false` hardcoded** everywhere; no `--dry-run` flag
  (user can revisit later).

## Phase 0 — Foundation

Single foundational commit before any per-site refactor.

### 0.1 Refactor `ledger/transaction/util.go` Lines API

For each of the four printers, add a raw form that takes a library.
Keep the existing signature as a backward-compat wrapper.

```go
// Raw form — wallet path. lib is the library that knows how to
// decompile constraints (typically built from the API's
// /get_ledger_definition response).
func (tx *Transaction) LinesHRWithLib(lib *txbuildercore.Library, prefix ...string) *lines.Lines
func (tx *Transaction) LinesShortWithLib(lib *txbuildercore.Library, prefix ...string) *lines.Lines
func (tx *Transaction) LinesSourceWithLib(lib *txbuildercore.Library, prefix ...string) *lines.Lines
func (tx *Transaction) LinesWithLib(lib *txbuildercore.Library, inputLoaderByIndex func(byte) (*ledger.Output, error), prefix ...string) *lines.Lines

// Backward-compat — internally uses ledger.L(tx.Timestamp.Slot).
// Existing callers (server, tests) get the same behaviour.
func (tx *Transaction) LinesHR(prefix ...string) *lines.Lines
func (tx *Transaction) LinesShort(prefix ...string) *lines.Lines
func (tx *Transaction) LinesSource(prefix ...string) *lines.Lines
func (tx *Transaction) Lines(inputLoaderByIndex func(byte) (*ledger.Output, error), prefix ...string) *lines.Lines

// Free function — same split.
func LinesFromTransactionBytesWithLib(lib *txbuildercore.Library, txBytes []byte, inputLoader func(byte) (*ledger.Output, error), prefix ...string) *lines.Lines
func LinesFromTransactionBytes(txBytes []byte, inputLoader func(byte) (*ledger.Output, error), prefix ...string) *lines.Lines // unchanged surface, internal singleton lookup
```

Internal note: the printers decompile bytecodes — they call into the
library's `DecompileBytecode` and `ParseBytecodeOneLevel`. Both APIs
exist on `*txbuildercore.Library` (promoted from
`*engine.Library[any]`). The current code calls `ledger.L(slot).Library`
to reach the engine library. Replace with the `lib *txbuildercore.Library`
parameter.

### 0.2 Add `client.GetLibrary(slot *uint32) (*txbuildercore.Library, error)`

In `api/client/client.go`. Reuses the existing `GetLedgerDefinition`
client method; decodes the JSON response through
`engine.LibraryFromJSON`; constructs the wallet library via
`txbuildercore.NewLibrary`.

```go
// GetLibrary fetches the ledger library descriptor for the given slot
// (latest if slot is nil) and constructs a wallet-side
// *txbuildercore.Library ready for composing transactions. Does NOT
// touch the ledger.L() singleton.
func (c *APIClient) GetLibrary(slot *uint32) (*txbuildercore.Library, error)
```

### 0.3 Add `proxi/glb/wallet_submit.go`

A focused helper file with three functions:

```go
// GetTxLibrary returns the per-process wallet library, fetched lazily
// from the API on first call. Cached for the lifetime of the proxi
// command process.
func GetTxLibrary() *txbuildercore.Library

// SubmitAndDisplay submits txBytes via the new /api/v1/submit_tx
// endpoint (validate_only=false).
//
// consumedUTXOBytes is an optional variadic parameter — each entry
// is the raw output wire-bytes for the corresponding tx input
// (positionally aligned with the tx's InputIDs). When non-empty
// the server runs full-context validation before submit. Passing
// no arg = parse + partial-context validation only at submit time.
//
// On submit failure, prints the error + LinesHR(lib) of the failing
// tx and returns the error. On success, prints LinesHR(lib) only
// when --verbose is on.
func SubmitAndDisplay(txBytes []byte, consumedUTXOBytes ...[]byte) error
```

Usage:

```go
// Default — no full-context validation server-side.
glb.SubmitAndDisplay(txBytes)

// With full-context validation (consumed is [][]byte aligned with tx.InputIDs).
glb.SubmitAndDisplay(txBytes, consumed...)
```

Internally `SubmitAndDisplay` calls `client.SubmitTransactionWithDetail`
with `client.WithConsumedUTXOs(consumed)` when consumedUTXOBytes is
non-empty; `validate_only` is hardcoded `false`.

The library is fetched once and reused. The existing
`InitLedgerFromNode()` call in proxi startup stays for now — it
populates the singleton used by display from non-refactored code.
After Phase 2 cleanup the singleton init can be removed.

### 0.4 Verification

- `go build ./...` clean.
- `go test ./ledger/... ./api/...` green (the `Lines*` refactor must
  not break existing tests since their signature is preserved).

## Phase 1 — Per-site refactor

Each command becomes one edit batch (single commit). User can pace per-site.

**Refactor template** (applied to every site):

```go
// 1. Library + wallet.
lib := glb.GetTxLibrary()
wallet := glb.GetWalletData()
holderID := base.HolderID(ledger.SigLockFromED25519PrivateKey(wallet.PrivateKey))

// 2. Fetch inputs / chain output via existing api/client methods
//    (GetOutputs, GetChainOutput, etc.) — these are unchanged.
inputs := client.GetOutputs(...)

// 3. Build via txbuildercore.
txb := txbuildercore.New(0)  // upgradeIndex set later by ledger lookup or hardcoded; revisit
for _, in := range inputs {
    txb.ConsumeOutput(in.Bytes, in.OID)
}
mainOut, _ := txbuildercore.NewSigLockOutput(lib, amount, targetHolderID)
txb.ProduceOutput(mainOut.Bytes())
// tag-along, chain unlock, etc. — via helpers
txb.PutSignatureUnlock(0)
txb.SetTimestamp(ts)
txb.ComputeInputCommitment()
txb.SignED25519(wallet.PrivateKey)

// 4. Submit + display.
if err := glb.SubmitAndDisplay(txb.Bytes(), inputs...); err != nil {
    return err
}
```

### Order (easiest first; gate per site)

1. **`proxi node send`** — simplest sigLock transfer + tag-along. Currently goes through `client.TransferFromED25519Wallet`. Replace.
2. **`proxi node send --tag`** (`send_tagged.go`) — sigLock + native-token + tag-along. Uses `lib.AppendTokenAmountToOutput`.
3. **`proxi node compact`** — consolidate own outputs.
4. **`proxi node fund`** — YAML-driven multi-output transfer.
5. **`proxi node killchain`** — end a chain.
6. **`proxi node mkchain`** — chain origin output. Currently `client.MakeChainOrigin`. Replace with `txbuildercore.NewChainOrigin`.
7. **`proxi node delegate amount`** — new delegation chain origin.
8. **`proxi node delegate chain`** — delegate existing chain (chain transition with new delegation lock).
9. **`proxi node delegate askstop`** — sequencer-request tag-along + ensureStopDelegation. Uses `lib.NewSequencerRequestOutput` + `lib.NewEnsureStopDelegationConstraint`.
10. **`proxi node seq withdraw`** — sequencer-request tag-along. Uses `lib.NewSequencerRequestOutput` with withdraw payload.
11. **`proxi node seq set-params`** — sequencer-request tag-along with set-seq-data payload.
12. **`proxi node foundry create`** — foundry chain origin.
13. **`proxi node foundry mint`** — foundry transit + tokenAmount output. Uses `lib.NewFoundryBytecode` + `lib.TokenFoundry` + `lib.AppendTokenAmountToOutput`.
14. **`proxi node foundry burn`** — foundry transit decreasing supply.
15. **`proxi node foundry retire`** — terminate foundry chain.
16. **`proxi node chess new`** — chess game chain origin (more involved; pushes redeemScript tx-constraint).
17. **`proxi node chess move`** — chess chain transition.

### Helpers gap (current best estimate)

The audit didn't surface any hard missing helpers. All necessary
primitives exist in `txbuildercore`:

| Helper | Used by | Already exists? |
|---|---|---|
| `NewSigLockOutput` | send, compact, fund | ✓ |
| `NewTagAlongOutput` | most sites | ✓ |
| `NewChainOrigin` / `NewChainTransition` | mkchain, delegate amount, delegate chain, foundry, killchain, chess, mint, burn, retire | ✓ |
| `ChainUnlockParams` / `FinishChainUnlockParams` | chain transitions / killchain / retire | ✓ |
| `NewDelegateLockBytecode` / `NewDelegateLockState` / `NewDelegationParams` | delegate amount / delegate chain | ✓ |
| `NewFoundryBytecode` / `TokenFoundry` / `TokenSentinel` / `NewTokenAmountBytecode` / `AppendTokenAmountToOutput` | foundry create/mint/burn, send --tag | ✓ |
| `NewSequencerRequestOutput` / `NewEnsureStopDelegationConstraint` | seq withdraw / set-params / delegate askstop | ✓ |
| `NewRedeemScriptConstraint` / `NewCallRedeemerConstraint` / `LocalScriptHash` | chess | ✓ |

**Per-site discovery:** if during a site's refactor a helper turns
out to be needed, **stop and ask** (per the user's constraint #3)
before creating it.

## Phase 2 — Cleanup (after all sites migrated)

Once no proxi callers reference `ledger/txbuilder.TransferData` /
`MakeTransferTransaction` / `MakeChainOrigin` / etc., delete or
unexport those legacy recipes. Also revisit whether
`InitLedgerFromNode()` is still needed at proxi startup.

This phase is gated on Phase 1 completion. Out of scope until then.

## Critical files referenced

- `api/client/client.go` — `GetLedgerDefinition`, `GetOutputs`,
  `GetChainOutput`, `SubmitTransactionWithDetail`,
  `TransferFromED25519Wallet`, `MakeChainOrigin`, `MakeSendOutputTransaction`.
- `ledger/transaction/util.go` (lines 18, 46, 81, 87, 221) — Lines* methods.
- `ledger/txbuildercore/library.go` — wallet library + embedded engine library.
- `ledger/txbuildercore/helpers_*.go` — all existing wallet helpers.
- `proxi/glb/profile.go` — `GetPrivateKey`, `GetWalletData`.
- `proxi/glb/node.go:43-68` — `InitLedgerFromNode` (singleton init).
- `proxi/glb/console.go:18-36` — `IsVerbose`, `Verbosef`.
- `proxi/node_cmd/*.go` + `proxi/node_cmd/{foundry,delegate,seq_cmd,chess_cmd}/*.go` — the 17 sites.

## Verification

Per-phase:

- After Phase 0: `go build ./...`, `go test ./ledger/... ./api/...`.
- After each Phase-1 site:
  - Build clean.
  - Spin up the single-node testnet (or rely on existing tests if any).
  - Run the refactored command against a real node; verify tx
    reaches LRB.
  - Run with `--verbose`; verify `LinesHR` is printed on success.
  - Run with a deliberately-invalid input; verify error + `LinesHR`
    is printed on failure.

## Per-edit + per-commit gates

The user's standing directive applies throughout. For each phase:
- Ask before each file edit batch.
- Ask before each commit.
- Ask before each push.

## Scope explicitly NOT in this plan

- API server-side changes (already shipped via commit `7bbfa218`).
- Ledger-side typed constructor changes.
- WASM wallet refactor (separate downstream task).
- Removing `ledger/txbuilder` package — depends on Phase 2 cleanup
  and is out of scope here.
