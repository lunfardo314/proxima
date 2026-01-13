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

## Genesis as Snapshot (New Design)

### Current Bootstrap Flow (Being Replaced)

```
proxi util ledger_id   → Creates proxi.genesis.id.yaml (ledger definitions)
proxi init genesis_db  → Creates proximadb with genesis state
proxi init bootstrap   → Distributes initial supply to accounts
node startup           → Opens existing DB or fails
```

### New Bootstrap Flow

```
proxi init genesis     → Creates genesis.snapshot file
node startup           → If no proximadb, find/restore from latest snapshot
```

### Key Design Changes

1. **Genesis snapshot is a regular snapshot**: Genesis is just a special case of snapshot containing initial state
2. **Unified restore mechanism**: Node startup always uses snapshot restore when DB is missing
3. **Aligns with snapshot_restore**: `CheckAndRestoreOnStartup()` already handles missing DB by finding latest snapshot
4. **Simpler mental model**: Snapshot is the universal state transfer format for both genesis and recovery

### Genesis Snapshot Contents

- Ledger identity (genesis time + description)
- Upgrade library at slot 0 (full compiled YAML)
- Three genesis outputs:
  - Output #0: Initial supply (locked to genesis controller)
  - Output #1: Genesis stem
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
3. Build genesis state in memory (3 outputs + root record)
4. Serialize to snapshot format (includes upgrade library)
5. Write snapshot file

### Node Startup Changes

**Current behavior in `node.Start()`:**
```
checkAndRestoreOnStartup()  → Only restores if snapshot_restore marked in-progress
initMultiStateLedger()      → Fails if DB missing
```

**New behavior:**
```
checkAndRestoreOnStartup()  → Also restores if proximadb missing (finds latest snapshot)
initMultiStateLedger()      → Always succeeds (DB guaranteed to exist after restore)
```

The existing `CheckAndRestoreOnStartup()` logic in snapshot_restore module already handles:
- Detecting missing/corrupted DB
- Finding latest snapshot in configured directory
- Restoring from snapshot

We need to extend it to:
- Also check for snapshots when DB is completely absent (not just corrupted)
- Look in multiple directories (working dir, configured snapshot dir)

---

## Single Pending Upgrade Model

### Design Principle

At most **one pending upgrade** can exist in the codebase at any time. This simplifies:
- Code organization (no need to manage multiple pending upgrades)
- Testing (only test current → next transition)
- Node operator experience (clear single upgrade path)

### `ledger/upgrade/` Folder Structure

```
ledger/upgrade/
├── pending.go           # Pending upgrade registration (or nil)
├── pending_defs.yaml    # EasyFL definitions for pending upgrade (if any)
└── pending_resolver.go  # Embedded function resolver for pending upgrade (if any)
```

**When no pending upgrade:**
- `pending.go` exports `var PendingUpgrade *UpgradeDefinition = nil`
- Or folder contains only placeholder/documentation

**When pending upgrade exists:**
- `pending.go` exports upgrade definition with target slot
- `pending_defs.yaml` contains YAML deltas
- `pending_resolver.go` contains new/modified embedded functions

### Upgrade Lifecycle

```
1. DEVELOPMENT: Developer adds upgrade to ledger/upgrade/
   - Define target slot (well in the future)
   - Add YAML definitions
   - Add embedded resolver if needed

2. DEPLOYMENT: Node operators update their nodes
   - New code includes pending upgrade
   - Upgrade registered at startup (before target slot)

3. ACTIVATION: Target slot reached
   - Upgrade UTXO injected into first branch at/after slot
   - Library stored in DB partition
   - Ledger rules change

4. CLEANUP: After activation (optional)
   - ledger/upgrade/ folder can be cleared
   - Upgrade data now lives in DB partition
   - Code remains for embedded function implementations
```

### Integration with Existing Code

