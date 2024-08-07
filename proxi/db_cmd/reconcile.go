package db_cmd

import (
	"fmt"
	"strconv"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

var outputFileReconcile string

const defaultReconcileSlotsBack = 100

func initReconcileCmd() *cobra.Command {
	reconcileCmd := &cobra.Command{
		Use:   fmt.Sprintf("reconcile [max slots back, default %d]", defaultReconcileSlotsBack),
		Short: "report transactionIDs from the heaviest state without transaction bytes in the txStore",
		Args:  cobra.MaximumNArgs(1),
		Run:   runReconcileCmd,
	}
	reconcileCmd.PersistentFlags().StringVarP(&outputFileReconcile, "output", "o", "", "output file")
	reconcileCmd.InitDefaultHelpCmd()
	return reconcileCmd
}

func runReconcileCmd(_ *cobra.Command, args []string) {
	glb.InitLedger()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	slotsBack := defaultReconcileSlotsBack
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

	for ; slot >= downToSlot; slot-- {
		n := 0
		rdr.IterateKnownCommittedTransactions(func(txid *ledger.TransactionID, slot ledger.Slot) bool {
			n++
			return true
		}, slot)
		glb.Infof("slot %d: num transactions: %d", slot, n)
	}
}
