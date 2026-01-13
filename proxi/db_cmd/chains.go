package db_cmd

import (
	"bytes"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initChainsCmd() *cobra.Command {
	dbChainsCmd := &cobra.Command{
		Use:   "chains",
		Short: "list all chain outputs in the LRB state",
		Args:  cobra.NoArgs,
		Run:   runChainsCmd,
	}
	//dbChainsCmd.PersistentFlags().StringP("output", "o", "", "output file")

	dbChainsCmd.InitDefaultHelpCmd()
	return dbChainsCmd
}

func runChainsCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	branchData := multistate.FindLatestReliableBranch(glb.StateStore(), global.FractionHealthyBranch)
	if branchData == nil {
		glb.Infof("no branches found")
		return
	}

	accountInfo := multistate.MustCollectAccountInfo(glb.StateStore(), branchData.Root)

	glb.Infof("---------------- global LRB state ------------------")
	glb.Infof("supply:   %s     coverage delta: %s     slot inflation: %s", util.Th(branchData.Supply), util.Th(branchData.CoverageDelta),
		util.Th(branchData.SlotInflation))

	glb.Infof("---------------- chain infos in the LRB state ------------------")
	glb.Infof("Chains: %d", len(accountInfo.ChainRecords))
	chainIDSSorted := util.KeysSorted(accountInfo.ChainRecords, func(k1, k2 base.ChainID) bool {
		return bytes.Compare(k1[:], k2[:]) < 0
	})

	sum := uint64(0)
	for _, chainID := range chainIDSSorted {
		ci := accountInfo.ChainRecords[chainID]
		glb.Infof("   %s :: %s :: %s   seq=%v branch=%v",
			chainID.String(), ci.Output.ID.String(), util.Th(ci.Balance), ci.Output.ID.IsSequencerTransaction(), ci.Output.ID.IsBranchTransaction())
		if glb.IsVerbose() {
			// CLI uses latest library version for parsing outputs
			o, _ := ledger.OutputFromBytesAtSlot(ci.Output.Data, base.MaxSlot)
			lines := o.Lines("            ::")
			glb.Infof("     Output details:")
			glb.Infof(lines.String())
		}
		sum += ci.Balance
	}
	glb.Infof("--------------------------------")
	glb.Infof("   Total on chains: %s", util.Th(sum))
	glb.Infof("--------Stem Output-------------")
	rdr := multistate.MustNewSugaredReadableState(glb.StateStore(), branchData.Root, 0)
	stem := rdr.GetStemOutput()
	lines := stem.LinesSource("  ")
	glb.Infof(lines.String())

}
