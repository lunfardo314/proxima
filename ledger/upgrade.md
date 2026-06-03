# Proxima Ledger Upgrade Guide

This document describes how to create and deploy ledger library upgrades in Proxima.

## Overview

Proxima supports incremental upgrades to its ledger library (EasyFL definitions and embedded functions). Each upgrade:

- Activates at a specific **upgrade slot** (the first slot where new rules apply)
- Is deterministic across all nodes
- Maintains backward compatibility for historical transaction validation
- Creates an **upgrade UTXO** committed to the ledger state, forming a hash chain of all upgrades

## Upgrade Lifecycle

```
1. DEVELOPMENT   → Add upgrade code to ledger/
2. DEPLOYMENT    → Node operators update their nodes (before upgrade slot)
3. ACTIVATION    → Target slot reached, upgrade UTXO injected
4. VERIFICATION  → All nodes use new rules from upgrade slot onwards
```

## Key Data Structures

### UpgradeChainData

Each library version carries chain data that links it to its predecessor, forming a hash chain commitment across the entire upgrade history:

```go
// ledger/lib.go
type UpgradeChainData struct {
    UpgradeSlot     uint32   // The slot this library was upgraded at
    LibraryHash     [32]byte // Hash of this library
    PrevLibraryHash [32]byte // Hash of the previous library (BaseLibraryHash for slot 0)
    PrevUpgradeSlot uint32   // Slot of the previous upgrade (MaxSlot for slot 0)
}
```

This structure is cached in each `Library` instance and used for:
- Verifying upgrade UTXO correctness
- Traversing the upgrade history
- Displaying upgrade chain in CLI (`proxi db upgrades`)

### Upgrade UTXO

Each upgrade is committed to the ledger state via an unspendable UTXO with 6 elements:

| Index | Content | Description |
|-------|---------|-------------|
| 0 | Amount: `0` | No tokens (unspendable) |
| 1 | Index-values tuple | Standard UTXO indexing slot (empty here) |
| 2 | Lock: empty inline data | Evaluates to `false` (unspendable) |
| 3 | Library hash (32 bytes) | Hash of the new compiled library |
| 4 | Previous library hash (32 bytes) | Hash of the predecessor library |
| 5 | Previous upgrade slot (4 bytes BigEndian) | Slot of the predecessor upgrade (`MaxSlot` for slot 0) |

`ParseUpgradeUTXO` requires at least 6 elements and reads the two hashes and the slot from
indices 3, 4 and 5. The synthetic OutputID format is defined in
`ledger/base/upgrade_output_id.go`.

## Creating an Upgrade

### Step 1: Define the Upgrade Slot

Edit `ledger/def_upgrade.go` and set the `PendingUpgrade` variable:

```go
var PendingUpgrade = &UpgradeDefinition{
    Slot:  100000,  // First slot where new rules apply
    Build: buildUpgradeNLibrary,
}
```

**Important:** Choose a slot far enough in the future to allow all node operators to update. There is a minimum spacing of `MinSlotsBetweenUpgrades` (100) slots between consecutive upgrades, enforced by `WriteUpgradeLibrary` in `multistate/upgrades.go`.

### Step 2: Create JSON Definitions

Create JSON definition files in `ledger/def/` (following the existing pattern `def/def_*.json`):

```json
{
  "functions": [
    {
      "sym": "myFunction",
      "description": "description of what this function does",
      "numArgs": 2,
      "source": "add($0, $1)"
    }
  ]
}
```

For functions requiring Go implementations (embedded functions):

```json
{
  "functions": [
    {
      "sym": "myEmbeddedFunc",
      "description": "description",
      "numArgs": 3,
      "embeddedAs": "evalMyEmbeddedFunc"
    }
  ]
}
```

### Step 3: Create Upgrade Go File (if using embedded functions)

Create `ledger/def_upgradeN.go` (following the existing pattern of `def_upgrade0.go`):

