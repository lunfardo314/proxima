package ledger

import (
	_ "embed"
)

// Library upgrade 5 (kb/library_upgrade5.md): the mine successor is pinned in full,
// and the key of a sequencer chain changes only in the first slot of a window. Pure
// EasyFL, no new embedded function. Built on the library of upgrade 4.

//go:embed def/upgrade5.easyfl
var upgrade5Source string

// BuildUpgrade5Library applies upgrade 5 to the compiled JSON of the library
// before it and returns the compiled JSON of the upgraded one.
func BuildUpgrade5Library(prevJSON []byte) ([]byte, error) {
	return buildUpgrade(prevJSON, "upgrade 5", upgrade5Source)
}
