# Refactor Ledger Library Upgrades

## Status: ✅ COMPLETED

All implementation phases (1-16) are complete.

---

## Upgrade Activation Slot Behavior

### Question
When a ledger upgrade is activated at slot S, is the branch transaction at slot S validated with the upgraded (new) library?

### Answer
**YES.** The branch transaction at upgrade activation slot S is validated with the **new (upgraded) library**. This branch transaction is the first transaction validated with the new library.

### Implementation Details

The `L(slot)` function in `ledger/lib_singleton.go` returns the library applicable to a given slot. The core logic is in `findUpgradeSlotForSlot()`:

```go
// ledger/lib_singleton.go - findUpgradeSlotForSlot
for i, s := range lc.upgradeSlots {
    if s > slot {
        break
    }
    // ...
    upgradeSlot = s
}
```

**Key insight:** The loop finds the largest upgrade slot `<= slot`, which means:
- When `slot == upgradeSlot`, the **new** library at that upgrade slot is returned
- The library is effective starting from its upgrade slot

### Attacher Library Initialization

The attacher caches the library in `newPastConeAttacher()`:

```go
// core/attacher/attacher.go:20-22
ret := attacher{
    Environment: env,
    Library:     ledger.L(txTs.Slot),  // Gets library for transaction's slot
    ...
}
```

For a branch transaction at upgrade slot S:
1. `txTs.Slot` = S (branch timestamp is on slot boundary, tick == 0)
2. `ledger.L(S)` is called
3. `findUpgradeSlotForSlot(S)` finds the largest upgrade slot `<= S`
4. Since S is the upgrade slot, the new library at slot S is selected
5. The **new library is returned** and cached in the attacher

### Verification

The behavior is confirmed by test cases in `ledger/multistate/upgrades_test.go`:

```go
{1000, lib1000, 1000, true, "exactly at first upgrade"},     // ← uses NEW library
{1001, lib1000, 1000, true, "just after first upgrade"},     // ← uses new library
{999, lib0, 0, true, "just before first upgrade"},           // ← uses OLD library
```

### Design Implications

1. **Immediate Activation:** Upgrades take effect immediately at the activation slot
2. **Deterministic:** All nodes agree on which library to use for each slot
3. **Branch Transaction:** The first branch at upgrade slot S is validated with new rules
4. **Upgrade UTXO:** An upgrade UTXO is injected at the branch, committing to the new library hash
5. **Backward Compatibility:** When consuming outputs from older library versions, `OutputFromBytesWithLib()` handles deterministic parsing

---

## Quick Reference for Testing and Documentation

### Key Files by Function

**Library Access:**
- `ledger/lib_singleton.go` - `L(slot)`, `LibraryCache`, `ResolverFactory`, resolver registration
- `ledger/lib.go` - `Library` struct, `UpgradeChainData`, `LibraryHash()`

**Upgrade Storage:**
- `ledger/multistate/upgrades.go` - DB partition read/write: `WriteUpgradeLibrary()`, `GetUpgradeLibraryDirect()`
- `ledger/multistate/upgrade_inject.go` - `InjectMissingUpgradeUTXOs()`, injection during branch commits

**Upgrade UTXO:**
- `ledger/upgrade_utxo.go` - `UpgradeUTXO()`, `ParseUpgradeUTXO()`, `VerifyUpgradeUTXO()`, `BaseLibraryHash()`
- `ledger/base/upgrade_output_id.go` - Synthetic OutputID: `UpgradeOutputID()`, `IsUpgradeOutputID()`

**Genesis:**
- `ledger/multistate/genesis_snapshot.go` - `CreateGenesisSnapshot()` - in-memory genesis builder
- `ledger/ledger_identity.go` - `LedgerIdentity` - minimal trie root data

**Pending Upgrades:**
- `ledger/def_upgrade.go` - `UpgradeDefinition`, `PendingUpgrade` variable
- `ledger/def_embed.go` - `upgradeEmbeddedResolvers`, `GetEmbeddedFunctionResolver`
- `docs/upgrade.md` - Upgrade authoring guide

**Snapshot:**
- `ledger/multistate/snapshot.go` - Format with upgrade libraries
- `core/core_modules/snapshot_restore/` - Restore logic, auto-bootstrap

