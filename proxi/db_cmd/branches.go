package db_cmd

import (
	"strconv"
	"strings"

	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initBranchesCmd() *cobra.Command {
	branchesCmd := &cobra.Command{
		Use:   "branches [<slot from> [<N slots>]]",
		Short: "displays branch records in the slot and N non-empty slots back",
		Args:  cobra.RangeArgs(0, 2),
		Run:   runBranchesCmd,
	}
	branchesCmd.InitDefaultHelpCmd()
	return branchesCmd
}

func runBranchesCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	latestSlot := multistate.FetchLatestCommittedSlot(glb.StateStore())
	glb.Infof("latest committed slot is %d", latestSlot)

	var slot int
	nSlots := 1
	var err error
	slot = int(latestSlot)
	if len(args) > 0 {
		if !strings.Contains(args[0], "latest") {
			slot1, err := strconv.Atoi(args[0])
			glb.AssertNoError(err)
			if slot1 > int(latestSlot) {
				slot = int(latestSlot)
			} else {
				slot = slot1
			}
		}
	}
	if len(args) > 1 {
		nSlots, err = strconv.Atoi(args[1])
		glb.AssertNoError(err)
		glb.Assertf(nSlots > 0, "wrong second parameter")
		if nSlots > int(latestSlot)+1 {
			nSlots = int(latestSlot + 1)
		}
	}
	for i := 0; i < nSlots; i++ {
		s := uint32(slot - i)
		rootRecords := multistate.FetchRootRecords(glb.StateStore(), s)
		branches := multistate.FetchBranchDataMulti(glb.StateStore(), rootRecords...)
		if len(branches) == 0 {
			continue
		}
		glb.Infof("=== slot %d, number of branches %d ===", s, len(branches))
		for _, branch := range branches {
			if glb.IsVerbose() {
				glb.Infof("------\n%s", branch.LinesVerbose("    ").String())
			} else {
				glb.Infof("------\n%s", branch.Lines("    ").String())
			}
		}
	}
}
