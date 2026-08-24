# Wallet eval/constants API — singleton-decoupling for external wallets

## Goal

Let an external wallet (and `proxi`) operate against a node WITHOUT
calling `glb.InitLedgerFromNode()` / reaching into `ledger.L(...)`.
The current proxi codebase keeps the singleton around for two
unrelated reasons:

1. **Reading runtime constants** of the ledger (e.g.
   `AttachmentCostBudget`, `TransactionPace`, `MaxFrozenEpochs`).
2. **Evaluating closed EasyFL formulas** at the active library
   (e.g. `storageDepositCost(<output>)`, `chainInflationOneSlot(...)`,
   `branchCoverageLowerBound(<slot>)`).

The wallet library (`txbuildercore.Library` = `engine.Library[any]`
built from `library.json`) is compile-only: it can emit bytecode and
parse one level of an existing bytecode, but it has NO embed
callback so it cannot evaluate. Bundling eval into the wallet would
mean shipping every Go-side embedded builtin — out of scope.

Two thin server endpoints close the gap without re-architecting the
library: `/api/v1/ledger_constants` and `/api/v1/eval`. Both are
keyed by slot so they pick the right library version across upgrades.

## API surface

### `GET /api/v1/ledger_constants?slot=N`

Returns the parsed `Constants` struct for the library active at slot
`N`. `slot` omitted → latest. Result is the JSON-marshalled
`txbuildercore.Constants` (see Phase A1 below). Cacheable per
(library hash, slot range) — immutable until the next library
upgrade.

**Wire encoding**: every numeric field is serialised as a JSON
integer (not a quoted string). 32-byte hash and the genesis
controller's public key go as plain hex strings (no `0x` prefix);
duration goes as integer nanoseconds. No `json:",string"` shims.

### `POST /api/v1/eval`

Batched evaluator. Request:

```json
{ "slot": N,
  "sources": [ "constAttachmentCostBudget",
               "storageDepositCost(0x<output-bytes>)",
               "chainInflationOneSlot(u64/1000000, u64/123)" ] }
```

Each entry MUST be a **closed** EasyFL formula (no `$0`/`$1` args).
Response carries results in the same order:

```json
{ "results": [ { "value": "0x00...22b" },
               { "value": "0x..." },
               { "error": "compile failed: ..." } ] }
```
>>>> in `value` no need for `0x` prefix, just hex string, possibly empty


`value` and `error` are mutually exclusive per entry. A single bad
formula doesn't fail the batch — each entry is independent. HTTP
2xx as long as the request itself parses; per-formula failures live
in `error`.

Server impl: for each source, `lib.EvalFromSource(nil, src)` under
`util.CatchPanicOrError`; bytes → hex on success, error.Error() on
failure. No per-call slicepool reuse needed (batch is small in
practice).

Wire size for `value`: hex string. Callers convert via
`easyfl_util.Uint64FromBytes` / `Uint32FromBytes` / raw bytes
depending on the formula. A typed convenience wrapper
(`EvalU64Batch`) can land next to the generic call in `api/client`.

### Client surface (`api/client`)

```go
func (c *APIClient) GetLedgerConstants(slot *uint32) (*txbuildercore.Constants, error)
func (c *APIClient) Eval(slot uint32, sources []string) ([]EvalResult, error)

type EvalResult struct {
    Value []byte  // nil iff Error != ""
    Error string  // empty iff Value != nil
}
```

`slot` semantics for `Eval`: 0 = "latest at request time", same as
elsewhere in the client.

## Phase A — `Constants` on the wallet library

### A1. `ledger/txbuildercore/constants.go`

Define `txbuildercore.Constants` as a flat struct of plain Go types
(`uint64`/`uint32`/`byte`/`ed25519.PublicKey`/`time.Duration`/
`string`). Mirror the fields of `ledger.Constants` that wallets
plausibly care about (`AttachmentCostBudget`, `TransactionPace`,
`TransactionPaceSequencer`, `TicksPerSlot`, `TickDuration`,
`GenesisTimeUnix`, `MaxNumberOfEndorsements`,
`PreBranchConsolidationTicks`, `DelegationEpochSlots`,
`MaxFrozenEpochs`, the four delegation bounds, `TagAlongSlots`,
`TagAlongReclaimSlots`, `SafeRevocationSlots`, `TxIDStateTTLSlots`,
`HealthyCoverage{Numerator,Denominator}`, `InitialSupply`,
`SlotInflationBase`, `MinimumInflatableAmount0`, `Hash`,
`GenesisControllerPublicKey`, `Description`). NO ledger imports —
the struct must be wallet-importable without dragging in the full
ledger package.

Methods that are pure functions of the fields are fine here
(`SlotDuration`, `LedgerTimeFromClockTime`, `GenesisTime`,
`IsPreBranchConsolidationTimestamp`). These move/duplicate from
`ledger.Constants` so the wallet can do clock math.

