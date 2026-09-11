package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initTxCmd() *cobra.Command {
	txCmd := &cobra.Command{
		Use:   "tx <transaction id, hex or dashed>",
		Short: "prints the transaction from the node's txstore with its consumed outputs",
		Args:  cobra.ExactArgs(1),
		Run:   runTxCmd,
	}
	txCmd.InitDefaultHelpCmd()
	return txCmd
}

// runTxCmd fetches the transaction and every transaction it consumes from
// the node, then prints the wallet-side rendering with the consumed outputs
// in place. A consumed transaction the node no longer has is marked as such
// instead of failing the whole display.
func runTxCmd(_ *cobra.Command, args []string) {
	txid, err := glb.ParseTransactionID(args[0])
	glb.AssertNoError(err)

	txBytes, err := glb.GetClient().GetTxBytes(txid)
	glb.AssertNoError(err)

	tx, err := transaction.ParseLibraryAgnostic(txBytes)
	glb.AssertNoError(err)

	consumed := make([][]byte, tx.NumInputs())
	producedBy := make(map[base.TransactionID]*transaction.Transaction)
	tx.ForEachInputID(func(idx byte, oid base.OutputID) bool {
		inTxID := oid.TransactionID()
		inTx, ok := producedBy[inTxID]
		if !ok {
			inTx = fetchTransaction(inTxID)
			producedBy[inTxID] = inTx
		}
		if inTx != nil {
			inTx.ForEachProducedOutputData(func(i byte, oData []byte) bool {
				if i == oid.Index() {
					consumed[idx] = oData
					return false
				}
				return true
			})
		}
		return true
	})
	glb.Infof("%s", glb.TxDisplay(glb.GetTxLibrary(), txBytes, consumed...))
}

// fetchTransaction returns nil when the node cannot serve the transaction,
// reporting why.
func fetchTransaction(txid base.TransactionID) *transaction.Transaction {
	txBytes, err := glb.GetClient().GetTxBytes(txid)
	if err != nil {
		glb.Infof("consumed transaction %s: %v", txid.StringShort(), err)
		return nil
	}
	tx, err := transaction.ParseLibraryAgnostic(txBytes)
	if err != nil {
		glb.Infof("consumed transaction %s: parse error: %v", txid.StringShort(), err)
		return nil
	}
	return tx
}
