package db_cmd

import (
	"fmt"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

var outputFileReconcile string

const defaultCrossCheckSlotsBack = 100

func initCrossCheckCmd() *cobra.Command {
	crossCheckCmd := &cobra.Command{
		Use:   fmt.Sprintf("crosscheck [max slots back, default %d]", defaultCrossCheckSlotsBack),
		Short: "report transactionIDs from the heaviest state without transaction bytes in the txStore",
		Args:  cobra.MaximumNArgs(1),
		Run:   runReconcileCmd,
	}
	crossCheckCmd.PersistentFlags().StringVarP(&outputFileReconcile, "output", "o", "", "output file")
	crossCheckCmd.InitDefaultHelpCmd()
	return crossCheckCmd
}

func runReconcileCmd(_ *cobra.Command, args []string) {
	glb.InitLedger()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	slotsBack := defaultCrossCheckSlotsBack
	var err error
	if len(args) >= 1 {
		slotsBack, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
	}

	glb.Infof("now is slot %d", ledger.TimeNow().Slot())

	slot := multistate.FetchLatestCommittedSlot(glb.StateStore())
	glb.Infof("latest committed slot is %d", slot)
	var downToSlot ledger.Slot
	if int(slot) > slotsBack {
		downToSlot = slot - ledger.Slot(slotsBack)
	}

	branches := multistate.FetchLatestBranches(glb.StateStore())
	rdr := multistate.MustNewReadable(glb.StateStore(), branches[0].Root)

	nTx := 0
	nSlots := 0
	start := time.Now()
	for ; slot >= downToSlot; slot-- {
		rdr.IterateKnownCommittedTransactions(func(txid *ledger.TransactionID, slot ledger.Slot) bool {
			if !glb.TxBytesStore().HasTxBytes(txid) {
				glb.Infof("transaction %s not in the txStore", txid.StringShort())
			}
			nTx++
			return true
		}, slot)
		nSlots++
	}
	glb.Infof("it took %v to check %d transaction IDs and %d slots", time.Since(start), nTx, nSlots)
}
