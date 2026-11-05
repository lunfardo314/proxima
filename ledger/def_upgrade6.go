package ledger

import (
	_ "embed"
)

// Library upgrade 6 (kb/library_upgrade6.md): every output a mine transit produces is
// pinned element by element, the full reward is a top-up request, and a top-up lands
// only in a plain delegation, so a weak mining key leaves every reward open to anyone
// who knows it. Pure EasyFL, no new embedded function. Built on the library of upgrade 5.

//go:embed def/upgrade6.easyfl
var upgrade6Source string

// BuildUpgrade6Library applies upgrade 6 to the compiled JSON of the library
// before it and returns the compiled JSON of the upgraded one.
func BuildUpgrade6Library(prevJSON []byte) ([]byte, error) {
	return buildUpgrade(prevJSON, "upgrade 6", upgrade6Source)
}
