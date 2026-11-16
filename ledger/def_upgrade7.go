package ledger

import (
	"bytes"
	_ "embed"
	"text/template"
)

// Library upgrade 7: the branch bonus chosen by the sequencer within [1 mote, 5 PROX] and
// sealed by a commitment on the branch's chain predecessor; an extension element on every
// sequencer output (a tuple, element 0 the bid commitment); a branch consumes only its stem
// and chain predecessor; no sequencer transaction other than a branch after tick 119; the
// soft bound of a sequencer reached by 20,000 PROX of its own or 100,000 PROX of coverage; a
// top-up from any sender. Pure EasyFL, no new embedded function. Built on the library of
// upgrade 6.

//go:embed def/upgrade7.easyfl
var upgrade7Source string

// SeqExtensionFixedIndex is the position of the extension element on a sequencer output,
// required from upgrade 7: inline data holding a serialized tuple, element 0 the bid
// commitment.
const SeqExtensionFixedIndex = 6

// BranchSaltTxConstraintIndex is where a branch reveals the 8-byte salt of its sealed bid,
// as inline data in TxConstraints.
const BranchSaltTxConstraintIndex = 0

// HasUpgrade7 tells whether the library carries upgrade 7. Node and sequencer code that comes
// with it is gated by it, so it applies from the same slot as the ledger rules.
func (lib *Library) HasUpgrade7() bool {
	_, err := lib.FunctionByName("_sealedBidRevealed")
	return err == nil
}

// BuildUpgrade7LibraryAt returns the builder of upgrade 7 listed at slot: it applies the
// upgrade to the compiled JSON of the library before it, the activation slot filled in, and
// returns the compiled JSON of the upgraded one. At slot 0 the upgrade is folded into a
// genesis.
func BuildUpgrade7LibraryAt(slot uint32) func(prevJSON []byte) ([]byte, error) {
	return func(prevJSON []byte) ([]byte, error) {
		var src bytes.Buffer
		err := template.Must(template.New("upgrade7").Parse(upgrade7Source)).Execute(&src, struct {
			UpgradeSlot uint32
		}{slot})
		if err != nil {
			return nil, err
		}
		return buildUpgrade(prevJSON, "upgrade 7", src.String())
	}
}
