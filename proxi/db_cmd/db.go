// Package db_cmd hosts the `proxi db <subcommand>` CLI.
//
// SINGLETON-DEPENDENT BY DESIGN: every subcommand here opens the local
// BadgerDB directly (no node API) and walks the multistate via typed
// parsers that need ledger.L() initialised. There is no node to fetch
// /ledger_constants from, so these commands legitimately stay on the
// singleton path. Not part of the wasm-style refactor.
package db_cmd

import (
	"github.com/lunfardo314/proxima/proxi/db_cmd/txstore"
	"github.com/spf13/cobra"
)

func Init() *cobra.Command {
	dbCmd := &cobra.Command{
		Use:   "db [<subcommand>]",
		Short: "specifies subcommand on the database",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { _ = cmd.Help() },
	}

	dbCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(
		initDBInfoCmd(),
		initMainChainCmd(),
		initAccountsCmd(),
		initBranchesCmd(),
		initReliableBranchCmd(),
		txstore.Init(),
		initChainsCmd(),
		initFindTxCmd(),
		initDbGetLedgerIDCmd(),
		initUlistCmd(),
		//initScanDBCmd(),
		//initDbStatsCmd(),
		initDbChainStatsCmd(),
		initAnalyzeBranchesCmd(),
		initCountTxCmd(),
		initUpgradesCmd(),
	)
	return dbCmd
}