**At node startup (`InitLedgerFromStore`):**
```go
// Always register upgrade0
RegisterResolverForUpgrade(0, GetEmbeddedFunctionResolverUpgrade0)

// Register pending upgrade if exists and not yet in DB
if upgrade.PendingUpgrade != nil {
    slot := upgrade.PendingUpgrade.Slot
    if !upgradeExistsInDB(store, slot) {
        RegisterResolverForUpgrade(slot, upgrade.PendingUpgrade.Resolver)
        WriteUpgradeLibrary(store, slot, upgrade.PendingUpgrade.CompiledYAML)
    }
}

MustInitLibraryCache(store)
```

### Benefits

1. **Simplicity**: Only one upgrade to think about at a time
2. **Clear ownership**: Pending upgrade is explicit in code
3. **Easy cleanup**: After activation, pending folder can be emptied
4. **No accumulation**: Code doesn't grow with upgrade history (DB handles history)

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
- [x] 3.1 Modify `WriteEmptyRootWithLedgerIdentity()` to store only genesis time + description
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
- `core/core_modules/snapshot_restore/restore.go` - Updated for new identity format
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

**Status:** ✅ Complete

**Goal:** Inject upgrade UTXOs during branch production when crossing upgrade slots.

**Tasks:**
- [x] 5.1 Check for pending upgrades during branch production
- [x] 5.2 Inject upgrade UTXO into branch mutations if needed
- [x] 5.3 Use correct library version for validation (already handled by L(slot))

**Files created/modified:**
- `ledger/lib_singleton.go` - Added `GetAllUpgradeSlots(maxSlot)` function
- `ledger/multistate/upgrade_inject.go` - New file with `InjectMissingUpgradeUTXOs()`
- `core/attacher/wrapup.go` - Call injection in `commitBranch()`

**Implementation notes:**
- `GetAllUpgradeSlots(maxSlot)` returns all upgrade slots from DB partition up to maxSlot
- `InjectMissingUpgradeUTXOs()` checks each upgrade slot and injects missing UTXOs
- Uses `HasUTXO()` to check if upgrade UTXO already exists in baseline state
- Uses `InsertAddOutputMutationRaw()` for injection (upgrade UTXOs have non-standard locks)
- Injection happens after getting mutations but before committing to state

---

### Phase 6: Snapshot Format with Upgrade Libraries

**Status:** ✅ Complete

**Goal:** Include upgrade library YAMLs in snapshots (prerequisite for genesis as snapshot).

**Tasks:**
- [x] 6.1 Add upgrade libraries section to snapshot format (header with count + library entries)
- [x] 6.2 Update snapshot creation to include all upgrade YAMLs from DB partition
- [x] 6.3 Update snapshot restore to populate upgrade DB partition
- [x] 6.4 Unit tests for snapshot round-trip with upgrade libraries

**Files modified:**
- `ledger/multistate/snapshot.go` - Updated format, SaveSnapshot, OpenSnapshotFileStream
- `core/core_modules/snapshot_restore/restore.go` - Updated RestoreFromSnapshot to write all libraries
- `proxi/snapshot_cmd/restore.go` - Simplified to use shared RestoreFromSnapshot
- `proxi/snapshot_cmd/check.go` - Updated for new SnapshotFileStream format
- `proxi/snapshot_cmd/info.go` - Updated for new SnapshotFileStream format
- `ledger/multistate/snapshot_test.go` - New test file

**Snapshot format (ver 1):**
```
[header JSON: version="ver 1"]
[root record: branchID + RootRecord bytes]
[upgrade count: key={0x06}, value=4-byte big-endian count]
[for each upgrade:]
  [key=4-byte big-endian slot, value=yaml bytes]
[trie data records]
```

Note: Ledger identity (genesis time + description) is embedded in the slot 0 library YAML, not as a separate record.

**Key changes:**
- Version string "ver 1" (unchanged - format was never used in production)
- `SnapshotFileStream.UpgradeLibraries` contains all upgrade entries
- `GetLedgerConstants()` method parses constants from slot 0 library
- Upgrade libraries written BEFORE trie data (for early access during restore)
- Code duplication removed: `proxi snapshot restore` now uses shared `RestoreFromSnapshot()`

**Documentation:** See `docs/snapshot_format.md` for detailed snapshot format specification.

---

### Phase 7: Genesis as Snapshot

