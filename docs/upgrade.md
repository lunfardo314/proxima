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

Each upgrade is committed to the ledger state via an unspendable UTXO with 5 constraints:

| Index | Content | Description |
|-------|---------|-------------|
| 0 | Amount: `0` | No tokens (unspendable) |
| 1 | Lock: empty inline data | Evaluates to `false` (unspendable) |
| 2 | Library hash (32 bytes) | Hash of the new compiled library |
| 3 | Previous library hash (32 bytes) | Hash of the predecessor library |
| 4 | Previous upgrade slot (4 bytes BigEndian) | Slot of the predecessor upgrade (`MaxSlot` for slot 0) |

The synthetic OutputID format is defined in `ledger/base/upgrade_output_id.go`.

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

### Step 2: Create YAML Definitions

Create YAML definition files in `ledger/def/` (following the existing pattern `def/def_*.yaml`):

```yaml
# ledger/def/def_upgradeN.yaml
# Upgrade N definitions
# Activates at slot XXXXX

functions:
  -
    sym: "myFunction"
    description: "description of what this function does"
    numArgs: 2
    source: "add($0, $1)"  # Pure EasyFL formula
```

For functions requiring Go implementations (embedded functions):

```yaml
functions:
  -
    sym: "myEmbeddedFunc"
    description: "description"
    numArgs: 3
    embedded_as: evalMyEmbeddedFunc  # Maps to Go function name
```

### Step 3: Create Upgrade Go File (if using embedded functions)

Create `ledger/def_upgradeN.go` (following the existing pattern of `def_upgrade0.go`):

```go
package ledger

import (
    _ "embed"
    "github.com/lunfardo314/easyfl"
)

//go:embed def/def_upgradeN.yaml
var _upgradeNDefsYAML []byte

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
func buildUpgradeNLibrary(prevYAML []byte) ([]byte, error) {
    // Parse the previous library with the unified resolver
    lib, err := ParseLibraryFromYAML(prevYAML, GetEmbeddedFunctionResolver)
    if err != nil {
        return nil, err
    }

    // Apply the upgrade definitions using upgradeLibrary helper
    err = upgradeLibrary(lib, _upgradeNDefsYAML)
    if err != nil {
        return nil, err
    }

    // Return compiled YAML (true = include bytecode)
    return lib.ToYAML(true), nil
}
```

The `Build` function signature is `func(prevYAML []byte) ([]byte, error)` — it receives the previous library's compiled YAML and must return the upgraded library's compiled YAML.

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
- To fix bugs: create a new Go function and use `embedded_as` in YAML
- Each upgrade version's resolver returns the appropriate Go implementations

Example of fixing an embedded function:

```go
// Original (upgrade0) - keep forever
func embeddedTicksBefore_v0(...) { /* original buggy code */ }

// Fixed (upgradeN)
func embeddedTicksBefore_v1(...) { /* fixed code */ }
```

```yaml
# In def/def_upgradeN.yaml
functions:
  -
    sym: "ticksBefore"
    embedded_as: embeddedTicksBefore_v1  # Maps to fixed version
```

## Peering Rendezvous

The hash of the **pending upgrade library** (if defined) is used as the peering rendezvous code. This:

- Isolates upgraded nodes from non-upgraded nodes
- Forces node operators to upgrade before the target slot
- Ensures network consensus on upcoming changes

## Verification

### Check Upgrade Status

```bash
# View all upgrades in the database
proxi db upgrades

# View database info including current library
proxi db info

# Query via API
curl http://localhost:8080/api/v1/get_ledger_definition
curl http://localhost:8080/api/v1/get_ledger_definition?slot=0  # Genesis
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

Snapshots include all upgrade libraries as `UpgradeLibraryEntry` records (slot + compiled YAML). When restoring from a snapshot, all upgrade libraries are written to the DB partition before initializing the library cache. This ensures the full upgrade history is preserved across snapshot round-trips.

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

1. Create snapshot after upgrade: `proxi snapshot create`
2. Delete database
3. Restore from snapshot
4. Verify all upgrade libraries are present: `proxi db upgrades`

## File Reference

| File | Purpose |
|------|---------|
| `ledger/def_upgrade.go` | `PendingUpgrade` variable, `UpgradeDefinition` type, `upgradeLibrary` helper |
| `ledger/def_upgradeN.go` | Upgrade N: YAML embed, resolver function, build function |
| `ledger/def/def_*.yaml` | EasyFL YAML definitions (embedded, helpers, general functions) |
| `ledger/def_embed.go` | `upgradeEmbeddedResolvers` list, `GetEmbeddedFunctionResolver`, `EmbeddedResolver` type |
| `ledger/lib.go` | `Library` struct, `UpgradeChainData` |
| `ledger/lib_singleton.go` | `L(slot)`, `LibraryCache`, `MustInitLibraryCache`, `MustInitLibraryCacheFromYAML` |
| `ledger/upgrade_utxo.go` | `UpgradeUTXO()`, `ParseUpgradeUTXO()`, `VerifyUpgradeUTXO()`, `BaseLibraryHash()` |
| `ledger/base/upgrade_output_id.go` | Synthetic OutputID: `UpgradeOutputID()`, `IsUpgradeOutputID()` |
| `ledger/multistate/upgrades.go` | DB storage: `WriteUpgradeLibrary()`, `GetUpgradeLibraryDirect()`, `MinSlotsBetweenUpgrades` |
| `ledger/multistate/upgrade_inject.go` | `InjectMissingUpgradeUTXOs()` — injection during branch commits |
| `ledger/multistate/snapshot.go` | Snapshot format with `UpgradeLibraryEntry` records |
| `ledger/multistate/genesis_snapshot.go` | `BuildGenesisSnapshotData()`, `CreateGenesisSnapshot()` |
| `proxi/db_cmd/upgrades.go` | `proxi db upgrades` CLI command |

## Checklist for New Upgrades

- [ ] Choose upgrade slot (well in the future, >= 100 slots from previous upgrade)
- [ ] Create `ledger/def/def_upgradeN.yaml` with function definitions
- [ ] Create `ledger/def_upgradeN.go` with build function and resolver (if using embedded functions)
- [ ] Add resolver to `upgradeEmbeddedResolvers` in `ledger/def_embed.go`
- [ ] Set `PendingUpgrade` in `ledger/def_upgrade.go`
- [ ] Ensure genesis time and description are unchanged
- [ ] Write tests for new functions
- [ ] Test upgrade activation on testnet
- [ ] Test snapshot round-trip with upgrade
- [ ] Document changes in release notes
