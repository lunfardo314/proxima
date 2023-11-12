package db

import (
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initAccountsCmd(dbCmd *cobra.Command) {
	accountsCmd := &cobra.Command{
		Use:   "accounts",
		Short: "displays totals of accounts from the state DB",
		Args:  cobra.NoArgs,
		Run:   runAccountsCmd,
	}
	accountsCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(accountsCmd)
}

func runAccountsCmd(_ *cobra.Command, _ []string) {
	glb.Infof("---------------- account totals at the heaviest branch ------------------")
	displayDBNames()

	dbName := GetMultiStateStoreName()
	if dbName == "(not set)" {
		return
	}
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	defer func() { _ = stateDb.Close() }()

	stateStore := badger_adaptor.New(stateDb)

	branchData := multistate.FetchLatestBranches(stateStore)
	if len(branchData) == 0 {
		glb.Infof("no branches found")
		return
	}

	brHeaviest := util.Maximum(branchData, func(br1, br2 *multistate.BranchData) bool {
		return br1.LedgerCoverage.Sum() < br2.LedgerCoverage.Sum()
	})

	accountInfo := multistate.MustCollectAccountInfo(stateStore, brHeaviest.Root)
	glb.Infof("%s\n", accountInfo.Lines("   ").String())
}