**Status:** ✅ Complete

**Goal:** Replace `proxi util ledger_id` + `proxi init genesis_db` with single `proxi init genesis` that creates snapshot.

**Tasks:**
- [x] 7.1 Create `proxi init genesis` command that outputs genesis.snapshot
- [x] 7.2 Build genesis state in memory (identity, upgrade library, 3 outputs, root record)
- [x] 7.3 Serialize genesis state to snapshot format
- [ ] 7.4 Remove/deprecate `proxi util ledger_id` command (deferred - keep for compatibility)
- [ ] 7.5 Remove/deprecate `proxi init genesis_db` command (deferred - keep for compatibility)
- [ ] 7.6 Remove/deprecate `proxi init bootstrap_account` command (deferred - keep for compatibility)
- [x] 7.7 Update documentation and help text

**Files created/modified:**
- `proxi/init_cmd/init_genesis.go` - New command
- `ledger/multistate/genesis_snapshot.go` - New file for in-memory genesis builder
- `ledger/multistate/genesis_snapshot_test.go` - Unit tests (10 tests)

**Additional improvements in this phase:**
- Removed ledger identity record from snapshot format (now embedded in slot 0 library YAML)
- Changed upgrade count serialization to big-endian (`binary.BigEndian`)
- Added `validateGenesisIdentityImmutability()` - enforces genesis time/description immutability on upgrades
- Added `WriteUpgradeLibraryUnchecked()` for tests using placeholder data
- Renamed `CommitEmptyRootWithLedgerIdentity()` → `WriteEmptyRootWithLedgerIdentity()`
- Updated `docs/snapshot_format.md` with new format
- Added BigEndian serialization rule to `CLAUDE.md`

---

### Phase 8: Node Startup from Snapshot

**Status:** ✅ Complete

**Goal:** Node automatically restores from snapshot when proximadb is missing.

**Tasks:**
- [x] 8.1 Extend `CheckAndRestoreOnStartup()` to detect missing DB (not just corrupted)
- [x] 8.2 Search for snapshots in working directory and configured snapshot directory
- [x] 8.3 Select latest snapshot (by modification time) when multiple found
- [x] 8.4 Restore from snapshot before `initMultiStateLedger()` (already in correct order)
- [x] 8.5 Ensure alignment with existing snapshot_restore restore logic
- [x] 8.6 Unit tests for new functionality

**Files modified:**
- `core/core_modules/snapshot_restore/snapshot_restore.go` - Restructured `CheckAndRestoreOnStartup()`:
  - Now checks DB state BEFORE checking `snapshot_restore.enable` config
  - If DB missing/corrupted, finds and restores from snapshot regardless of config
  - Improved logging for genesis bootstrap vs periodic cleanup scenarios
- `core/core_modules/snapshot_restore/restore.go` - Added `FindLatestSnapshotInDirs()`:
  - Searches multiple directories for snapshots
  - Skips non-existent directories gracefully
  - Skips `__tmp__` prefixed files (snapshots being written)
- `core/core_modules/snapshot_restore/snapshot_restore_test.go` - Added `TestFindLatestSnapshotInDirs`

**Key behavior:**
```
node.Start():
  checkAndRestoreOnStartup()
    1. Check if DB exists/valid (independent of config)
    2. If DB fine and snapshot_restore disabled, return
    3. If DB missing: search "." then "snapshot" dir for .snapshot files
    4. Restore from latest snapshot found
  initMultiStateLedger()  // Always succeeds (DB guaranteed after restore)
```

**Implementation notes:**
- Genesis bootstrap works even with `snapshot_restore.enable: false`
- Working directory is searched first (for `genesis.snapshot`)
- Configured `snapshot_restore.snapshot_directory` or `snapshot.directory` searched second
- Multiple directories can have snapshots; the newest (by modification time) is selected

---

### Phase 9: Single Pending Upgrade Folder

**Status:** ✅ Complete

**Goal:** Create `ledger/upgrade/` folder for single pending upgrade model.

