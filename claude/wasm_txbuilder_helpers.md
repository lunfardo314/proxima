# txcore — additional compose helpers

**Status: SHIPPED on develop08 (2026-05-19).** 16 helpers across 5
thematic files, zero new wasm transitive imports beyond Phase 4.
Wasm probe unchanged at 1.35 MB / 442 KB gzipped.

| Phase | Scope | Commit |
|---|---|---|
| A | sequencer-request helpers (smallkv) | `4e010dec` |
| B | chain origin / transition / unlock params | `1164b3c3` |
| C | delegation lock / state / params | `e36ec35f` |
| D | foundry + native-token + indexer side-effect | `628cbfa1` |
| E | redeemScript / callRedeemer / LocalScriptHash | `152f5e28` |

Sibling: [wasm_txbuilder.md](wasm_txbuilder.md). Phases 0–6 shipped a
wasm-buildable txcore (compose + sign, sigLock + tagAlong helpers,
1.35 MB / 442 KB gzipped binary). This document scopes the next set
of wallet-side helpers — driven by the actual sequencer-request +
delegation flows in `sequencer/txbuilder_seq/req_*.go` and the
`proxi node_cmd/delegate` CLI commands — and audits the dep-graph
cost of adding each.

The constraint: every helper goes into `ledger/txcore` (or a small
sibling sub-package). It compiles down to bytecode through the
already-held `txcore.Library` and produces bytes / `*txcore.Output`.
**Nothing in the wallet path should depend on `ledger`, on
`proxima/util` (x/text drag), or on the typed constraint serdes.**

---

## What the wallet needs and doesn't have

### Already in txcore (Phase 4)

- `txcore.Library` — wraps `*engine.Library[any]`, compiles source.
- `NewSigLockOutput(lib, amount, holderID)` — sig-locked output.
- `NewTagAlongOutput(lib, fee, target, sender)` — 3-slot tag-along.
- `EncodeAmounts`, `EncodeIndexValuesTuple`.
- `OutputBuilder`, `TxBuilder`, sign + tx-ID derivation.

### Already in util/smallkv

- `smallkv.Map` — byte-keyed persistent map. The on-the-wire shape
  used by sequencer-request encoding.

### Missing — wallet flows that don't compose today

| Wallet flow | Source files (existing typed code) | Helpers needed |
|---|---|---|
| Send sequencer request via tag-along (set-seq-data, withdraw, ask-stop-delegation) | `sequencer/txbuilder_seq/req_seqdata.go`, `req_withdraw.go`, `req_askstop.go` | `NewSequencerRequestOutput` + `EnsureStopDelegation` |
| Create delegation lock output | `ledger/lock_delegate.go::NewDelegateLock` + `OutputBuilder.WithLock` | `NewDelegateLockOutput` |
| Chain origin / transition constraints | `ledger/chain.go::NewChainOrigin`, `NewChainConstraint` | `NewChainOrigin` + `NewChainTransition` |
| Chain unlock params (1-byte successor index) | `ledger/chain.go::NewChainUnlockParams` | `ChainUnlockParams` (no compile) |
| Chain-lock unlock params | `ledger/lock_chain.go::NewChainLockUnlockParams` | `ChainLockUnlockParams` (no compile) |
| Delegation params constraint | `ledger/delegation_params.go::NewDelegationParams` | `NewDelegationParams` (compile) |
| Foundry chain origin / transit | `ledger/foundry.go::NewFoundry` | `NewFoundryBytecode` (compile) |
| Native token declaration (tx-level) | `ledger/native_token.go::TokenSentinelBytecode`, `TokenFoundryBytecode` | `TokenSentinel` + `TokenFoundry` (compile) |
| Native token amount on output | `ledger/native_token.go::NewTokenAmount` + `OutputBuilder.WithTokenAmount` | `NewTokenAmountBytecode` + `AppendTokenAmountToOutput` |
| Redeem-script tx-level constraint (commit local-script bin) | `ledger/local_script_builtins.go` + DEX/chess examples | `NewRedeemScriptConstraint` (compile + LocalScript hash) |
| Call-redeemer constraint on output (with N typed args) | DEX/chess `callRedeemer(...)` source patterns | `NewCallRedeemerConstraint` (compile, variadic args) |

