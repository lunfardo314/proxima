# Proxima Ledger Upgrade Guide

This document describes how to create and deploy ledger library upgrades in Proxima.

## Overview

Proxima supports incremental upgrades to its ledger library (EasyFL definitions and embedded functions). Each upgrade:

- Activates at a specific **upgrade slot** (the first slot where new rules apply)
- Is deterministic across all nodes
- Maintains backward compatibility for historical transaction validation
- Creates an **upgrade UTXO** committed to the ledger state

## Upgrade Lifecycle

```
1. DEVELOPMENT   → Add upgrade code to ledger/
2. DEPLOYMENT    → Node operators update their nodes (before upgrade slot)
3. ACTIVATION    → Target slot reached, upgrade UTXO injected
4. VERIFICATION  → All nodes use new rules from upgrade slot onwards
```

## Creating an Upgrade

### Step 1: Define the Upgrade Slot

Edit `ledger/def_upgrade.go` and set the `PendingUpgrade` variable:

```go
func init() {
    PendingUpgrade = &UpgradeDefinition{
        Slot:  100000,  // First slot where new rules apply
        Build: buildUpgradeNLibrary,
    }
}
```

**Important:** Choose a slot far enough in the future to allow all node operators to update.

### Step 2: Create YAML Definitions

Create a file `ledger/upgradeN_defs.yaml` with your EasyFL function definitions:

```yaml
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

### Step 3: Create Resolver (if using embedded functions)

Create `ledger/def_resolvers_upgradeN.go`:

```go
package ledger

import (
    _ "embed"
    "github.com/lunfardo314/easyfl"
)

//go:embed upgradeN_defs.yaml
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
        {100, resolveEmbeddedUpgradeN},  // Add your resolver
    }
}
```

### Step 5: Create the Build Function

Add the build function (in your resolver file or `def_upgrade.go`):

```go
func buildUpgradeNLibrary(prevYAML []byte) ([]byte, error) {
    // Parse the previous library with the unified resolver
    lib, err := ParseLibraryFromYAML(prevYAML, GetEmbeddedFunctionResolver)
    if err != nil {
        return nil, err
    }

    // Apply the upgrade definitions
    err = lib.UpgradeFromYAML(_upgradeNDefsYAML, GetEmbeddedFunctionResolver(lib))
    if err != nil {
        return nil, err
    }

    // Return compiled YAML
    return lib.ToYAML(true), nil
}
```

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
# In upgradeN_defs.yaml
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

1. Node checks if upgrade UTXO exists in baseline state
2. If not present, the branch **must** include the upgrade UTXO
3. The UTXO commits to the new library hash

**Upgrade UTXO Format:**
- Amount: `0` (unspendable)
- Lock: `false`
- Constraint 2: hash of new library
- Constraint 3: hash of previous library
- Constraint 4: previous upgrade slot (4 bytes BigEndian)

### Edge Case: No Branch at Exact Upgrade Slot

If no branch is produced exactly at the upgrade slot:
- The first branch at slot ≥ upgrade slot commits the upgrade UTXO
- Determinism is preserved (OutputID derived from upgrade slot, not commit slot)

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
| `ledger/def_upgrade.go` | `PendingUpgrade` definition, `UpgradeDefinition` type |
| `ledger/upgradeN_defs.yaml` | EasyFL definitions for upgrade N |
| `ledger/def_resolvers_upgradeN.go` | Embedded function resolver for upgrade N |
| `ledger/def_embed.go` | Resolver list, `GetEmbeddedFunctionResolver` |
| `ledger/upgrade_utxo.go` | Upgrade UTXO creation and parsing |
| `ledger/multistate/upgrades.go` | DB storage for upgrade libraries |
| `ledger/multistate/upgrade_inject.go` | Injection during branch commits |
| `proxi/db_cmd/upgrades.go` | CLI command for viewing upgrades |

## Checklist for New Upgrades

- [ ] Choose upgrade slot (well in the future)
- [ ] Create `ledger/upgradeN_defs.yaml` with function definitions
- [ ] Create `ledger/def_resolvers_upgradeN.go` (if using embedded functions)
- [ ] Add resolver to `upgradeEmbeddedResolvers` in `def_embed.go`
- [ ] Set `PendingUpgrade` in `def_upgrade.go`
- [ ] Write tests for new functions
- [ ] Test upgrade activation on testnet
- [ ] Document changes in release notes