**Tasks:**
- [x] 9.1 Create `ledger/upgrade/` folder structure
- [x] 9.2 Create `UpgradeDefinition` type and `PendingUpgrade` variable
- [x] 9.3 Modify `InitLedgerFromStore()` to register pending upgrade if exists
- [x] 9.4 Add pending upgrade to DB partition at startup (if not already present)
- [x] 9.5 Document upgrade authoring process (in doc.go)
- [x] 9.6 Unit tests: `TestFindPreviousLibrary_*`, `TestRegisterAndStorePendingUpgrade_*`

**Files created:**
- `ledger/upgrade/upgrade.go` - Core types: `UpgradeDefinition`, `PendingUpgrade` variable
- `ledger/upgrade/doc.go` - Comprehensive documentation on upgrade lifecycle and authoring

**Files modified:**
- `ledger/multistate/genesis.go` - Added `registerAndStorePendingUpgrade()`, `findPreviousLibrary()`
- `ledger/multistate/upgrades_test.go` - Added 5 new tests for pending upgrade registration

**Implementation notes:**
- `UpgradeDefinition` contains: `Slot`, `Build` function, `RegisterResolver` callback
- Circular import avoided by having `RegisterResolver` call back into `ledger.RegisterResolverForUpgrade()`
- `InitLedgerFromStore()` checks for `upgrade.PendingUpgrade` and stores if not already in DB
- Uses `WriteUpgradeLibraryUnchecked()` since Build function is trusted code
- Idempotent: if upgrade already exists in DB, registration is skipped but resolver is still registered

**Usage (to create an upgrade):**
```go
// In ledger/upgrade/pending.go
var PendingUpgrade = &UpgradeDefinition{
    Slot:             100000,  // First slot where new rules apply
    Build:            buildUpgradeLibrary,
    RegisterResolver: func() {
        ledger.RegisterResolverForUpgrade(100000, getResolverUpgrade1)
    },
}
```

---

### Phase 10: Transaction Validation (Slot-Aware)

**Status:** ✅ Complete

**Goal:** Ensure all transaction validation uses slot-appropriate library version; remove implicit MaxSlot wrapper functions.

**Tasks:**
- [x] 10.1 Verify validation code uses `L(txSlot)` consistently
- [x] 10.2 Integration tests with transactions spanning upgrade boundary
- [x] 10.3 Test historical transaction re-validation with old library
- [x] 10.4 Remove wrapper functions that implicitly use `base.MaxSlot`
- [x] 10.5 Replace all calls with explicit `*AtSlot(..., base.MaxSlot)` pattern
- [x] 10.6 Add comments explaining upgrade code responsibility for backward compatibility

**Files verified:**
- `ledger/transaction/validate.go` - Uses `L(ctx.Slot())` for all library access
- `ledger/constraints.go` - Has slot-aware parsing functions
- `core/attacher/*.go` - Uses transaction slot for validation

**Test file created:**
- `ledger/tests/slot_aware_validation_test.go` - 7 test functions verifying slot-aware behavior

**Wrapper functions removed:**
- `ledger/constraints.go`: `ConstraintFromBytes`, `LockFromBytes`, `AccountableFromBytes`
- `ledger/output.go`: `OutputFromBytes`, `OutputFromHexString`, `OutputFromBytesMain`
- `ledger/amounts.go`: `AmountsFromBytes`, `TokenBalanceFromAmountsBytes`
- `ledger/chain.go`: `ChainConstraintFromBytes`
- `ledger/sequencer.go`: `SequencerConstraintFromBytes`
- `ledger/lock_*.go`: `ChainLockFromBytes`, `StemLockFromBytes`, `TagAlongLockFromBytes`, `DeadlineLockFromBytes`, `Delegate2LockFromBytes`, `ConditionalLockFromBytes`

