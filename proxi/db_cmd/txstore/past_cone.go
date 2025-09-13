package txstore

import (
	"strconv"

	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initPastConeCmd() *cobra.Command {
	pastConeCmd := &cobra.Command{
		Use:   "past_cone <transaction id hex> <number of slots back>",
		Short: "creates .DOT file with dag representation of the past cone of the transaction",
		Args:  cobra.ExactArgs(2),
		Run:   runPastConeCmd,
	}
	pastConeCmd.InitDefaultHelpCmd()
	return pastConeCmd
}

func runPastConeCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromDB()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	txid, err := base.TransactionIDFromHexString(args[0])
	glb.AssertNoError(err)
	glb.Infof("transaction id: %s", txid.String())
	slotsBack, err := strconv.Atoi(args[1])
	glb.AssertNoError(err)
	glb.Assertf(slotsBack >= 1 && int(txid.Slot()) >= slotsBack, "wrong second parameter '%s'", args[1])
	oldestSlot := txid.Slot() - uint32(slotsBack)

	fname := txid.AsFileNameShort()
	glb.Infof("writing past cone of %s to '%s'", txid.StringShort(), fname)
	memdag.SavePastConeFromTxStore(txid, glb.TxBytesStore(), oldestSlot, fname)
}
