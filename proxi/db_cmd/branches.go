package db_cmd

import (
	"sort"
	"strconv"

	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initBranchesCmd() *cobra.Command {
	branchesCmd := &cobra.Command{
		Use:   "branches [<n slots back>]",
		Short: "displays latest branch records",
		Args:  cobra.MaximumNArgs(1),
		Run:   runBranchesCmd,
	}
	branchesCmd.InitDefaultHelpCmd()
	return branchesCmd
}

func runBranchesCmd(_ *cobra.Command, args []string) {
	glb.InitLedger()
	defer glb.CloseDatabases()

	const defaultLastNSlots = 5

	lastNSlots := defaultLastNSlots
	var err error
	if len(args) > 0 {
		lastNSlots, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
		if lastNSlots < 1 {
			lastNSlots = defaultLastNSlots
		}
	}
	glb.Infof("displaying branch info of last %d slots back", lastNSlots)
	rootRecords := multistate.FetchRootRecordsNSlotsBack(glb.StateStore(), lastNSlots)
	branchData := multistate.FetchBranchDataMulti(glb.StateStore(), rootRecords...)
	glb.Assertf(len(branchData) > 0, "no branches have been found")

	sort.Slice(branchData, func(i, j int) bool {
		return branchData[i].Stem.Timestamp().After(branchData[j].Stem.Timestamp())
	})

	for i, bd := range branchData {
		txid := bd.Stem.ID.TransactionID()
		glb.Infof("%3d: %18s   numTx: %d, seqID: %s, root: %s",
			i,
			txid.StringShort(),
			bd.NumTransactions,
			bd.SequencerID.String(),
			bd.Root.String(),
		)
	}
}
