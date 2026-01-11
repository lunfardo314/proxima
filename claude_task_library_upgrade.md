# Refactor Ledger Library Upgrades

## Instructions for Claude Code

1. Critically analyze the task description, the code; ask clarifying questions; provide suggestions
2. Edit and refine this file until a clear understanding of the task is reached
3. Create an implementation plan
4. Implement changes in small commits in separate branch `develop-breaking-upgrades`
5. Implement test cases
6. Propose testing strategy for the node and testnet

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

## Design Principles

1. **All ledger logic in EasyFL**: Constants, inflation rules, constraints—everything deterministic is defined in EasyFL, not Go code
2. **Platform independence**: The ledger rules are the EasyFL program; Go is just the runtime/interpreter
3. **Embedded functions are minimal**: Only simple, foundational operations are embedded in Go
4. **No deletion, only addition/replacement**: Functions can be added or replaced but never deleted
5. **Immutable legacy code**: Old embedded function implementations remain in codebase forever for historical validation

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
- Third constraint: hash of the new library version

**Synthetic OutputID Format (33 bytes):**
- 5-byte timestamp: `<upgrade slot>` with `ticks = 0`, sequencer flag = 0
- 1-byte output count: `0xff` (255)
- 26-byte hash portion: big-endian representation of the upgrade slot number (zero-padded)
- 1-byte output index: `0xff` (255, reserved for synthetic UTXOs)

**No-collision guarantee:**
1. At slot 0 (genesis), only 2 outputs exist (indices 0 and 1), so index 255 is impossible for real transactions
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

## Code Changes Required

### 1. Library Access Function

- Modify `L()` to `L(slot)` returning the library for a specific slot
- In most cases this will be the latest version
- Implement lazy loading and caching

### 2. Trie Root Storage

- Remove ledger definitions from trie root
- Trie root contains only: _genesis time_ and _description_
- This makes the true root value immutable

### 3. Upgrade Definition in Code

- Startup code contains upcoming upgrade definitions (like current `upgrade0`)
- Each upgrade has its own function: `upgrade0`, `upgrade1`, etc.
- Each upgrade has its own embedded function resolver

### 4. Genesis State

- Genesis creates **3 UTXOs** instead of 2:
  1. Initial supply output (existing)
  2. Stem output (existing)
  3. Upgrade commitment UTXO for version 0 at slot 0

### 5. DB Partition for Upgrades

- New DB partition stores all known library versions
- Key: upgrade slot
- Value: full compiled library YAML

### 6. Snapshot Format

- Snapshots contain all past upgrade library YAMLs (full compiled versions)
- Upgrade UTXOs are part of the state and included automatically
- On restore, libraries are loaded into the DB partition

### 7. Branch Transaction Production

- When producing a branch at slot ≥ any pending upgrade slot:
  - Check if upgrade UTXO exists in baseline state
  - If not, include it in branch mutations
  - Use the new library for validation

### 8. Transaction Validation

- Attacher uses `L(txSlot)` for validation
- Historical transactions validated with their era's library
- New transactions validated with current library

### 9. API Changes

- TBD (analysis required)
- Likely: endpoint to query upgrade history, current library version, pending upgrades

### 10. Proxi CLI Tool

- Commands that interact directly with the DB need updates:
  - `proxi db` subcommands (inspect, repair, etc.)
  - `proxi snapshot` subcommands (create, restore)
  - Any commands that read/write ledger state or library definitions
- May need new commands for:
  - Querying upgrade history
  - Inspecting library versions at specific slots
  - Verifying upgrade UTXOs

---

## Open Questions

1. **Determinism verification for Q6**: Need to double-check that committing upgrade UTXO in later slots preserves determinism in all edge cases (competing branches, reorganizations)

2. **API design**: What endpoints are needed for upgrade information?

3. **Migration path**: How to migrate existing nodes/states to the new architecture?

---

## Current Implementation Reference

This section maps the design to existing code locations, enabling a cold restart of this refactoring task.

### Core Library Files

| File | Purpose | Changes Needed |
|------|---------|----------------|
| `ledger/def.go` | `LibraryFromParameters()`, `LibraryYAMLFromParameters()`, `ParseLibraryFromYAML()` | Add slot-based versioning |
| `ledger/lib_singleton.go` | Global `L()` function, `MustInitSingleton()`, `libraryGlobal` variable | Modify `L()` → `L(slot)`, add version caching |
| `ledger/def_upgrade.go` | `upgradeData` struct, `upgradeLibrary()`, `upgrade0()`, `registerConstraints()` | Add `upgrade1`, `upgrade2`, etc.; versioned resolvers |
| `ledger/def_embed.go` | Embedded function definitions and resolvers | Version embedded functions, keep legacy implementations |
| `ledger/def_init_params.go` | `InitParameters` struct | May need upgrade slot parameters |
| `ledger/def_helper_func.go` | Helper function YAML definitions | Part of upgrade deltas |
| `ledger/def_general_func.go` | General function YAML definitions | Part of upgrade deltas |
| `ledger/def_path_constants.go` | Path constant definitions | Part of upgrade deltas |

