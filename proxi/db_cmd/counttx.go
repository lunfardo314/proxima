package db_cmd

import (
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initCountTxCmd() *cobra.Command {
	dbCountTxCmd := &cobra.Command{
		Use:   "counttx",
		Short: "counts all transactions in the LRB",
		Args:  cobra.NoArgs,
		Run:   runCountTx,
	}
	dbCountTxCmd.InitDefaultHelpCmd()
	return dbCountTxCmd
}

func runCountTx(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	store := glb.StateStore()
	lrb := multistate.FindLatestReliableBranch(store, global.FractionHealthyBranch)
	glb.Assertf(lrb != nil, "can't find latest reliable branch (LRB)")

	rdr, err := multistate.NewReadable(store, lrb.Root)
	glb.AssertNoError(err)
	count := 0
	countSeq := 0
	countBranch := 0
	rdr.IterateKnownCommittedTransactions(func(txid base.TransactionID, _ uint32) bool {
		count++
		if txid.IsSequencerTransaction() {
			countSeq++
			if txid.IsBranchTransaction() {
				countBranch++
			}
		}
		if count%100_000 == 0 {
			glb.Infof("tx count: %10d", count)
		}
		return true
	})
	glb.Infof("tx count:         %10d", count)
	glb.Infof("branch sequencer: %10d", countSeq)
	glb.Infof("branch count:     %10d", countBranch)
}
