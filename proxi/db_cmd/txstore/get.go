package txstore

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

var (
	txStoreParse        bool
	txStoreSave         bool
	useProvidedLedgerID bool
)

func initGetCmd() *cobra.Command {
	getCmd := &cobra.Command{
		Use:   "get <transaction id hex>",
		Short: "retrieves transaction from the txStore, optionally parses and saves it",
		Args:  cobra.ExactArgs(1),
		Run:   runGetCmd,
	}
	getCmd.PersistentFlags().BoolVarP(&txStoreParse, "parse", "p", false, "parse and display transaction with metadata")
	getCmd.PersistentFlags().BoolVarP(&txStoreSave, "save", "s", false, "save transaction with metadata as file")
	getCmd.PersistentFlags().BoolVarP(&useProvidedLedgerID, "ledger_id", "l", false, fmt.Sprintf("use ledger definitions from '%s'", glb.LedgerDefinitionsFileName))
	getCmd.InitDefaultHelpCmd()
	return getCmd
}

func runGetCmd(_ *cobra.Command, args []string) {
	if useProvidedLedgerID {
		glb.InitLedgerFromProvidedID()
	} else {
		glb.InitLedgerFromDB()
	}
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	txid, err := base.TransactionIDFromHexString(args[0])
	glb.AssertNoError(err)

	txBytes := glb.TxBytesStore().GetTxBytes(&txid)
	if len(txBytes) == 0 {
		glb.Infof("NOT FOUND transaction %s in the txStore", txid.String())
		os.Exit(1)
	}

	glb.Infof("FOUND transaction %s in the txStore\n%d bytes", txid.String(), len(txBytes))
	if txStoreParse {
		glb.ParseAndDisplayTxFromSore(txid)
	}

	if txStoreSave {
		saveTx(&txid, txBytes)
	}
}

func saveTx(txid *base.TransactionID, txBytes []byte) {
	err := os.WriteFile(txid.AsFileName(), txBytes, 0666)
	glb.AssertNoError(err)
}