```go
package ledger

import (
    _ "embed"
    "github.com/lunfardo314/easyfl"
)

//go:embed def/def_upgradeN.json
var _upgradeNDefsJSON []byte

// resolveEmbeddedUpgradeN resolves embedded functions from this upgrade.
// Returns nil if the symbol is not from this upgrade.
func resolveEmbeddedUpgradeN(sym string) easyfl.EmbeddedFunction[*EvalContext] {
    switch sym {
    case "evalMyEmbeddedFunc":
        return evalMyEmbeddedFunc
    }
    return nil
}

// evalMyEmbeddedFunc implements the embedded function.
func evalMyEmbeddedFunc(par *easyfl.CallParams[*EvalContext]) []byte {
    // Implementation here
    return result
}
```

### Step 4: Register the Resolver

Add your resolver to the resolver list in `ledger/def_embed.go`:

```go
func init() {
    upgradeEmbeddedResolvers = []struct {
        Slot     uint32
        Resolver EmbeddedResolver
    }{
        {0, resolveEmbeddedUpgrade0},
        {100000, resolveEmbeddedUpgradeN},  // Add your resolver
    }
}
```

**Resolver resolution order:** `GetEmbeddedFunctionResolver` searches resolvers in descending slot order (newest first), then falls back to the base EasyFL resolver. This means a newer upgrade's resolver can override symbols from older upgrades, which is how `embedded_as` function replacement works.

Upgrades that only add pure EasyFL formulas (no new embedded functions) don't need an entry in `upgradeEmbeddedResolvers`.

### Step 5: Create the Build Function

Add the build function in your upgrade file (`def_upgradeN.go`):

```go
func buildUpgradeNLibrary(prevJSON []byte) ([]byte, error) {
    // Parse the previous library with the unified resolver
    lib, err := ParseLibraryFromJSON(prevJSON, GetEmbeddedFunctionResolver)
    if err != nil {
        return nil, err
    }

    // Apply the upgrade definitions
    resolver := GetEmbeddedFunctionResolver(lib)
    if err := easyfl.IntroduceUpdateJSONMulti(lib, resolver, _upgradeNDefsJSON); err != nil {
        return nil, err
    }
    if err := lib.CommitUpdate(); err != nil {
        return nil, err
    }

    // Serialize compiled JSON (compiled=true includes bytecode; indent=false for storage)
    return easyfl.ToJSON(lib, true, false), nil
}
```

The `Build` function signature is `func(prevJSON []byte) ([]byte, error)` — it receives the previous library's compiled JSON and must return the upgraded library's compiled JSON.

> Note: the example above is illustrative — no concrete `def_upgradeN.go` Build function
> exists in the tree yet. `def_upgrade0.go` is the **genesis** builder (`upgrade0(lib, par)`,
> which mutates the library in place); it is not itself a `Build(prevJSON)` function, though
> it is the reference for which APIs to call.

## Modifying VersionData

`Library.VersionData` (`easyfl/engine/types.go`) is an opaque byte payload attached to each compiled library. It is included in the library hash (`easyfl/engine/serde_tools.go`), so changing it changes the library hash and therefore the upgrade UTXO commitment.

In proxima today, `VersionData` carries a JSON object naming the two transaction-integrity validator EasyFL functions, seeded at genesis by `def/def_constants0.json`:

```json
{"txIntegrityValidatorPartialContext":"txIntegrityValidatorPartialContext0",
 "txIntegrityValidatorFullContext":"txIntegrityValidatorFullContext0"}
```

`VersionDataIntegrityValidatorNames` (`ledger/constants.go`) parses it at startup to populate `*Library.TxIntegrityValidatorPartialContextName` / `...FullContextName`.

### Update rule (easyfl)

Every JSON blob passed to `IntroduceUpdateJSON` / `IntroduceUpdateJSONMulti` may carry a top-level `"versionData": "..."` field. The rule in `easyfl/engine/serde_tools.go` is:

- If the new blob's `versionData` is **non-empty after trim**, it **overwrites** `lib.VersionData`.
- If empty or missing, the existing value is left untouched.
- When several JSON blobs are applied in one call, the **last non-empty `versionData` wins**.

### Setting it in an upgrade

Place `"versionData": "<new payload>"` at the top of one of the JSON blobs your upgrade already passes (e.g. the constants file analogous to `def/def_constants0.json`):