### Multistate / Storage Files

| File | Purpose | Changes Needed |
|------|---------|----------------|
| `ledger/multistate/genesis.go` | `InitStateStoreFromGlobals()`, `ScanGenesisState()`, `InitLedgerFromStore()` | Add upgrade UTXO at genesis (3rd UTXO); modify library loading |
| `ledger/multistate/state.go` | `LedgerIdentityBytesFromStore()`, `LedgerIdentityBytesFromRoot()`, `MustLedgerIdentityBytes()` | Remove library from trie root; add upgrade DB partition access |
| `ledger/multistate/roots.go` | Root record management, DB partitions | Add upgrade library partition |
| `ledger/multistate/snapshot.go` | Snapshot create/restore | Include all upgrade YAMLs in snapshot; restore to DB partition |
| `ledger/multistate/mutate.go` | State mutations | Handle upgrade UTXO mutations |
| `ledger/multistate/kvtypes.go` | `StateIndexReader` interface with `MustLedgerIdentityBytes()` | Update interface for slot-based library access |

### Transaction Validation

| File | Purpose | Changes Needed |
|------|---------|----------------|
| `ledger/transaction/validate.go` | Transaction validation using `L()` | Use `L(txSlot)` for validation |
| `core/attacher/*.go` | Attacher uses library for validation | Pass slot to library access |
| `core/workflow/*.go` | Transaction workflow | Ensure correct library version used per slot |

### Proxi CLI Commands (DB-related)

| File | Purpose | Changes Needed |
|------|---------|----------------|
| `proxi/db_cmd/db.go` | DB command group | Add upgrade-related subcommands |
| `proxi/db_cmd/get_ledger_id.go` | Get ledger identity from DB | Update for new storage location |
| `proxi/db_cmd/info.go` | DB info display | Show upgrade history |
| `proxi/db_cmd/scandb.go` | DB scanning utilities | Scan upgrade partition |
| `proxi/snapshot_cmd/snapshot.go` | Snapshot commands | Handle upgrade YAMLs |
| `proxi/snapshot_cmd/restore.go` | Restore from snapshot | Restore upgrade libraries to DB |
| `proxi/snapshot_cmd/info.go` | Snapshot info | Display upgrade info |
| `proxi/init_cmd/init_genesis_db.go` | Genesis DB initialization | Create upgrade UTXO |
| `proxi/util_cmd/util_ledger_id.go` | Ledger ID utilities | Update for slot-based libraries |
| `proxi/util_cmd/util_compile_ledger_id.go` | Compile ledger ID | May need version awareness |
| `proxi/util_cmd/util_verify_ledger_id.go` | Verify ledger ID | Verify against upgrade history |
| `proxi/glb/db.go` | DB utilities | Add upgrade partition helpers |

### Key Data Flow (Current)

1. **Genesis**: `LibraryFromParameters()` → `upgrade0()` → library YAML stored at trie root (`nil` key)
2. **Node startup**: `InitLedgerFromStore()` → `LedgerIdentityBytesFromStore()` → `MustInitSingleton()`
3. **Validation**: All code uses global `L()` → single library version
4. **Snapshots**: Library YAML included via trie root value

### Key Data Flow (Target)

1. **Genesis**: `LibraryFromParameters()` → `upgrade0()` → library stored in upgrade DB partition; upgrade UTXO created
2. **Node startup**: Load known upgrades to DB partition; `L(slot)` with lazy loading
3. **Validation**: Use `L(txSlot)` → slot-appropriate library version
4. **Branch production**: Check for pending upgrades, create upgrade UTXOs as needed
5. **Snapshots**: All upgrade YAMLs stored separately in snapshot file

---

## Documentation Requirements

### New Documentation to Write

1. **Upgrade System Architecture** (`docs/upgrades.md` or in main docs site)
   - How upgrades work (slots, deltas, UTXOs)
   - Node operator guide for handling upgrades
   - Timeline expectations before upgrade slots

2. **Embedded Function Versioning Guide** (developer docs)
   - How to add new embedded functions
   - How to fix bugs in existing embedded functions using `embedded-as`
   - Version naming conventions

