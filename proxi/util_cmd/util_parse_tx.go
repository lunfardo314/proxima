package util_cmd

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initParseTx() *cobra.Command {
	validateLedgerIDCmd := &cobra.Command{
		Use:   "parse_tx <tx file>",
		Args:  cobra.ExactArgs(1),
		Short: fmt.Sprintf("parses transaction with ledger definitions provided in '%s'", glb.LedgerDefinitionsFileName),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runParseTx,
	}
	// 'config' / '-c' is inherited from the root command (see proxi/main.go).
	return validateLedgerIDCmd
}

func runParseTx(_ *cobra.Command, args []string) {
	txBytesWithMetadata, err := os.ReadFile(args[0])
	glb.AssertNoError(err)
	ledgerIDData, err := os.ReadFile(glb.LedgerDefinitionsFileName)
	glb.AssertNoError(err)

	ledger.MustInitLibraryCacheFromJSON(ledgerIDData)

	glb.ParseAndDisplayTxBytes(txBytesWithMetadata)
}
