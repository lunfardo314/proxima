// Package txbtest is a test-only convenience layer around the
// transaction.ParseAndValidate sugar. Tests use it to keep their
// build-and-validate call sites a single line; production code calls
// transaction.ParseAndValidate directly with the loader of its choice.
package txbtest

import (
	"fmt"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
)

// BuildAndValidate finalises the builder (sequencer-side: stem outputs,
// commitment, signature; plain exhelp.Builder: as-is) and runs
// transaction.ParseAndValidate against its bytes. Returns (bytes, id,
// pretty-string, err); on parse failure id is zero and pretty-string is
// empty. On full-context failure tx is still rendered for diagnostics.
//
// txb must be *exhelp.Builder or *txbuilder_seq.SeqTxBuilder.
func BuildAndValidate(txb any) ([]byte, base.TransactionID, string, error) {
	var txBytes []byte
	var loader func(i byte) ([]byte, error)
	switch v := txb.(type) {
	case *exhelp.Builder:
		txBytes = v.Bytes()
		loader = v.LoadInputBytes
	case *txbuilder_seq.SeqTxBuilder:
		var err error
		txBytes, loader, err = v.BytesWithInputLoader()
		if err != nil {
			return nil, base.TransactionID{}, "", err
		}
	default:
		panic(fmt.Sprintf("txbtest.BuildAndValidate: unsupported builder type %T", txb))
	}
	tx, err := transaction.ParseAndValidate(txBytes, loader)
	if tx == nil {
		return txBytes, base.TransactionID{}, "", err
	}
	return txBytes, tx.ID(), tx.String(), err
}