3. **Upgrade UTXO Specification** (technical spec)
   - Synthetic OutputID format
   - UTXO structure and constraints
   - Verification procedures

4. **Proxi CLI Upgrade Commands** (user guide)
   - New commands for querying upgrade history
   - Commands for verifying upgrade state

### Existing Documentation to Update

1. **CLAUDE.md** - Add upgrade-related architecture notes
2. **Main documentation site** (`lunfardo314.github.io`) - Ledger model section
3. **API documentation** - New endpoints for upgrade info
4. **Snapshot format documentation** - New fields for upgrade YAMLs

---

## Implementation Phases

**Decisions made:**
- Storage layer first, then L(slot)
- No migration path - clean testnet reset
- Unit tests with increasing coverage/complexity
- Small commits

---

### Phase 1: Storage Layer (DB Partition for Upgrades)

**Status:** ✅ Complete (fe98f945)

**Goal:** Create DB partition to store compiled library YAMLs keyed by upgrade slot.

**Tasks:**
- [x] 1.1 Add new DB partition constant in `multistate/roots.go`
- [x] 1.2 Create read/write functions for upgrade library records
- [x] 1.3 Create accessor functions: `GetLibraryForSlot()`, `WriteLibrary()`, `IterateLibraries()`
- [x] 1.4 Unit tests for storage layer

**Files to modify:**
- `ledger/multistate/roots.go` - Add partition, read/write functions
- `ledger/multistate/upgrades.go` - New file for upgrade storage logic
- `ledger/multistate/upgrades_test.go` - Unit tests

---

### Phase 2: L(slot) with Caching

**Status:** ✅ Complete

**Goal:** Modify library access to be slot-aware with lazy loading and caching.

**Tasks:**
- [x] 2.1 Add `LibraryCache` structure with slot→library mapping
- [x] 2.2 Modify `L()` → `L(slot)` signature
- [x] 2.3 Implement lazy loading from DB partition
- [x] 2.4 Add cache for recently used library versions
- [x] 2.5 Update all `L()` call sites to pass slot parameter
- [x] 2.6 Unit tests for caching behavior

**Files modified:**
- `ledger/lib_singleton.go` - Added `LibraryCache`, `L(slot)`, `ResolverFactory`, `RegisterResolverForUpgrade()`
- `ledger/multistate/upgrades_cache_test.go` - New file with cache unit tests
- 36+ files across ledger, api, node, proxi packages - Updated `L()` to `L(base.MaxSlot)` with optimized local caching

**Implementation notes:**
- `L(slot)` finds the latest upgrade slot <= requested slot
- Libraries are cached by upgrade slot (not query slot) for efficiency
- `base.MaxSlot` (0xFFFFFFFF) is used as sentinel for "latest library"
- Each upgrade slot must have a registered `ResolverFactory` for its embedded functions
- Backward compatibility maintained via `MustInitSingleton()` for existing tests

---

### Phase 3: Genesis Changes

**Status:** ✅ Complete

**Goal:** Remove library from trie root; add upgrade UTXO at genesis.

**Tasks:**
- [x] 3.1 Modify `CommitEmptyRootWithLedgerIdentity()` to store only genesis time + description
- [x] 3.2 Store library in upgrade DB partition at genesis (slot 0)
- [x] 3.3 Create upgrade commitment UTXO (3rd genesis UTXO)
- [x] 3.4 Define synthetic OutputID format for upgrade UTXOs
- [x] 3.5 Update `ScanGenesisState()` to load library from DB partition
- [x] 3.6 Update snapshot restore to use new identity format

**Files created/modified:**
- `ledger/ledger_identity.go` - New file for minimal identity data structure
- `ledger/upgrade_utxo.go` - New file for upgrade UTXO functions
- `ledger/base/upgrade_output_id.go` - New file for synthetic OutputID format
- `ledger/multistate/genesis.go` - Modified for new genesis format
- `ledger/lib.go` - Renamed `IdentityData()` to `DefinitionsYAML()`
- `ledger/output.go` - Added `CloneRaw()` for unvalidated cloning
- `ledger/multistate/mutate.go` - Added `InsertAddOutputMutationRaw()`, skip upgrade UTXOs in account indexing
- `core/core_modules/state_cleanup/restore.go` - Updated for new identity format
- `proxi/snapshot_cmd/restore.go` - Updated for new identity format

**Implementation notes:**
- Trie root now stores only `LedgerIdentity` (genesis time + description)
- Library YAML stored in upgrade DB partition at slot 0
- Upgrade UTXO uses synthetic OutputID with index 255 (collision-free)
- Upgrade UTXO has empty inline data lock (unspendable)
- Genesis now creates 3 outputs: initial supply (0), stem (1), upgrade (255)