**API/CLI:**
- `api/api.go` - `PathGetLedgerDefinition`, `LedgerDefinition` response struct
- `api/server/server.go` - `getLedgerDefinition()` handler
- `proxi/db_cmd/upgrades.go` - `proxi db upgrades` command
- `proxi/init_cmd/init_genesis.go` - `proxi init genesis` command

**Transaction Validation:**
- `ledger/transaction/tx.go` - `Transaction.lib` cached library, `Library()` method
- `ledger/transaction/validate.go` - Uses `ctx.Transaction.Library()` for validation

### Core Concepts

**Upgrade Slot:** First slot where new library rules apply (max one upgrade per slot)

**L(slot):** Returns library version applicable to the slot. Lazy loaded, cached by upgrade slot.

**Synthetic OutputID Format (33 bytes):**
```
[5-byte timestamp: upgrade slot, ticks=0, seq=0]
[1-byte output count: 0xff]
[26-byte hash: big-endian slot number, zero-padded]
[1-byte index: 0xff]
```

**Upgrade UTXO Constraints:**
```
[0] amount: 0
[1] lock: false (empty inline data lock)
[2] library hash (32 bytes)
[3] previous library hash (32 bytes)
[4] previous upgrade slot (4 bytes BigEndian)
```

**Snapshot Format (ver 1):**
```
[header JSON: version="ver 1"]
[root record: branchID + RootRecord bytes]
[upgrade count: key=0x06, value=4-byte BE count]
[for each upgrade: key=4-byte BE slot, value=yaml bytes]
[trie data records]
```

### Testing Scenarios

1. **Genesis from snapshot:**
   ```bash
   proxi init genesis -w wallet.yaml -o genesis.snapshot
   # Delete proximadb, start node - should auto-restore
   ```

2. **Upgrade UTXO injection:**
   - Set `PendingUpgrade` in `ledger/def_upgrade.go`
   - Start node, create branch at/after upgrade slot
   - Verify upgrade UTXO in state

3. **Slot-aware validation:**
   - Create transaction at slot N
   - Verify `Transaction.Library()` returns correct version
   - Verify validation uses slot-appropriate library

4. **API queries:**
   ```bash
   curl /api/v1/get_ledger_definition  # Latest
   curl /api/v1/get_ledger_definition?slot=0  # Genesis library
   proxi db upgrades  # Full history
   proxi db info  # Summary
   ```

5. **Snapshot round-trip:**
   - Create snapshot with upgrades
   - Restore to new DB
   - Verify all upgrade libraries present

### Creating an Upgrade

1. Set `PendingUpgrade` in `ledger/def_upgrade.go`:
```go
var PendingUpgrade = &UpgradeDefinition{
    Slot:  100000,
    Build: buildUpgradeNLibrary,
}
```

2. Create `ledger/def_upgradeN.go` with YAML definitions and resolver:
```go
//go:embed upgradeN_defs.yaml
var _upgradeNDefsYAML []byte

func resolveEmbeddedUpgradeN(sym string) easyfl.EmbeddedFunction[*EvalContext] {
    // Return embedded functions for this upgrade
}

func buildUpgradeNLibrary(prevYAML []byte) ([]byte, error) {
    lib, err := ParseLibraryFromYAML(prevYAML, GetEmbeddedFunctionResolver)
    // ... apply upgrade definitions ...
    return lib.ToYAML(true), nil
}
```

3. Add resolver to `upgradeEmbeddedResolvers` in `ledger/def_embed.go`

**See `docs/upgrade.md` for complete step-by-step guide.**

### Backward Compatibility Notes

- Upgrade code must maintain backward-compatible bytecode parsing
- `numArgs` cannot change for replaced functions (EasyFL enforces)
- Old embedded function implementations must remain forever
- Use `embedded-as` in YAML to map function names to new implementations

### Key Constants

- `upgradeLibraryDBPartition = 0x06` - DB partition for upgrade libraries
- `SyntheticUpgradeOutputIndex = 0xff` - Output index for upgrade UTXOs
- `base.MaxSlot = 0xFFFFFFFF` - Sentinel for "latest library" / "base library as previous"

---

## Full Implementation Details

See sections below for complete phase documentation (preserved for reference).

---

## Completed Phases Summary

