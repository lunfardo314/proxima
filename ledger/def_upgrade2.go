package ledger

import (
	_ "embed"
)

// Library upgrade 2: no sequencer transaction other than a branch before tick
// constPostBranchConsolidationTicks of its slot, and in the pre-branch consolidation
// zone a sequencer transaction's chain predecessor must be of the same slot
// (kb/library_upgrade2.md). Pure EasyFL, no new embedded function. Built on the
// library of upgrade 1.

//go:embed def/upgrade2.easyfl
var upgrade2Source string

// BuildUpgrade2Library applies upgrade 2 to the compiled JSON of the library
// before it and returns the compiled JSON of the upgraded one.
func BuildUpgrade2Library(prevJSON []byte) ([]byte, error) {
	return buildUpgrade(prevJSON, "upgrade 2", upgrade2Source)
}