## The on-the-wire shapes

### Sequencer-request output (tag-along + smallkv params)

Every "wallet → sequencer" command rides as a normal tag-along
output with **one extra constraint slot** carrying inline-data
bytecode of a `smallkv.Map`-encoded parameter bundle:

```
slot 0  amounts (fee)
slot 1  index-values [senderID, targetSequencerID]
slot 2  tagAlong bytecode (canonical, per-Library cached)
slot 3  InlineDataBytecode(smallkv.Bytes())  -- params
slot 4+ optional extra constraints (e.g. ensureStopDelegation)
```

The smallkv map always carries `FieldCmdCode = 0` byte at key 0,
plus command-specific fields. Command codes in use today:

| Code | Command | Extra fields |
|---|---|---|
| 1 | withdraw-from-seq | `'a'` amount (z-trimmed uint64), `'t'` target (source string bytes) |
| 2 | set-seq-data | `'d'` parsed `seqdata.SequencerData` bytes |
| 3 | ask-stop-delegation | `'D'` delegation chainID (32 bytes); also adds slot-4 `ensureStopDelegation` constraint |

Wallet doesn't need to KNOW these codes are reserved by the
sequencer — it just needs the ability to put arbitrary (cmd code,
fields) into the smallkv envelope. The host wallet-doc tells JS
which code is which.

### Delegation lock output

The `delegateLock` is at output slot 2; its bytecode carries 4 typed
args, while the (master, target) pair lives in the index-values
tuple at slot 1.

Canonical source: `delegateLock(<m>, z16/<inflShare>, z32/<epochSlots>, <targetMaxFrozen>)`

Where `<m>` is either `0x` (when MaxFrozenEpochs == 0 or matches
target) or `<maxFrozenEpochs>` as a uint8 literal.

```
slot 0  amounts
slot 1  index-values [masterID, targetChainID]
slot 2  delegateLock(...)  bytecode
slot 3  chain(...) bytecode  -- delegations are chain outputs
slot 6  delegationParams(z32/<epochSlots>, <maxFrozenEpochs>)  -- optional
```

### Chain constraint (origin and transition)

Two canonical source forms:

```
origin:     chain(0x<chainID>, 0x<predRef>, z32/<originSlot>, 0x, 0x, 0x, 0x)
transition: chain(0x<chainID>, 0x<predRef>, z32/<originSlot>,
                  z64/<cumInfl>, z64/<cumBranchBonus>, z64/<txCounter>, z32/<branchCounter>)
```

(`predRef` is empty bytes at origin, 1-byte input index at transition.)

### Unlock params (no compile)

- Chain unlock: `[]byte{successorOutputIdx}` — 1 byte at the
  predecessor's chain constraint slot.
- Chain-lock unlock: `[]byte{predChainInputIdx}` — same shape.

### ensureStopDelegation

Single arg, just chainID hex:

```
ensureStopDelegation(0x<chainID>)
```

### Foundry (1-arg, slot 4 of a foundry chain output)

```
foundry(z64/<supply>)
```

A foundry output's overall shape:
```
slot 0  amounts (token balance — must be 0 for a pure foundry)
slot 1  index-values (master = lock controller)
slot 2  lock
slot 3  chain(...)  -- foundry IS a chain
slot 4  foundry(z64/<supply>)
slot 5  foundryPolicy (optional, currently unused on compose side)
```

The chain ID of this output IS the token tag (read by `token()` at
tx level, per native-token contract).

### Native token declarations (tx-level constraints)

Two shapes, both pushed onto the tx-level constraint list via
`TxBuilder.PushTxConstraint(bin)`. Each tx declares the set of token
tags it participates in:

```
pure conservation:   token(0x<tag>, 0xFF)
foundry transit:     token(0x<tag>, 0x<foundryProducedIdx>)
```

`tag` is the 32-byte chain ID. `foundryProducedIdx` is the produced
output index of the foundry being transited (0..254). `0xFF` is the
sentinel meaning "no foundry — pure conservation balance check".

### tokenAmount(tag, amount) on outputs

Appended (not Put-at-fixed-slot) to outputs carrying native tokens.
Multiple `tokenAmount` constraints per output are allowed (one per
tag carried).