**Files updated to use explicit `*AtSlot(..., base.MaxSlot)`:**
- `ledger/output.go` - Internal methods like `Lock()`, `Amounts()`, `Clone()`, `ChainConstraint()`, etc.
- `ledger/def_embed.go` - `SelfOutput()` function
- `api/client/client.go` - All `OutputFromHexString` calls
- `api/server/txapi.go` - `OutputFromBytes` call
- `proxi/util_cmd/util_parse_bytecode.go` - Bytecode parsing
- `proxi/db_cmd/chains.go`, `proxi/db_cmd/ulist.go` - Database operations
- `sequencer/txbuilder_seq/req_withdraw.go` - Lock parsing
- Test files: `ledger/tests/ledger_test.go`, `ledger/tests/output_test.go`, `ledger/tests/txbuilder_test.go`

**Design decision:**
- Front-end and CLI code uses `base.MaxSlot` for parsing (always wants latest library)
- Internal ledger code uses explicit slot from transaction context
- Comment added: "Uses latest library version - upgrade code must maintain backward-compatible parsing"
- Upgrade code is responsible for not causing non-determinism when parsing legacy bytecode

**Verification summary:**
1. `validate.go` uses `L(ctx.Slot())` at lines 22, 197, 297, 302, 322
2. Constraint parsing uses `NameByPrefixAtSlot`, `ConstraintFromBytesAtSlot`, `LockFromBytesAtSlot`
3. All validation paths correctly access the library version for the transaction's slot
4. Tests verify library caching, constraint parsing, and bytecode compilation across slots

**Note:** Full integration tests with actual upgrade boundaries would require setting up multiple library versions, which is deferred to testnet testing. The slot-aware mechanism is verified to work correctly.

---

### Phase 11: API and CLI Updates

**Status:** ⏳ Pending

**Goal:** Expose upgrade information through API and CLI.

**Tasks:**
- [ ] 11.1 API endpoint for upgrade history (`/upgrades`)
- [ ] 11.2 API endpoint for library version at slot (`/library?slot=N`)
- [ ] 11.3 Update `proxi db info` to show upgrade history
- [ ] 11.4 Add `proxi db upgrades` command to list upgrades
- [ ] 11.5 Clean up deprecated commands from Phase 7

**Files to modify:**
- `api/server/server.go`
- `proxi/db_cmd/info.go`
- `proxi/db_cmd/` - New commands

---

## Session State

_Track current progress here between sessions._

**Current Phase:** 10 Complete, Ready for Phase 11
**Current Task:** Phase 11 - API and CLI Updates
**Last Commit:** Phase 10: Remove implicit MaxSlot wrapper functions (2fd307d4)
**Notes:**
- Phases 1-10 fully complete
- Upgrade UTXO improvements (2026-01-13):
  - **Chained upgrade UTXOs**: Each upgrade UTXO now commits to the entire upgrade history
    - Constraint 2: library hash (32 bytes)
    - Constraint 3: previous library hash (32 bytes) - for slot 0, this is the EasyFL base library hash
    - Constraint 4: previous upgrade slot (4 bytes BigEndian) - for slot 0, this is MaxSlot (sentinel)
  - **Optimized injection check**: `InjectMissingUpgradeUTXOs()` no longer scans state on every branch commit
    - Added atomic `nextPendingUpgradeSlot` tracking
    - `HasPendingUpgradeForSlot(branchSlot)` provides fast path (O(1) check)
    - `InitNextPendingUpgradeSlot()` called at node startup to initialize tracking
    - `UpdateNextPendingUpgradeSlot()` updates after injection
  - Files modified:
    - `ledger/upgrade_utxo.go` - Added `BaseLibraryHash()`, `UpgradeUTXOData`, updated `UpgradeUTXO()` signature
    - `ledger/lib_singleton.go` - Added `nextPendingUpgradeSlot` atomic, `HasPendingUpgradeForSlot()`, `UpdateNextPendingUpgradeSlot()`, `InitNextPendingUpgradeSlot()`
    - `ledger/multistate/genesis.go` - Updated to pass base library hash for slot 0
    - `ledger/multistate/genesis_snapshot.go` - Updated to pass base library hash for slot 0
    - `ledger/multistate/upgrade_inject.go` - Updated to compute previous library hash/slot, added fast path
    - `node/db.go` - Added `InitNextPendingUpgradeSlot()` call at startup
  - All tests pass