```json
{
  "versionData": "{\"txIntegrityValidatorPartialContext\":\"txIntegrityValidatorPartialContext1\",\"txIntegrityValidatorFullContext\":\"txIntegrityValidatorFullContext1\"}",
  "functions": [ ... ]
}
```

If the upgrade only changes `versionData` and nothing else, pass a one-field blob `{"versionData":"..."}` — the empty function loop is a valid no-op.

### Proxima-specific constraints

If you change the integrity-validator payload:

- Both `txIntegrityValidatorPartialContext` and `txIntegrityValidatorFullContext` must be non-empty strings (asserted in `VersionDataIntegrityValidatorNames`).
- The named functions must be defined in the library **at that upgrade slot**. Register the new validator functions in the same upgrade (mirror how `upgrade0` registers `_txLayoutValidator0` via `lib.IntroduceUpdateManyMulti`).

## Rules and Constraints

### What Can Be Upgraded

- **Add** new EasyFL functions (formulas or embedded)
- **Replace** existing function definitions (with same `numArgs`)
- **Add** new embedded Go functions
- **Modify** constants defined in EasyFL

### What Cannot Be Done

- **Delete** functions (old code must remain for historical validation)
- **Change** `numArgs` of existing functions (EasyFL enforces this)
- **Remove** embedded function implementations
- **Change** genesis time or description (immutable across all upgrades, enforced by `validateGenesisIdentityImmutability`)
- **Place** upgrades closer than `MinSlotsBetweenUpgrades` (100) slots apart

### Backward Compatibility

- Old embedded function code must **never be modified or deleted**
- To fix bugs: create a new Go function and reference it via `embeddedAs` (with `replace: true`) in the upgrade's JSON
- Each upgrade version's resolver returns the appropriate Go implementations

Example of fixing an embedded function:

```go
// Original (upgrade0) - keep forever
func embeddedTicksBefore_v0(...) { /* original buggy code */ }

// Fixed (upgradeN)
func embeddedTicksBefore_v1(...) { /* fixed code */ }
```

```json
{
  "functions": [
    {
      "sym": "ticksBefore",
      "replace": true,
      "embeddedAs": "embeddedTicksBefore_v1"
    }
  ]
}
```

## Network Isolation via TxVersion

Each transaction carries a `TxVersion` field (uint16 big-endian at tuple index 0) that must match the library's upgrade index for the transaction's slot. This provides upgrade isolation at the transaction validation layer:

- Non-upgraded nodes reject transactions from upgraded nodes at Stage 1 parsing (TxVersion mismatch or tuple element count mismatch)
- Upgraded nodes reject old-format transactions for the same reason
- Nodes that fall behind on upgrades naturally fall out of sync

The peering rendezvous string is **fixed** (derived from the genesis library hash), so all nodes on the same ledger always discover each other regardless of upgrade status. Peering is no longer used for upgrade isolation.

## Verification

### Check Upgrade Status

```bash
# View all upgrades in the database
proxi db upgrades

# View database info including current library
proxi db info

# Query via API
curl http://localhost:8000/api/v1/get_ledger_definition
curl http://localhost:8000/api/v1/get_ledger_definition?slot=0  # Genesis
```

### Node Startup Logs

When a node starts, it logs upgrade information:

```
Ledger upgrades list:
        0:   IN EFFECT   <library-hash>
   100000:   PENDING     <library-hash>
```

## Upgrade Activation Details

### When Does an Upgrade Take Effect?

The upgrade takes effect **immediately at the activation slot**. The branch transaction at upgrade slot S is validated with the **new (upgraded) library**.

### Upgrade UTXO Injection

When a branch transaction is produced at or after an upgrade slot:

1. `InjectMissingUpgradeUTXOs` checks if upgrade UTXO exists in baseline state
2. If not present, the branch **must** include the upgrade UTXO in its mutations
3. The UTXO commits to the new library hash and links to the previous upgrade (hash chain)

The injection uses an optimization (`HasPendingUpgradeForSlot`) that tracks the next pending upgrade slot to avoid scanning all upgrades on every branch commit.