### A2. Host-side extractor

`ledger.ConstantsFromLibrary` already exists; keep it. Add a thin
adapter that converts `*ledger.Constants` → `*txbuildercore.Constants`
(field copy). Lives in `ledger/constants.go` next to the existing
extractor so it's obvious they stay in sync.

Don't try to share the struct between `ledger` and `txbuildercore`
(would force `ledger` to depend on `txbuildercore` for that one
struct, awkward). Two structs, one converter — cheap.

### A3. `GET /api/v1/ledger_constants?slot=N`

`api/server` handler: parse slot (default MaxSlot), look up
`ledger.L(slot)`, build `Constants` via `ConstantsFromLibrary`,
adapt to `txbuildercore.Constants`, JSON-marshal.

Add path constant `api.PathGetLedgerConstants` next to
`PathGetLedgerDefinition`.

### A4. `client.GetLedgerConstants(slot *uint32)`

Standard pattern (mirror `GetLedgerDefinition`). Returns
`*txbuildercore.Constants`.

### A5. `glb` cache

Add `glb.GetLedgerConstants() *txbuildercore.Constants`, lazy-loaded
once per process (same as `GetTxLibrary`). Cached by library hash —
if a future call returns a different hash we know an upgrade
happened (handle later; for now: cache once, ignore upgrades within
a single CLI invocation).

## Phase B — `/api/v1/eval`

### B1. Server handler

Path constant `api.PathEval`. JSON body in, JSON body out per the
shape above. Implementation:

```go
lib := ledger.L(req.Slot)
results := make([]apiEvalResult, len(req.Sources))
for i, src := range req.Sources {
    err := util.CatchPanicOrError(func() error {
        bin, err := lib.EvalFromSource(nil, src)
        if err != nil { return err }
        results[i].ValueHex = hex.EncodeToString(bin)
        return nil
    })
    if err != nil { results[i].Error = err.Error() }
}
```

Slot defaulting: `0` (or omitted) → `base.MaxSlot`.

### B2. Client method

```go
func (c *APIClient) Eval(slot uint32, sources []string) ([]EvalResult, error)
```

Single round-trip for the batch. Wire failures (HTTP non-2xx,
malformed response) return `error`. Per-formula failures land in
`EvalResult.Error`.

### B3. Typed sugar (optional, add as needed)

```go
func (c *APIClient) EvalU64(slot uint32, source string) (uint64, error)
```

Wraps `Eval` for the common single-formula uint64 case. Don't
prebuild dozens of sugars; add only when a real caller appears.

## Phase C — Apply to `proxi node compact`

Goal: `compact.go` no longer calls `glb.InitLedgerFromNode()`. No
stopgaps — the singleton-bound helpers it transitively uses are
ported, not lazily-initialised.

### C1. Drop the singleton call

Remove `glb.InitLedgerFromNode()` from `runCompactCmd`.

### C2. AttachmentCostBudget from constants

```go
budget := glb.GetLedgerConstants().AttachmentCostBudget
```

Replaces `ledger.L(targetSlot).AttachmentCostBudget`.

### C3. `targetSlot` from the wallet, not the singleton

The caller passes a `targetSlot` — the slot it intends to use as
the tx timestamp. The `sendWithDeadline` Δ check is meaningful
only at a specific slot (accept-window vs reclaim-window), so the
filter slot has to match the eventual tx slot.

The wallet derives this slot from `glb.GetLedgerConstants()` —
`Constants.LedgerTimeFromClockTime(time.Now()).Slot` is the
singleton-free equivalent of `ledger.TimeNow().Slot`. Constants
already arrived in Phase A.

### C3'. Server-side filter on `get_outputs`

Move the `spendableForAccount` logic to the server (it lives where
the singleton lives). Add two query parameters on
`/api/v1/get_outputs`:

  - `spendable=true` — apply the filter;
  - `target_slot=N` — the slot to use for the Δ check AND for the
    library version that dispatches the lock. 0 / omitted → server's
    current LRB slot.

Lock dispatch uses `ledger.LockFromOutputElementsWithLib(iv,
lockBin, ledger.L(target_slot))`, so the library version matches
the validating tx's slot.

The client's `GetSpendableOutputs` becomes a thin wrapper that sets
`spendable=true` + `target_slot=params.TargetSlot`, no client-side
dispatch, no `o.Output.Lock()` call. `SpendableOutputsParams.TargetSlot`
is preserved.

### C4. Lock-kind dispatch via the wallet library

The per-UTXO breakdown (sigCount / swdMasterCount / swdTargetCount)
doesn't need the singleton — `lib.ParseBytecodeOneLevel(lockBytes)`
returns the lock symbol ("sigLock", "sendWithDeadlineLock", …). For
sendWithDeadline, parse the master/target IDs out of the args
returned by ParseBytecodeOneLevel and compare to the wallet's
holder ID.

