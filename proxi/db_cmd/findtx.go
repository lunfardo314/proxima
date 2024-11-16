package db_cmd

import (
	"strings"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initFindTxCmd() *cobra.Command {
	findTxCmd := &cobra.Command{
		Use:   "findtx",
		Short: "finds transaction IDs in slot and/or with hex fragment",
		Args:  cobra.NoArgs,
		Run:   runFindTxCmd,
	}

	findTxCmd.PersistentFlags().Uint32VarP(&findInSlot, "slot", "s", 0, "slot prefix")
	findTxCmd.PersistentFlags().StringVarP(&findWithHexFragment, "hex_fragment", "x", "", "hex fragment")
	findTxCmd.PersistentFlags().BoolVarP(&findFirst, "find_first", "1", false, "break when first found")
	findTxCmd.InitDefaultHelpCmd()

	return findTxCmd
}

var (
	findInSlot          uint32
	findWithHexFragment string
	findFirst           bool
)

func runFindTxCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromDB()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	glb.Assertf(findInSlot != 0 || findWithHexFragment != "", "at least one of slot or fragment must be specified")

	lrb := multistate.FindLatestReliableBranch(glb.StateStore(), global.FractionHealthyBranch)
	glb.Assertf(lrb != nil, "can't find latest reliable branch (LRB)")

	var filterSlots []ledger.Slot
	if findInSlot != 0 {
		filterSlots = []ledger.Slot{ledger.Slot(findInSlot)}
	}

	if findInSlot > 0 {
		glb.Infof("find in slot %d", findInSlot)
	} else {
		glb.Infof("find in slot: N/A")
	}
	if findWithHexFragment != "" {
		glb.Infof("find with hex fragment '%s'", findWithHexFragment)
	} else {
		glb.Infof("find with hex fragment: N/A")
	}

	rdr := multistate.MustNewReadable(glb.StateStore(), lrb.Root)
	nTx := 0
	nFound := 0
	rdr.IterateKnownCommittedTransactions(func(txid *ledger.TransactionID, _ ledger.Slot) bool {
		if findWithHexFragment == "" || strings.Contains(txid.String(), findWithHexFragment) {
			glb.Infof("%6d   %s    %s", nFound, txid.StringHex(), txid.String())
			nFound++
		}
		nTx++
		return !findFirst || nFound == 0
	}, filterSlots...)

	glb.Infof("---------\ntotal: %d transaction IDs found", nFound)
	glb.Infof("---------\ntotal: %d transaction scanned", nTx)
}