### Edge Case: No Branch at Exact Upgrade Slot

If no branch is produced exactly at the upgrade slot:
- The first branch at slot >= upgrade slot commits the upgrade UTXO
- Determinism is preserved (OutputID derived from upgrade slot, not commit slot)

### Snapshot Format

Snapshots include all upgrade libraries as `UpgradeLibraryEntry` records (slot + compiled JSON). When restoring from a snapshot, all upgrade libraries are written to the DB partition before initializing the library cache. This ensures the full upgrade history is preserved across snapshot round-trips.

## Testing Upgrades

### Unit Tests

```bash
# Run upgrade-related tests
go test ./ledger/multistate/... -run Upgrade
go test ./ledger/multistate/... -run Snapshot
```

### Integration Testing

1. Set `PendingUpgrade.Slot` to a low value (e.g., 100)
2. Start a test network
3. Wait for branch at/after upgrade slot
4. Verify upgrade UTXO appears in state
5. Verify new functions work in transactions

### Snapshot Round-Trip

1. Create snapshot after upgrade: `proxi snapshot db` (writes a snapshot from a recent branch)
2. Delete database
3. Restore from snapshot
4. Verify all upgrade libraries are present: `proxi db upgrades`

## File Reference

| File | Purpose |
|------|---------|
| `ledger/def_upgrade.go` | `PendingUpgrade` variable, `UpgradeDefinition` type (`Build func(prevJSON []byte) ([]byte, error)`) |
| `ledger/def_upgradeN.go` | Upgrade N: JSON embed, resolver function, build function |
| `ledger/def/def_*.json` | EasyFL JSON definitions (embedded, helpers, general functions, constants) |
| `ledger/def.go` | `ParseLibraryFromJSON`, `LibraryJSONFromParameters`, `LibraryFromParameters` |
| `ledger/def_embed.go` | `upgradeEmbeddedResolvers` list, `GetEmbeddedFunctionResolver`, `EmbeddedResolver` type |
| `ledger/lib.go` | `Library` struct, `UpgradeChainData` |
| `ledger/lib_singleton.go` | `L(slot)`, `LibraryCache`, `MustInitLibraryCache`, `MustInitLibraryCacheFromJSON`, `HasPendingUpgradeForSlot` |
| `ledger/upgrade_utxo.go` | `UpgradeUTXO()`, `ParseUpgradeUTXO()`, `VerifyUpgradeUTXO()`, `BaseLibraryHash()` |
| `ledger/base/upgrade_output_id.go` | Synthetic OutputID: `UpgradeOutputID()`, `IsUpgradeOutputID()` |
| `ledger/multistate/upgrades.go` | DB storage: `WriteUpgradeLibrary()`, `GetUpgradeLibraryDirect()`, `MinSlotsBetweenUpgrades` |
| `ledger/multistate/upgrade_inject.go` | `InjectMissingUpgradeUTXOs()` — injection during branch commits |
| `ledger/multistate/snapshot.go` | Snapshot format with `UpgradeLibraryEntry` records |
| `ledger/multistate/genesis_snapshot.go` | `BuildGenesisSnapshotData()`, `CreateGenesisSnapshot()` |
| `proxi/db_cmd/upgrades.go` | `proxi db upgrades` CLI command |

## Checklist for New Upgrades

- [ ] Choose upgrade slot (well in the future, >= 100 slots from previous upgrade)
- [ ] Create `ledger/def/def_upgradeN.json` with function definitions
- [ ] Create `ledger/def_upgradeN.go` with build function and resolver (if using embedded functions)
- [ ] Add resolver to `upgradeEmbeddedResolvers` in `ledger/def_embed.go`
- [ ] If changing `versionData`, ensure the named functions are registered in this upgrade
- [ ] Set `PendingUpgrade` in `ledger/def_upgrade.go`
- [ ] Ensure genesis time and description are unchanged
- [ ] Write tests for new functions
- [ ] Test upgrade activation on testnet
- [ ] Test snapshot round-trip with upgrade
- [ ] Document changes in release notes