| Phase | Description | Commit |
|-------|-------------|--------|
| 1 | Storage Layer (DB Partition) | fe98f945 |
| 2 | L(slot) with Caching | (multiple) |
| 3 | Genesis Changes | (merged with 4) |
| 4 | Upgrade UTXO Mechanics | (merged with 3) |
| 5 | Branch Production Integration | (multiple) |
| 6 | Snapshot Format | (multiple) |
| 7 | Genesis as Snapshot | (multiple) |
| 8 | Node Startup from Snapshot | (multiple) |
| 9 | Single Pending Upgrade | (multiple) |
| 10 | Transaction Validation | (multiple) |
| 11 | API and CLI Updates | ca353ba0 |
| 12 | EasyFL numArgs Immutability | (EasyFL dependency) |
| 13 | Code Review and Cleanup | 7c8b5f8e |
| 14 | Library Pointer Caching | a8526c4e |
| 15 | Constants in Library Structure | ✅ DONE |
| 16 | L(slot) Fast Path Optimization | ✅ DONE |

---

## Phase 16: L(slot) Fast Path Optimization

### Problem

The `getOrLoad()` and `findLibraryForSlot()` functions traversed the DB on every call to find the applicable library for a slot. This was inefficient since:
1. Upgrades are rare (typically just one library at slot 0)
2. Most calls request the latest library
3. DB traversal is expensive compared to a simple pointer return

### Solution

Cache upgrade slots once at initialization and add a fast path for the common case:

```go
type LibraryCache struct {
    // ... existing fields ...

    // Fast-path: cache latest library directly (most common case)
    latestLib         *Library
    latestUpgradeSlot uint32

    // Slot index loaded once from DB to avoid repeated traversal
    upgradeSlots []uint32          // sorted ascending
    slotToYAML   map[uint32][]byte // for lazy parsing
}
```

**Fast path in `getOrLoad()`:**
```go
// Most common case: requesting current/latest library
if lc.latestLib != nil && slot >= lc.latestUpgradeSlot {
    return lc.latestLib  // O(1), no map lookup, no DB access
}
```

**Changes:**
1. `loadUpgradeSlots()` - loads all slots from DB once during init
2. `findUpgradeSlotForSlot()` - linear search in cached `upgradeSlots` (replaces old `findLibraryForSlot`)
3. `getOrLoad()` - fast path returns `latestLib` directly when `slot >= latestUpgradeSlot`

**Result:** The common case (`L(slot)` where slot >= latest upgrade) is now O(1) with no map lookup or DB access.

---

## Phase 15: Constants in Library Structure

### Problem

The global singleton `Const *Constants` in `ledger/constants.go` prevents upgrade-aware constant access. Since ledger constants can be modified upon upgrade, each library version needs its own `Constants` instance.

### Current State

- `Const` is a global singleton initialized once at startup via `initConstantsSingleton()`
- `ConstantsFromLibrary()` extracts constants from EasyFL library
- ~237 usages of `ledger.Const.X` or `Const.X` across 42+ files

### Solution

Embed `Constants` directly in the `Library` structure (not as pointer). Access becomes `L(slot).Constants.X`.

### Implementation Steps

#### Step 1: Embed Constants in Library Structure

**File: `ledger/lib.go`**
```go
type Library struct {
    *easyfl.Library[*EvalContext]
    definitionsYAML    []byte
    constraintByPrefix map[string]*constraintRecord
    constraintNames    set.Set[string]
    locksByName        map[string]LockParser
    inlineTests        []func()
    upgradeChainData   *UpgradeChainData
    Constants          Constants  // Embedded constants for this library version
}
```

#### Step 2: Initialize Constants During Library Loading

**File: `ledger/lib_singleton.go`**

In `parseLibrary()`:
```go
func (lc *LibraryCache) parseLibrary(upgradeSlot uint32, yamlData []byte) *Library {
    lib, err := ParseLibraryFromYAML(yamlData, GetEmbeddedFunctionResolver)
    util.AssertNoError(err)

    result := newLibrary(lib, yamlData)
    result.Constants = *ConstantsFromLibrary(lib)  // Initialize constants
    result.registerConstraints()
    return result
}
```

Remove `initConstantsSingleton()` call from `MustInitLibraryCache()`.

#### Step 3: Keep Backward-Compatible Global Const

Keep `var Const *Constants` pointing to `&L(0).Constants` for backward compatibility during migration.

#### Step 4: Update All Usages (~237 locations)

