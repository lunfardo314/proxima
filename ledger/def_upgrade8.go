package ledger

import (
	"bytes"
	_ "embed"
	"text/template"
)

// Library upgrade 8: the burn of the funds still frozen after the 2026-11-17 exploit
// (kb/burn_frozen.md). The crossing branch, the first of each lineage at or after the
// upgrade slot, deletes the listed outputs as it is committed and declares a total supply
// lower by their amount; stemLock is replaced to allow exactly that. The list and the node
// side are in ledger/freezeburn. Pure EasyFL, no new embedded function. Built on the
// library of upgrade 7.

//go:embed def/upgrade8.easyfl
var upgrade8Source string

// HasUpgrade8 tells whether the library carries upgrade 8.
func (lib *Library) HasUpgrade8() bool {
	_, err := lib.FunctionByName("_burnedOnCrossing")
	return err == nil
}

// BuildUpgrade8LibraryAt returns the builder of upgrade 8 listed at slot: it applies the
// upgrade to the compiled JSON of the library before it, the activation slot filled in, and
// returns the compiled JSON of the upgraded one. At slot 0 the upgrade is folded into a
// genesis, where no branch crosses it.
func BuildUpgrade8LibraryAt(slot uint32) func(prevJSON []byte) ([]byte, error) {
	return func(prevJSON []byte) ([]byte, error) {
		var src bytes.Buffer
		err := template.Must(template.New("upgrade8").Parse(upgrade8Source)).Execute(&src, struct {
			UpgradeSlot uint32
		}{slot})
		if err != nil {
			return nil, err
		}
		return buildUpgrade(prevJSON, "upgrade 8", src.String())
	}
}
