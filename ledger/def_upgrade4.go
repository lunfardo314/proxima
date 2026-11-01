package ledger

import (
	_ "embed"
)

// Library upgrade 4 (kb/library_upgrade4.md): every endorsement is a sequencer
// transaction, checked in the partial context, and the sequencer output of a sequencer
// transaction is not a chain origin. Pure EasyFL, no new embedded function. Built on the
// library of upgrade 3.

//go:embed def/upgrade4.easyfl
var upgrade4Source string

// BuildUpgrade4Library applies upgrade 4 to the compiled JSON of the library
// before it and returns the compiled JSON of the upgraded one.
func BuildUpgrade4Library(prevJSON []byte) ([]byte, error) {
	return buildUpgrade(prevJSON, "upgrade 4", upgrade4Source)
}

// HasUpgrade4 tells whether the library carries upgrade 4. Node-side rules that
// come with it are gated by it, so they apply from the same slot as the ledger rules.
func (lib *Library) HasUpgrade4() bool {
	_, err := lib.FunctionByName("_endorsementIsSequencerTx")
	return err == nil
}
