package txstore

import (
	"os"

	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initPutCmd() *cobra.Command {
	getCmd := &cobra.Command{
		Use:   "put <transaction file name>",
		Short: "persist raw transaction bytes from the file into the txStore",
		Args:  cobra.ExactArgs(1),
		Run:   runPutCmd,
	}
	getCmd.InitDefaultHelpCmd()
	return getCmd
}

// runPutCmd reads a file containing raw transaction bytes (no metadata prefix
// after metadata-refactor §7) and persists them to the txStore.
func runPutCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromDB()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	txBytes, err := os.ReadFile(args[0])
	glb.AssertNoError(err)

	tx, err := transaction.Parse(txBytes)
	glb.AssertNoError(err)

	txid := tx.ID()
	glb.Assertf(args[0] == txid.AsFileName(), "transaction id does not correspond to the file name")
	glb.Assertf(!glb.TxBytesStore().HasTxBytes(&txid), "txStore already contains transactions %s", tx.IDString())

	_, err = glb.TxBytesStore().PersistTxBytes(txBytes, tx.ID())
	glb.AssertNoError(err)
}