| Context | Old Pattern | New Pattern |
|---------|-------------|-------------|
| Slot-aware code | `ledger.Const.X` | `L(slot).Constants.X` |
| Transaction context | `ledger.Const.X` | `tx.Library().Constants.X` |
| Genesis-only / tests | `ledger.Const.X` | `ledger.Const.X` (keep) |

**Key files:**
- `ledger/transaction/validate.go` → `ctx.Transaction.Library().Constants.X`
- `core/attacher/*.go` → `L(slot).Constants.X`
- `sequencer/**/*.go` → slot-appropriate access
- `ledger/*.go`, `api/**/*.go`, `proxi/**/*.go`

### Files to Modify

**Core:**
- `ledger/lib.go` - Add `Constants` embedded field
- `ledger/constants.go` - Remove singleton init, keep `Const` as alias
- `ledger/lib_singleton.go` - Initialize constants in `parseLibrary()`

**Usages (~42 files)** - Update to slot-aware access where appropriate

---

## Design Principles (Reference)

1. **All ledger logic in EasyFL**: Constants, inflation rules, constraints—everything deterministic is defined in EasyFL
2. **Platform independence**: Ledger rules are EasyFL programs; Go is runtime
3. **Embedded functions are minimal**: Only foundational operations in Go
4. **No deletion, only addition/replacement**: Functions can be added/replaced but never deleted
5. **Immutable legacy code**: Old embedded implementations remain forever

---

## Architecture Summary

```
Library Access:
  L(slot) → fast path: if slot >= latestUpgradeSlot, return cached latestLib (O(1))
         → slow path: linear search in cached upgradeSlots list
         → lazy loads from DB partition on first access
         → caches by upgrade slot

Genesis Flow:
  proxi init genesis → genesis.snapshot file
  node startup → detect missing DB → restore from snapshot

Upgrade Flow:
  PendingUpgrade defined in code → registered at startup → stored in DB
  Branch at/after upgrade slot → inject upgrade UTXO → new rules apply

Transaction Validation:
  Transaction.FromBytes() → caches Library via L(slot)
  validate.go → uses Transaction.Library() for all operations
```

---

## Original Design Document

_The full implementation details from Phases 1-14 are preserved below for historical reference._

[... Original content continues below ...]

---

## Current Architecture

The ledger library definitions in EasyFL (YAML format) and embedded functions are inherited from the EasyFL base library. At genesis, this library is extended with new embedded and EasyFL functions in the `upgrade0` function.

The upgraded library is saved in the ledger state at the root of the trie in YAML format. This commits the ledger definitions to the ledger state, making them immutable.

---

## Problem

Sometimes we need to add new EasyFL functions to the ledger, or even replace existing definitions. We may need to upgrade ledger rules with new UTXO constraints, embed new opcodes (such as cryptographic primitives), or fix bugs.

The current architecture makes it difficult to update ledger states saved in multi-state databases across a distributed network of nodes in a convenient, deterministic, and backward-compatible manner.

---

## Goal

Refactor the architecture to support incremental updates of the node's code while preserving the history of upgrades. Each historical transaction must be validated using the ledger rules that were applicable at its slot.

The goal is to ensure deterministic upgrades of ledger definitions across the distributed network of nodes, allowing breaking changes to ledger logic while maintaining backward compatibility.

---

## Library Upgrades: Deltas, Versions, History

### Upgrade Slots and Deltas

- Each upgrade is applied at a specific slot called the _upgrade slot_
- The upgrade slot is the **first slot where new rules apply**
- Maximum **one upgrade per slot**; however, an upgrade delta may internally consist of multiple steps (like current `upgrade0`)
- After applying the delta at the upgrade slot, the updated library applies to all subsequent slots
- The delta YAML comes from static node code, alongside embedded function code

### Upgrade Content

- Upgrades can only **add or replace** EasyFL functions (never delete)
- All deterministic constants (inflation rates, timing parameters, etc.) are defined in EasyFL
- There is no deprecation mechanism—old functions remain valid for historical validation

### Embedded Function Versioning

- Legacy embedded function code is **never modified or deleted**
- To fix bugs in embedded functions: create a new Go function and use the `embedded-as` field in YAML to map the EasyFL function name to the new implementation
- Each upgrade version has its own function resolver that returns the appropriate Go implementations
- This mapping must be thoroughly documented in code comments

