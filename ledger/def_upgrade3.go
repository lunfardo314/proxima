package ledger

import (
	_ "embed"
)

// Library upgrade 3 (kb/library_upgrade3.md): a frozen delegation is always one its
// target has credited, which closes the negative frozen-coverage cell on the target
// chain (report LPX-014), and a branch's VRF message is chained on its predecessor's
// VRF output instead of its proof, which closes the grinding of the next branch's
// bonus. Pure EasyFL, no new embedded function. Built on the library of upgrade 2.

//go:embed def/upgrade3.easyfl
var upgrade3Source string

// BuildUpgrade3Library applies upgrade 3 to the compiled JSON of the library
// before it and returns the compiled JSON of the upgraded one.
func BuildUpgrade3Library(prevJSON []byte) ([]byte, error) {
	return buildUpgrade(prevJSON, "upgrade 3", upgrade3Source)
}