```
tokenAmount(0x<tag>, z64/<amount>)
```

**Index-values compound entry side effect.** When `tokenAmount` is
added to an output that already has slot 1 populated (i.e. WithLock
was called), the existing OutputBuilder behaviour appends a 64-byte
`controller || tag` compound entry to slot 1 (dedup'd). This is what
lets the indexer write a "my UTXOs holding tag T" trie row. The
wallet helper has to mirror that side effect.

### redeemScript (tx-level, commits a LocalScriptBin)

Single tx-level constraint that publishes a `LocalScriptBin` so any
`callRedeemer(<hash>, …)` inside the same tx can resolve to it.
Compose pattern:

```
redeemScript(0x<localScriptBin-hex>)
```

The `<hash>` referenced by `callRedeemer` is `blake2b.Sum256(bin)`.
A wallet that calls `engine.CompileLocalScript(source)` to get `bin`
already knows the hash (or can compute it).

### callRedeemer (constraint on outputs)

```
callRedeemer(0x<scriptHash>, 0x<fnIdx>, arg0, arg1, ...)
```

`scriptHash` is the 32-byte blake2b of the bin published by some
`redeemScript` constraint (same tx, or the resolver's cache).
`fnIdx` is the function index inside the local script (1-byte
literal). `arg0`/`arg1`/… are arbitrary EasyFL literals — typed
helpers in `ledger/examples/dex/compile.go` format them as `z64/N`
/ `z32/N` / `z16/N` etc.

Wallet helper takes the args as a `[]string` of raw EasyFL source
fragments so the caller picks the typed encoding (it's the only
constraint family whose call shape is genuinely open-ended).

---

## Dep-graph audit per helper

For each missing helper, what does adding it pull in?

### `EnsureStopDelegation(lib, chainID) ([]byte, error)`

- Imports: `engine` (already in txcore), `base` (ChainID type),
  `encoding/hex`, `fmt`.
- New deps: NONE.

### `NewChainOrigin(lib, startSlot) ([]byte, error)` + `NewChainTransition(...)` 

- Imports: `engine`, `base`, `encoding/hex`, `fmt`.
- New deps: NONE.

### `ChainUnlockParams(idx byte) []byte` + `ChainLockUnlockParams(idx byte) []byte`

- Pure byte slice. No compile, no imports.
- New deps: NONE.

### `NewDelegateLockOutput(lib, amount, targetChainID, masterID, maxFrozenEpochs, inflShare, epochSlots, targetMaxFrozen) (*Output, error)`

- Imports: `engine`, `base`, `encoding/hex` (for index-values
  master/target), `fmt`.
- New deps: NONE.

### `NewDelegationParams(lib, epochSlots, maxFrozenEpochs) ([]byte, error)`

- Imports: `engine`, `fmt`.
- New deps: NONE.

### `NewSequencerRequestOutput(lib, fee, target, sender, requestCode, params *smallkv.Map, extras ...[]byte) (*Output, error)`

- Imports: `engine`, `base`, `util/smallkv`,
  `engine.InlineDataBytecode` (already accessible).
- **New dep: `util/smallkv`** for the txcore package.

util/smallkv's transitive deps: `easyfl_util`, `tuples`,
`util/lines`, `util/lazyargs`. The wasm path already has
`easyfl_util` and `tuples`. `util/lines` and `util/lazyargs` ARE
new transitive imports — but they were already in the wasm dep graph
before this conversation (TinyGo's DCE drops them from the binary
when no reachable code calls Lines()). Adding txcore → smallkv
edge doesn't change the binary; util/lines stays unreachable from
the actual wallet probe.

### Foundry / native-token helpers

`NewFoundryBytecode(lib, supply)`, `TokenSentinel(lib, tag)`,
`TokenFoundry(lib, tag, foundryProducedIdx)`,
`NewTokenAmountBytecode(lib, tag, amount)` — all pure
`lib.CompileExpression` over source templates.

`AppendTokenAmountToOutput(b *OutputBuilder, tag base.ChainID,
amount uint64, lib *Library)` — composite helper that pushes the
tokenAmount constraint AND mirrors the compound `controller || tag`
index-values dedup side effect. Mirrors the
`ledger.OutputBuilder.WithTokenAmount` behaviour byte-for-byte so
the indexer keeps emitting "my UTXOs holding T" trie rows.

- Imports: `engine`, `base`, `encoding/hex`, `fmt`,
  internal `bytes` for dedup.
- New deps: NONE.

### Redeemer helpers

`NewRedeemScriptConstraint(lib, bin []byte) ([]byte, error)` —
takes a `LocalScriptBin` (produced by
`engine.Library.CompileLocalScript`) and emits the tx-level
`redeemScript(0x<bin>)` constraint bytecode.

`NewCallRedeemerConstraint(lib, scriptHash [32]byte, fnIdx byte,
argsSrc ...string) ([]byte, error)` — emits
`callRedeemer(0x<hash>, 0x<fnIdx>, <argsSrc[0]>, <argsSrc[1]>, …)`.
The variadic `argsSrc` carries raw EasyFL literal fragments
(`"z64/123"`, `"z32/456"`, `"0xdeadbeef"`, …) — callRedeemer's
arg-list is genuinely open-ended (vararg constraint), so a string
slice is the honest shape.

`LocalScriptHash(bin []byte) [32]byte` — convenience: returns
`blake2b.Sum256(bin)`, so the wallet doesn't need to import blake2b
itself if it's already importing txcore.

- Imports: `engine` (CompileExpression + CompileLocalScript +
  LocalScriptBin), `encoding/hex`, `fmt`,
  `golang.org/x/crypto/blake2b` (already in the wasm path).
- New deps: NONE.

**Net new deps in the wasm wallet binary: zero.**

## What to NOT do

- **Don't import `ledger`.** That drags Lock parsers, EasyFL bodies,
  validators, x/text via util.Th, etc. — ~MB of weight.
- **Don't add a typed `DelegateLock` struct or `ChainConstraint`
  struct** in txcore. They'd require getters/setters and tempt
  someone later to add parsers (which depend on `engine.ParseBytecodeOneLevel`
  etc. — fine — but also on `Lock.IndexValues()` typed methods
  which are in ledger). Stay free-function only.
- **Don't pre-cache constraint bytecodes globally in txcore.** The
  existing Phase-4 helpers cache per-Library (via `lockCachesMu`
  map keyed by `*Library`). New helpers follow the same pattern.

## Proposed API

```go
package txcore

// --- Sequencer-request encoding -------------------------------------

// FieldCmdCode is the conventional smallkv key (0) that carries the
// 1-byte sequencer-request command code. Wallet-side mirror of the
// constant in sequencer/txbuilder_seq.
const FieldCmdCode = byte(0)

// NewSequencerRequestOutput builds a tag-along output carrying a
// smallkv-encoded request payload at slot 3 and optional extra
// constraints at slots 4+.
//
// The payload always has FieldCmdCode = requestCode prepended; any
// `params` entries are merged in.
func (l *Library) NewSequencerRequestOutput(
    fee uint64,
    target base.ChainID,
    sender base.HolderID,
    requestCode byte,
    params *smallkv.Map,
    extras ...[]byte,
) (*Output, error)

// NewEnsureStopDelegationConstraint compiles
//   ensureStopDelegation(0x<chainID>)
// to bytecode. Used at slot 4 of the ask-stop-delegation request.
func (l *Library) NewEnsureStopDelegationConstraint(chainID base.ChainID) ([]byte, error)

// --- Chain constraints ----------------------------------------------

// NewChainOrigin emits the bytecode for a chain-origin constraint
// (predInputIdx=0xff, all transition counters empty).
func (l *Library) NewChainOrigin(startSlot uint32) ([]byte, error)

// NewChainTransition emits the bytecode for a chain transition
// constraint with all counter fields.
func (l *Library) NewChainTransition(
    chainID base.ChainID,
    predInputIndex byte,
    originSlot uint32,
    cumChainInflation uint64,
    cumBranchBonus uint64,
    transitionCounter uint64,
    branchCounter uint32,
) ([]byte, error)

// ChainUnlockParams returns the canonical 1-byte unlock-params
// payload pointing at the successor's output index.
func ChainUnlockParams(successorOutputIdx byte) []byte { return []byte{successorOutputIdx} }

// ChainLockUnlockParams mirrors ChainUnlockParams for chainLock-locked
// inputs (predecessor chain-input index).
func ChainLockUnlockParams(predChainInputIdx byte) []byte { return []byte{predChainInputIdx} }

// --- Delegation -----------------------------------------------------

// NewDelegateLockBytecode emits the 4-arg delegateLock constraint
// bytecode (slot 2 of a delegation output).
func (l *Library) NewDelegateLockBytecode(
    maxFrozenEpochs byte,
    requiredInflationShare uint16,
    epochSlots uint32,
    targetMaxFrozenEpochs byte,
) ([]byte, error)

// NewDelegateLockOutput composes a complete delegation output:
//   slot 0  amounts(amount)
//   slot 1  [masterID, targetChainID]
//   slot 2  delegateLock(...)
//   slot 3  chain(...)  -- caller supplies via chainBin (Origin or Transition)
//   slot 6  delegationParams(...)  -- optional, slot left empty if both zero
func (l *Library) NewDelegateLockOutput(
    amount uint64,
    targetChainID base.ChainID,
    masterID base.HolderID,
    maxFrozenEpochs byte,
    requiredInflationShare uint16,
    epochSlots uint32,
    targetMaxFrozenEpochs byte,
    chainBin []byte,           // result of NewChainOrigin / NewChainTransition
    delegationParamsBin []byte, // optional; nil to skip slot 6
) (*Output, error)

// NewDelegationParams emits the 2-arg delegationParams constraint at
// slot 6.
func (l *Library) NewDelegationParams(epochSlots uint32, maxFrozenEpochs byte) ([]byte, error)

// --- Foundry --------------------------------------------------------

// NewFoundryBytecode emits the 1-arg foundry(z64/supply) constraint
// at slot 4 of a foundry chain output. The tag is read off the
// sibling chain constraint at slot 3, not from foundry itself.
func (l *Library) NewFoundryBytecode(supply uint64) ([]byte, error)

// --- Native tokens (tx-level + per-output) --------------------------

// TokenSentinel emits the tx-level "pure conservation" declaration:
//   token(0x<tag>, 0xFF)
// Push via TxBuilder.PushTxConstraint when the tx moves tokens of
// tag T without touching any foundry.
func (l *Library) TokenSentinel(tag base.ChainID) ([]byte, error)

// TokenFoundry emits the tx-level foundry-transit declaration:
//   token(0x<tag>, 0x<foundryProducedIdx>)
// Push when the tx mints / burns via the produced foundry at the
// given output index.
func (l *Library) TokenFoundry(tag base.ChainID, foundryProducedIdx byte) ([]byte, error)

// NewTokenAmountBytecode emits a tokenAmount(0x<tag>, z64/<amount>)
// constraint on a produced output. Appending the bytecode to an
// OutputBuilder is left to the caller; AppendTokenAmountToOutput is
// the composite helper that also mirrors the
// `controller || tag` slot-1 index-values dedup side effect.
func (l *Library) NewTokenAmountBytecode(tag base.ChainID, amount uint64) ([]byte, error)

// AppendTokenAmountToOutput pushes the tokenAmount constraint AND
// (if slot 1 already carries a primary controller) appends a 64-byte
// `controller || tag` compound entry to slot 1, dedup'd. Mirrors
// ledger.OutputBuilder.WithTokenAmount byte-for-byte. Call AFTER the
// lock has been written to slot 1.
func (l *Library) AppendTokenAmountToOutput(b *OutputBuilder, tag base.ChainID, amount uint64) error

// --- Redeem scripts (local-script tx-level commit + dispatch) -------

// NewRedeemScriptConstraint emits the tx-level constraint
//   redeemScript(0x<bin>)
// bin is a LocalScriptBin (the wallet calls
// l.Inner.CompileLocalScript(source) to produce it).
func (l *Library) NewRedeemScriptConstraint(bin []byte) ([]byte, error)

// LocalScriptHash returns blake2b.Sum256(bin) — the same hash
// callRedeemer expects when referring to a bin published by an
// earlier redeemScript constraint.
func LocalScriptHash(bin []byte) [32]byte

// NewCallRedeemerConstraint emits
//   callRedeemer(0x<scriptHash>, 0x<fnIdx>, <argsSrc[0]>, <argsSrc[1]>, …)
// argsSrc carries raw EasyFL literal fragments — callRedeemer is
// vararg by design, so the caller picks the typed encoding per arg.
// Common literal forms: "z64/123", "z32/456", "z16/7", "0xdeadbeef".
func (l *Library) NewCallRedeemerConstraint(scriptHash [32]byte, fnIdx byte, argsSrc ...string) ([]byte, error)
```

## Final state

All five phases landed in five small independent commits. Each
commit carries byte-identity tests against the existing typed
`ledger.*` constructors so wallet bytecode is bit-identical to the
server's compose path.

| Phase | File | Commit | Tests |
|---|---|---|---|
| A | `ledger/txcore/helpers_seq.go` | `4e010dec` | 4 (3 byte-identity vs `sequencer/txbuilder_seq.New*ReqOutput`, 1 standalone `EnsureStopDelegation`) |
| B | `ledger/txcore/helpers_chain.go` | `1164b3c3` | 5 (origin / transition / unlock-params / finish-chain / chain-lock unlock) |
| C | `ledger/txcore/helpers_delegate.go` | `e36ec35f` | 3 (lock bytecode, lock state, delegation params) |
| D | `ledger/txcore/helpers_native_token.go` | `628cbfa1` | 6 (foundry / token sentinel / token foundry / tokenAmount + 2 end-to-end output composition incl. dedup) |
| E | `ledger/txcore/helpers_redeemer.go` | `152f5e28` | 4 (redeemScript / hash determinism / callRedeemer no-args + variadic / round-trip via TxBuilder) |

Phase C deferred the carrier-struct decision flagged in the design
phase — `NewDelegateLockBytecode` ended up taking the 4 args
directly (matching `ledger.DelegateLock.Source()` exactly), and
chain transition kept the 7-arg signature. Both signatures map 1:1
to the underlying constraint's args; carrier structs would have
been one extra hop without simplification.

**Wasm impact (measured):** the probe at `ledger/txcore/wasm/`
stayed at 1.35 MB / 442 KB gzipped throughout. The new `*Library`
methods are unreachable from the probe (it doesn't construct a
Library), so TinyGo DCE drops them all. `golang.org/x/crypto/blake2b`
was already linked via `sign.go` and `output.go`; `easyfl/tuples`
was already linked via `output.go`. The only genuinely new import
is `bytes` in the Phase D dedup path, which is negligible.

---

## Summary

**Helper inventory** — 16 functions across 5 thematic groups:

| Group | Count | New file in txcore |
|---|---|---|
| Sequencer requests + ensureStopDelegation | 2 | `helpers_seq.go` |
| Chain (origin / transition / unlock params) | 4 | `helpers_chain.go` |
| Delegation (lock / params / output composer) | 3 | `helpers_delegate.go` |
| Native tokens (foundry / token tx-constraints / tokenAmount + index-side-effect) | 5 | `helpers_native_token.go` |
| Redeemers (redeemScript / callRedeemer / script-hash util) | 2 + 1 free function | `helpers_redeemer.go` |

**Dep impact:** zero new transitive imports in the wasm wallet
binary. util/smallkv joins the wallet path (its util/lines +
util/lazyargs deps are already DCE'd by TinyGo). blake2b is already
in the wasm path via HashOutputs.

**TxConstraints "in general":** the underlying `TxBuilder.PushTxConstraint(bin)`
plumbing has been in txcore since Phase 2c. The helpers above just
provide the canonical-source compilation for the three real tx-level
constraint families today: native-token declarations (`token(...)`)
and redeem-script commits (`redeemScript(...)`). Future tx-level
constraints can use the same pattern with no further txcore changes.

**Effort:** 5 small commits (A–E), each with byte-identity tests
against the existing typed ledger.* constructors. Wasm size budget
held: probe stays at 1.35 MB / 442 KB gzipped — methods on
`*Library` are DCE'd when no `Library` is constructed in the probe,
so the helpers cost zero wasm bytes until a wallet actually wires
them in.