Example pattern:
```go
// Original implementation (upgrade0)
func embeddedTicksBefore_v0(...) { /* original */ }

// Fixed implementation (upgrade1)
func embeddedTicksBefore_v1(...) { /* fixed */ }

// In upgrade1 YAML:
// ticksBefore:
//   embedded-as: ticksBefore_v1
```

### Storage and Persistence

- All known upgrades are stored in a dedicated DB partition as pairs: `<upgradeSlot>: <compiled library YAML>`
- Libraries are stored as **full compiled YAMLs**, not deltas (deltas may have sub-steps, adding unnecessary complexity)
- Upon startup, the node checks if upcoming upgrades are already in the DB partition; if not, it adds them
- Hashes can be calculated from the stored YAML for verification

### Library Access: `L(slot)`

- The current `L()` function becomes `L(slot)`, returning the library version applicable to that slot
- Libraries are **lazily loaded from DB and parsed** on first access
- **Recently used versions are cached** to avoid repeated parsing
- The slot→version lookup is optimized for efficiency
- No cache eviction needed—upgrades are rare; node restart naturally resets the cache

### Peering Rendezvous

- The hash of the latest upgrade library known to the node is used as a rendezvous code for peering
- This isolates upgraded nodes from non-upgraded nodes
- Forces node operators to upgrade their versions well before the upgrade slot

---

## Commitment to Upgrade History

### Upgrade UTXO

When an upgrade slot arrives, the node commits to the new library version by creating a special unspendable UTXO:

**UTXO Format:**
- Amount: `0`
- Lock: `false` (unspendable)
- Constraint 2: hash of the new library version
- Constraint 3: hash of the previous library version
- Constraint 4: previous upgrade slot (4 bytes BigEndian)

**Synthetic OutputID Format (33 bytes):**
- 5-byte timestamp: `<upgrade slot>` with `ticks = 0`, sequencer flag = 0
- 1-byte output count: `0xff` (255)
- 26-byte hash portion: big-endian representation of the upgrade slot number (zero-padded)
- 1-byte output index: `0xff` (255, reserved for synthetic UTXOs)

**No-collision guarantee:**
1. At slot 0 (genesis), only 3 outputs exist (indices 0, 1, and 2), so index 255 is impossible for real transactions
2. For non-genesis slots, the hash portion being the slot number (zero-padded) is computationally infeasible to match with a real blake2b hash (hash preimage resistance)

### Commitment Process

- When a branch transaction is produced at or after an upgrade slot, the node checks if the upgrade UTXO exists in the baseline state
- If not present, the branch **must** include the upgrade UTXO in its mutations
- This makes inclusion mandatory, preserving determinism across all nodes
- The upgrade UTXO is committed together with the branch using the new library rules

### Edge Case: No Branch at Upgrade Slot

If no branch is produced exactly at the upgrade slot (e.g., network issues):
- The first branch at slot ≥ upgrade slot commits the upgrade UTXO
- Determinism is preserved because:
  - The OutputID is derived from the upgrade slot (not the commit slot)
  - The library hash is deterministic (same delta applied to same base)
  - All nodes apply the same logic

### Verification

Each upgrade UTXO can be verified by:
1. Checking the synthetic OutputID matches the expected format for the upgrade slot
2. Checking the hash in the UTXO matches the expected library hash for that upgrade version

---

## Genesis as Snapshot

### Bootstrap Flow

```
proxi init genesis     → Creates genesis.snapshot file
node startup           → If no proximadb, find/restore from latest snapshot
```

### Genesis Snapshot Contents

- Ledger identity (genesis time + description) - embedded in slot 0 library YAML
- Upgrade library at slot 0 (full compiled YAML)
- Four genesis outputs:
  - Output #0: Initial supply minus 1 token (locked to genesis controller, chain+sequencer constraints)
  - Output #1: Genesis stem
  - Output #2: Controller mote output (1 token, ED25519 lock to genesis controller)
  - Output #255: Upgrade commitment UTXO
- Root record with initial state

### `proxi init genesis` Command

**Input:**
- Private key (from wallet or generated)

**Output:**
- `genesis.snapshot` file ready for node bootstrap

**Process:**
1. Generate ledger parameters from private key
2. Create library from parameters (upgrade0)
3. Build genesis state in memory (4 outputs + root record)
4. Serialize to snapshot format (includes upgrade library)
5. Write snapshot file

---

## Single Pending Upgrade Model

### Design Principle