---

### Phase 4: Upgrade UTXO Mechanics

**Status:** ✅ Complete (merged with Phase 3)

**Goal:** Implement upgrade UTXO creation and verification.

**Tasks:**
- [x] 4.1 Define upgrade UTXO constraint format (amount=0, lock=false, hash constraint)
- [x] 4.2 Implement synthetic OutputID generation for upgrade slots (`base.UpgradeOutputID()`)
- [x] 4.3 Implement upgrade UTXO verification (`ParseUpgradeUTXO()`, `VerifyUpgradeUTXO()`)
- [x] 4.4 Synthetic OutputID validation (`IsUpgradeOutputID()`, `UpgradeSlotFromOutputID()`)

**Files created:**
- `ledger/base/upgrade_output_id.go` - Synthetic OutputID functions
- `ledger/upgrade_utxo.go` - Upgrade UTXO creation and parsing

---

### Phase 5: Branch Production Integration

**Status:** ⏳ Pending

**Goal:** Inject upgrade UTXOs during branch production when crossing upgrade slots.

**Tasks:**
- [ ] 5.1 Check for pending upgrades during branch production
- [ ] 5.2 Inject upgrade UTXO into branch mutations if needed
- [ ] 5.3 Use correct library version for validation
- [ ] 5.4 Integration tests

**Files to modify:**
- `core/attacher/` - Branch production
- `ledger/multistate/mutate.go`

---

### Phase 6: Snapshot Format

**Status:** ⏳ Pending

**Goal:** Include upgrade library YAMLs in snapshots.

**Tasks:**
- [ ] 6.1 Add upgrade libraries section to snapshot format
- [ ] 6.2 Update snapshot creation to include all upgrade YAMLs
- [ ] 6.3 Update snapshot restore to populate DB partition
- [ ] 6.4 Unit tests for snapshot round-trip

**Files to modify:**
- `ledger/multistate/snapshot.go`

---

### Phase 7: Transaction Validation

**Status:** ⏳ Pending

**Goal:** Use slot-appropriate library version for transaction validation.

**Tasks:**
- [ ] 7.1 Pass slot to library access in validation code
- [ ] 7.2 Ensure historical transactions use correct library version
- [ ] 7.3 Integration tests with multiple library versions

**Files to modify:**
- `ledger/transaction/validate.go`
- `core/attacher/*.go`

---

### Phase 8: API and CLI

**Status:** ⏳ Pending

**Goal:** Expose upgrade information through API and CLI.

**Tasks:**
- [ ] 8.1 API endpoint for upgrade history
- [ ] 8.2 API endpoint for current library version
- [ ] 8.3 Proxi CLI commands for upgrade info
- [ ] 8.4 Update existing CLI commands for new storage

**Files to modify:**
- `api/server/server.go`
- `proxi/db_cmd/`
- `proxi/util_cmd/`

---

## Session State

_Track current progress here between sessions._

**Current Phase:** 2 Complete, Ready for Phase 3
**Current Task:** Phase 2 fully implemented and tested
**Last Commit:** Phase 2 transaction validation changes
**Notes:**
- Phase 2 fully complete:
  - `LibraryCache` structure with slot→library mapping in `lib_singleton.go`
  - `L(slot)` function with lazy loading and caching
  - All call sites updated to pass `base.MaxSlot` for latest library
  - Added slot-aware versions of all parsing functions:
    - Core: `ConstraintFromBytesAtSlot`, `LockFromBytesAtSlot`, `AccountableFromBytesAtSlot`
    - Output: `OutputFromBytesAtSlot`, `OutputFromBytesMainAtSlot`, `OutputFromHexStringAtSlot`
    - Constraints: All `*FromBytesAtSlot` variants for every constraint type
  - Original functions remain as wrappers calling slot-aware versions with `base.MaxSlot`
  - Transaction validation context fully slot-aware:
    - `makeEvalContext()` uses `ledger.L(ctx.Slot())`
    - `_evalBytecode()` uses `ledger.L(ctx.Slot())`
    - `constraintName()` converted to method on TxContext using slot
    - `runOutput()` uses slot-aware decompilation
  - All ledger and multistate tests passing
- Display/decompilation functions:
  - Using `base.MaxSlot` is acceptable for display purposes (human-readable output)
  - The latest library should be backward compatible for decompiling older bytecode
  - Critical validation path uses correct slot; display is informational only
- Ready for Phase 3: Genesis Changes
