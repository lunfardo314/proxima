package db_cmd

import (
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initAccountsCmd() *cobra.Command {
	accountsCmd := &cobra.Command{
		Use:   "accounts",
		Short: "displays totals of accounts from the state DB",
		Args:  cobra.NoArgs,
		Run:   runAccountsCmd,
	}
	accountsCmd.InitDefaultHelpCmd()
	return accountsCmd
}

func runAccountsCmd(_ *cobra.Command, _ []string) {
	glb.Infof("---------------- account totals at the heaviest branch ------------------")
	dbName := general.MultiStateDBName
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
