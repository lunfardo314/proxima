// Package upgrade provides the pending upgrade registration system for the Proxima ledger.
//
// At most one pending upgrade can exist in the codebase at any time. This simplifies:
// - Code organization (no need to manage multiple pending upgrades)
// - Testing (only test current → next transition)
// - Node operator experience (clear single upgrade path)
//
// Upgrade Lifecycle:
//  1. DEVELOPMENT: Developer adds upgrade to ledger/upgrade/
//     - Set PendingUpgrade with target slot and build function
//     - Create resolver in pending_resolver.go
//  2. DEPLOYMENT: Node operators update their nodes before target slot
//     - Upgrade is registered at startup and stored in DB partition
//  3. ACTIVATION: Target slot reached
//     - Upgrade UTXO injected into first branch at/after slot
//     - New library rules apply
//  4. CLEANUP: After activation (optional)
//     - PendingUpgrade can be set to nil
//     - Upgrade data lives in DB partition
package upgrade

// UpgradeDefinition defines a pending library upgrade.
// When PendingUpgrade is non-nil, the upgrade will be registered at node startup.
//
// The Resolver field must be registered separately via ledger.RegisterResolverForUpgrade()
// to avoid circular imports. This is typically done in pending_resolver.go.
type UpgradeDefinition struct {
	// Slot is the first slot where the new library rules apply.
	// This should be set well in the future to allow node operators time to upgrade.
	Slot uint32

	// Build takes the previous library YAML and returns the upgraded library YAML.
	// This is called at node startup if the upgrade is not yet in the DB partition.
	// The previous library is the most recent library already stored in the DB.
	Build func(prevYAML []byte) ([]byte, error)

	// RegisterResolver is called during initialization to register the embedded
	// function resolver for this upgrade. This is done separately to avoid
	// circular imports between the upgrade and ledger packages.
	// If nil, the upgrade uses the same resolver as the previous version.
	RegisterResolver func()
}

// PendingUpgrade is the current pending upgrade, or nil if no upgrade is pending.
// At most one pending upgrade can exist at a time.
//
// To create a new upgrade:
//  1. Set PendingUpgrade to a non-nil UpgradeDefinition in pending.go
//  2. Implement RegisterResolver to call ledger.RegisterResolverForUpgrade()
//  3. Implement the Build function to produce the upgraded library YAML
//  4. Optionally add pending_defs.yaml with EasyFL definitions
//
// Example in pending.go:
//
//	var PendingUpgrade = &UpgradeDefinition{
//	    Slot:             100000,  // First slot where new rules apply
//	    Build:            buildUpgrade1Library,
//	    RegisterResolver: registerUpgrade1Resolver,
//	}
var PendingUpgrade *UpgradeDefinition = nil
