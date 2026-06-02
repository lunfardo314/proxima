package node_cmd

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initNodeGetLedgerIDCmd() *cobra.Command {
	dbInfoCmd := &cobra.Command{
		Use:   "get_ledger_definitions",
		Short: fmt.Sprintf("retrieves ledger definitions from node and saves in file '%s'", glb.LedgerDefinitionsFileName),
		Args:  cobra.NoArgs,
		Run:   dbNodeLedgerIDCmd,
	}
	dbInfoCmd.InitDefaultHelpCmd()
	return dbInfoCmd
}

func dbNodeLedgerIDCmd(_ *cobra.Command, _ []string) {
	jsonData, err := glb.GetClient().GetLedgerDefinitionJSON()
	glb.AssertNoError(err)
	err = os.WriteFile(glb.LedgerDefinitionsFileName, jsonData, 0644)
	glb.AssertNoError(err)
	glb.Infof("ledger definitions has been saved to '%s'", glb.LedgerDefinitionsFileName)
}
