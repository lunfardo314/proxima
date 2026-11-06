package ledger

import (
	_ "embed"
)

// Library upgrade 6 (kb/library_upgrade6_mining.md): the mine chain's proof of work
// becomes the transit's own transaction ID and the reward is paid as the miner decides;
// the top-up guarantor requires a delegateLock predecessor. Pure EasyFL, no new
// embedded function. Built on the library of upgrade 5.

//go:embed def/upgrade6.easyfl
var upgrade6Source string

// BuildUpgrade6Library applies upgrade 6 to the compiled JSON of the library
// before it and returns the compiled JSON of the upgraded one.
func BuildUpgrade6Library(prevJSON []byte) ([]byte, error) {
	return buildUpgrade(prevJSON, "upgrade 6", upgrade6Source)
}