Implement as a small helper `glb.ClassifyLock(lockBytes,
walletHolderID) LockKind` so other sites can reuse it later. Three
kinds suffice for compact: `SigLockOwned`, `SWDMaster`,
`SWDTargetSig`, `Other`.

### C5. Port `MakeClaimingCompactTransaction` to txbuildercore

The recipe is the last singleton dep on the site path. Its
construction is small enough that the txbuildercore port is direct
— no `sendWithDeadline` unlock helper needed because, for an
input the WALLET is claiming, `PutSignatureUnlock(i)` covers all
three input flavours uniformly:

  - sigLock input: signature marker satisfies the holder check;
  - SWD master-reclaim: consumed-side dispatch lands in
    `_sigLock($master)`, falls through `unlockedByReference` (SWD
    lock bytecode ≠ sigLock bytecode), then the same signature
    check matches the wallet;
  - SWD target-accept: same fall-through, into `_sigLock($target)`.

Ported signature stays the same:

```go
func MakeClaimingCompactTransaction(walletPrivateKey ed25519.PrivateKey,
    tagAlongSeqID *base.ChainID, tagAlongFee uint64, targetSlot uint32,
    maxInputs int) (*transaction.Transaction, error)
```

Internally:
- `txbuildercore.New(0)`;
- iterate `c.GetSpendableOutputs(...)` (new server-filtered shape),
  `ConsumeOutput(in.Output.Bytes(), in.ID)` + `PutSignatureUnlock(i)`;
- `NewSigLockOutput(lib, sweepAmount, walletHolder)` (helper) for
  the sweep output; `NewTagAlongOutput(lib, fee, seq, sender)` for
  the fee;
- `SetTimestamp(base.T(targetSlot, 1))`; `ComputeInputCommitment`;
  `SignED25519`.

NO ledger-singleton dependency. The recipe takes `*txbuildercore.Library`
either as an arg or via `glb.GetTxLibrary()` from inside (compact is
proxi-only, so internal `glb.GetTxLibrary` is fine).

### C6. Verification

- `go build ./...`, `go test ./ledger/... ./api/...` clean.
- `proxi node compact` actually run against the single-node testnet:
  - no calls to `InitLedgerFromNode` on the site path;
  - constants endpoint hit exactly once;
  - lock breakdown matches prior output;
  - tx submits successfully.

## Phase D — sweep remaining proxi sites

Once Phases A–C land, audit every `proxi/node_cmd/**` for residual
`InitLedgerFromNode` calls and replace per the same playbook:
- runtime constants → `glb.GetLedgerConstants()`;
- closed formulas (inflation, storage deposit, branch coverage
  bounds) → `glb.GetClient().Eval(...)` (one batched call per site);
- current slot → derive from LRB ID;
- lock-kind / output-shape dispatch → `glb.ClassifyLock` or
  wallet-side `ParseBytecodeOneLevel`.

Known callsites to revisit (non-exhaustive, will firm up during the
sweep):
- `delegate/amount.go`: `ledger.L(ts.Slot).ChainInflationMultiStep`
  (Phase D — batched eval call).
- `delegate/chain.go`: `ledger.L(base.MaxSlot).ChainInflationOneSlot`
  (same).
- `mkchain.go`: `ledger.L(base.MaxSlot).TransactionPace`
  (constants).
- `foundry/mint.go`, `foundry/burn.go`, `foundry/retire.go`:
  `ledger.L(slot).TransactionPace` (constants).
- `chess_cmd/common.go`: `ledger.L(after.Slot).TransactionPace`
  (constants).

The legacy recipes in `wallet_recipes.go` stay singleton-bound until
Phase 1.3 of `claude/archive/shipped/proxi_txbuildercore.md` lands.

## Library JSON optimisation (deferred)

The constants endpoint requires one extra round-trip on every CLI
invocation. As a later optimisation, the host can bundle the
extracted constants inside `library.json` itself (new top-level
field), so `GetLibrary` returns lib + constants in one shot. Wire
size cost is small (a few hundred bytes). Not blocking — `proxi`
is fine with the extra call, and external wallets typically fetch
the library once and cache it.

Do NOT block Phase A on this; the JSON-embedding step lands cleanly
later because the client API doesn't change.

## Non-goals

- No wallet-side eval. The wallet stays compile-only.
- No re-architecting `ledger.Library`. The existing extractor stays
  put; we only add an adapter + endpoint.
- No removal of `ledger.L(...)` from server code. The singleton
  remains the source of truth host-side.
- No backwards-compat shims on the new endpoints. They're additive.

## Per-edit / per-commit gates

Standing directive applies. Ask before each file edit batch and
before each commit / push.