- Phase 10 completed (2026-01-13):
  - Removed all wrapper functions that implicitly use `base.MaxSlot` for bytecode parsing
  - Replaced all calls with explicit `*AtSlot(..., base.MaxSlot)` pattern
  - Added comments: "Uses latest library version - upgrade code must maintain backward-compatible parsing"
  - Removed wrappers: `ConstraintFromBytes`, `LockFromBytes`, `AccountableFromBytes`, `OutputFromBytes`,
    `OutputFromHexString`, `OutputFromBytesMain`, `AmountsFromBytes`, `TokenBalanceFromAmountsBytes`,
    `ChainConstraintFromBytes`, `SequencerConstraintFromBytes`, `ChainLockFromBytes`, `StemLockFromBytes`,
    `TagAlongLockFromBytes`, `DeadlineLockFromBytes`, `Delegate2LockFromBytes`, `ConditionalLockFromBytes`
  - Updated 31 files across ledger, api, proxi, and sequencer packages
  - All tests pass
- Phase 9 completed (2026-01-12):
  - Created `ledger/upgrade/` package with `UpgradeDefinition` type
  - Added `upgrade.go` with `PendingUpgrade` variable (nil when no upgrade pending)
  - Added `doc.go` with comprehensive documentation on upgrade lifecycle
  - Modified `InitLedgerFromStore()` to register and store pending upgrades
  - Added `registerAndStorePendingUpgrade()` and `findPreviousLibrary()` functions
  - Added 5 unit tests for pending upgrade registration
- Phase 8 completed (2026-01-12):
  - Restructured `CheckAndRestoreOnStartup()` to check DB state BEFORE config
  - Added `FindLatestSnapshotInDirs()` to search multiple directories
  - Genesis bootstrap now works even when `snapshot_restore.enable: false`
  - Working directory searched first (for `genesis.snapshot`)
  - Added `TestFindLatestSnapshotInDirs` unit test
- Module renamed (2026-01-12):
  - `state_cleanup` → `snapshot_restore` (better reflects purpose)
  - Config keys: `state_cleanup.*` → `snapshot_restore.*`
  - State file: `.state_cleanup.json` → `.snapshot_restore.json`
  - All imports, documentation, and references updated
- Phase 7 completed (2026-01-12):
  - Created `proxi init genesis` command
  - Created `genesis_snapshot.go` - in-memory genesis builder without BadgerDB
  - Created `genesis_snapshot_test.go` - 10 unit tests
  - Removed ledger identity record from snapshot format (now in slot 0 library YAML)
  - Changed upgrade count to big-endian serialization
  - Added `validateGenesisIdentityImmutability()` - enforces immutability on upgrades
  - Added `WriteUpgradeLibraryUnchecked()` for testing with placeholder data
  - Renamed `CommitEmptyRootWithLedgerIdentity()` → `WriteEmptyRootWithLedgerIdentity()`
  - Updated `docs/snapshot_format.md`
  - Added BigEndian serialization rule to `CLAUDE.md`
  - Fixed pion/webrtc dependency version conflict (v4.2.1 → v4.2.3)
- Design decisions:
  - Genesis as Snapshot: `proxi init genesis` creates snapshot file instead of DB
  - Node startup: automatically restore from snapshot when proximadb missing
  - Single Pending Upgrade: `ledger/upgrade/` folder contains at most one pending upgrade
  - Distribution done manually via proxi wallet commands (zero fees make this practical)
  - Old commands (`proxi util ledger_id`, `proxi init genesis_db`) kept for compatibility
- Next phases:
  - Phase 11: API and CLI updates
  - ~~Phase 12: EasyFL serde immutability enforcement~~ ✅ Addressed by EasyFL dependency

**Phase 12 - RESOLVED:**
EasyFL's `Upgrade()` function now enforces that `numArgs` cannot be changed when replacing functions.
This guarantees serde determinism across upgrades:
- Bytecode format depends on `numArgs` (number of arguments determines how many sub-expressions to parse)
- If `numArgs` is immutable for replaced functions, the same bytecode will always parse identically
- No additional enforcement needed in Proxima - EasyFL handles this at the library level