At most **one pending upgrade** can exist in the codebase at any time. This simplifies:
- Code organization (no need to manage multiple pending upgrades)
- Testing (only test current → next transition)
- Node operator experience (clear single upgrade path)

### File Structure

Upgrade-related code lives directly in the `ledger/` package:

```
ledger/
├── def_upgrade.go              # UpgradeDefinition type, PendingUpgrade variable
├── def_embed.go                # EmbeddedResolver type, GetEmbeddedFunctionResolver, upgradeEmbeddedResolvers list
├── def_upgrade0.go             # Upgrade 0 (genesis) - definitions and resolver
├── def_upgradeN.go             # Future upgrade N - definitions and resolver (when needed)
└── upgrade_utxo.go             # Upgrade UTXO creation and parsing
```

**When no pending upgrade:**
- `PendingUpgrade` in `def_upgrade.go` is `nil`

**When pending upgrade exists:**
- `PendingUpgrade` is set with upgrade slot and build function
- New resolver added to `upgradeEmbeddedResolvers` in `def_embed.go`
- New `def_upgradeN.go` file contains definitions and resolver

### Upgrade Lifecycle

```
1. DEVELOPMENT: Developer adds upgrade code to ledger/
   - Set PendingUpgrade in def_upgrade.go with target slot
   - Create def_upgradeN.go with YAML definitions and resolver
   - Add resolver to upgradeEmbeddedResolvers in def_embed.go

2. DEPLOYMENT: Node operators update their nodes
   - New code includes pending upgrade
   - Upgrade registered at startup (before target slot)

3. ACTIVATION: Target slot reached
   - Upgrade UTXO injected into first branch at/after slot
   - Library stored in DB partition
   - Ledger rules change

4. POST-ACTIVATION:
   - PendingUpgrade can be set to nil
   - Upgrade data lives in DB partition
   - Resolver code remains forever (for historical validation)
```

**See `docs/upgrade.md` for detailed upgrade authoring guide.**

---

## Transaction Validation: Library Pointer Caching

### Optimization

The `Transaction` struct caches a library pointer to eliminate repeated `L(slot)` calls during parallel validation:

```go
type Transaction struct {
    tree      *tuples.Tree
    txid      base.TransactionID
    timestamp base.LedgerTime
    lib       *Library  // cached library for this transaction's slot
    // ...
}

func _baseValidation(tx *Transaction) error {
    tx.txid, err = TxIDFromTransactionDataTree(tx.tree)
    tx.timestamp = tx.txid.Timestamp()
    tx.lib = L(tx.timestamp.Slot)  // Cache library once
    return nil
}

func (tx *Transaction) Library() *Library { return tx.lib }
```

### Function Patterns

**Library-based (core implementation):**
```go
func OutputFromBytesWithLib(data []byte, lib *Library) (*Output, error)
func LockFromBytesWithLib(data []byte, lib *Library) (Lock, error)
func AmountsFromBytesWithLib(data []byte, lib *Library) (Amounts, error)
```

**Slot-based (delegates to library-based):**
```go
func OutputFromBytesAtSlot(data []byte, slot uint32) (*Output, error) {
    return OutputFromBytesWithLib(data, L(slot))
}
```

**Usage in validation:**
```go
func (ctx *TxContext) _scanOutputs(path []byte) ([]*Output, error) {
    lib := ctx.Transaction.Library()
    // ...
    ret[i], err = OutputFromBytesWithLib(data, lib)
    // ...
}
```

---

## Test Files Reference

| File | Tests |
|------|-------|
| `ledger/multistate/upgrades_test.go` | Storage layer, pending upgrade registration |
| `ledger/multistate/upgrades_cache_test.go` | L(slot) caching behavior |
| `ledger/multistate/snapshot_test.go` | Snapshot format with upgrades |
| `ledger/multistate/genesis_snapshot_test.go` | In-memory genesis creation |
| `ledger/tests/slot_aware_validation_test.go` | Slot-aware transaction validation |
| `core/core_modules/snapshot_restore/snapshot_restore_test.go` | Auto-restore logic |

---

## Documentation Files

- `ledger/multistate/snapshot_format.md` - Detailed snapshot format specification
- `docs/upgrade.md` - Upgrade lifecycle and authoring guide
- `CLAUDE.md` - Updated with upgrade architecture notes

---

_Task completed 2026-01-14, updated 2026-01-18_
