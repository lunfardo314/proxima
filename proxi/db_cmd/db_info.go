package db_cmd

import (
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initDBInfoCmd(dbCmd *cobra.Command) {
	dbInfoCmd := &cobra.Command{
		Use:   "info",
		Short: "displays general info of the state DB",
		Args:  cobra.NoArgs,
		Run:   runDbInfoCmd,
	}
	dbInfoCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(dbInfoCmd)
}

func runDbInfoCmd(_ *cobra.Command, _ []string) {
	displayNames()

	stateDB := GetStateStoreName()
	if stateDB == "(not set)" {
		return
	}
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(stateDB)
	defer stateDb.Close()

	stateStore := badger_adaptor.New(stateDb)

	latestSlot := multistate.FetchLatestSlot(stateStore)
	console.Infof("Latest slot: %d", latestSlot)
}
