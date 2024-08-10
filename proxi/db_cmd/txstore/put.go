package txstore

import (
	"os"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initPutCmd() *cobra.Command {
	getCmd := &cobra.Command{
		Use:   "put <transaction file name>",
		Short: "persist transaction bytes with metadata contained in the file into the txStore",
		Args:  cobra.ExactArgs(1),
		Run:   runPutCmd,
	}
	getCmd.InitDefaultHelpCmd()
	return getCmd
}

func runPutCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromDB()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	txBytesWithMetadata, err := os.ReadFile(args[0])
	glb.AssertNoError(err)

	metaBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	glb.AssertNoError(err)

	meta, err := txmetadata.TransactionMetadataFromBytes(metaBytes)
	glb.AssertNoError(err)

	tx, err := transaction.FromBytes(txBytes)
	glb.AssertNoError(err)

	glb.Assertf(args[0] == tx.ID().AsFileName(), "transaction ID does not correspond to the file name")
	glb.Assertf(!glb.TxBytesStore().HasTxBytes(tx.ID()), "txStore already contains transactions %s", tx.IDString())

	_, err = glb.TxBytesStore().PersistTxBytesWithMetadata(txBytes, meta)
	glb.AssertNoError(err)
}
