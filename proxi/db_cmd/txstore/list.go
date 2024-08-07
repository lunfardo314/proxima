package txstore

import (
	"strconv"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initListCmd() *cobra.Command {
	crossCheckCmd := &cobra.Command{
		Use:   "list <slot>",
		Short: "lists IDs of transactions in the heaviest state in the particular slot",
		Args:  cobra.MaximumNArgs(1),
		Run:   runListCmd,
	}
	return crossCheckCmd
}

func runListCmd(_ *cobra.Command, args []string) {
	glb.InitLedger()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	latestSlot := multistate.FetchLatestCommittedSlot(glb.StateStore())
	glb.Infof("latest committed slot: %d", latestSlot)

	slot := latestSlot
	if len(args) > 0 {
		slotInt, err := strconv.Atoi(args[0])
		glb.AssertNoError(err)
		slot = ledger.Slot(slotInt)
	}

	glb.Infof("list transactions in the heaviest state for slot %d", slot)
	glb.Infof("now is slot %d", ledger.TimeNow().Slot())

	branches := multistate.FetchLatestBranches(glb.StateStore())
	rdr := multistate.MustNewReadable(glb.StateStore(), branches[0].Root)

	nTx := 0
	rdr.IterateKnownCommittedTransactions(func(txid *ledger.TransactionID, slot ledger.Slot) bool {
		hasBytes := glb.TxBytesStore().HasTxBytes(txid)
		glb.Infof("%s, hex ID = %s, has txBytes = %v ", txid.StringShort(), txid.StringHex(), hasBytes)
		nTx++
		return true
	}, slot)

	glb.Infof("total: %d transactions", nTx)
}
