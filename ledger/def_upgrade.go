package ledger

import "github.com/lunfardo314/easyfl"

// UpgradeDefinition defines a pending library upgrade.
type UpgradeDefinition struct {
	// Slot is the first slot where the new library rules apply.
	Slot uint32

	// Build takes the previous library YAML and returns the upgraded library YAML.
	Build func(prevYAML []byte) ([]byte, error)
}

// PendingUpgrade is the current pending upgrade, or nil if no upgrade is pending.
// At most one pending upgrade can exist at a time.
var PendingUpgrade *UpgradeDefinition = nil

// upgradeLibrary applies YAML definitions to a library using the unified resolver.
func upgradeLibrary(lib *easyfl.Library[*EvalContext], yamlList ...[]byte) error {
	resolver := GetEmbeddedFunctionResolver(lib)

	for _, yaml := range yamlList {
		if err := lib.UpgradeFromYAML(yaml, resolver); err != nil {
			return err
		}
	}
	return nil
}
