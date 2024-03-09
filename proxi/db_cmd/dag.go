package db_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

var outputFileDAG string

const defaultMaxSlotsBackDAG = 100

func initDBDAGCmd() *cobra.Command {
	dbTreeCmd := &cobra.Command{
		Use:   fmt.Sprintf("dag [max slots back, default %d]", defaultMaxSlotsBackDAG),
		Short: "create .DOT file for the MemDAG of all transactions in the past cone of tip branches",
		Args:  cobra.MaximumNArgs(1),
		Run:   runDbDAGCmd,
	}
	dbTreeCmd.PersistentFlags().StringVarP(&outputFileDAG, "output", "o", "", "output file")
	dbTreeCmd.InitDefaultHelpCmd()
	return dbTreeCmd
}

func runDbDAGCmd(_ *cobra.Command, args []string) {
	glb.InitLedger()
	glb.InitTxStoreDB()

	defer glb.CloseDatabases()

	pwdPath, err := os.Getwd()
	glb.AssertNoError(err)
	currentWorkingDir := filepath.Base(pwdPath)

	outFile := outputFile
	if outputFileDAG == "" {
		outputFileDAG = global.TxStoreDBName + "_DAG_" + currentWorkingDir
	}

	branchTxIDS := multistate.FetchLatestBranchTransactionIDs(glb.StateStore())
	numSlotsBack := defaultMaxSlotsBackDAG
	if len(args) == 0 {
		tmpDag := memdag.MakeDAGFromTxStore(glb.TxBytesStore(), 0, branchTxIDS...)
		tmpDag.SaveGraph(outputFileDAG)
	} else {
		latestSlot := multistate.FetchLatestSlot(glb.StateStore())
		var err error
		numSlotsBack, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
		oldestSlot := 0
		if numSlotsBack < int(latestSlot) {
			oldestSlot = int(latestSlot) - numSlotsBack
		}
		tmpDag := memdag.MakeDAGFromTxStore(glb.TxBytesStore(), ledger.Slot(oldestSlot), branchTxIDS...)
		tmpDag.SaveGraph(outputFileDAG)
	}
	glb.Infof("MemDAG has been store in .DOT format in the file '%s', %d slots back", outFile, numSlotsBack)
}
