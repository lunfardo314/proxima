package db_cmd

import (
	"fmt"

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

	glb.Infof("NOT IMPLEMENTED")

	// TODO not implemented
}
