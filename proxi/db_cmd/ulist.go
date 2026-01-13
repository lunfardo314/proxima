package db_cmd

import (
	"math"
	"strconv"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
)

func initUlistCmd() *cobra.Command {
	ulistCmd := &cobra.Command{
		Use:   "ulist <slot>",
		Short: "display outputs (UTXOs) in the main branch of the slot",
		Args:  cobra.ExactArgs(1),
		Run:   runUlist,
	}

	ulistCmd.InitDefaultHelpCmd()
	return ulistCmd
}

func runUlist(_ *cobra.Command, args []string) {
	slotInt, err := strconv.Atoi(args[0])
	glb.AssertNoError(err)
	glb.Assertf(slotInt < math.MaxUint32, "wrong slot number")
	slot := uint32(slotInt)

	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	lrb := multistate.FindLatestReliableBranch(glb.StateStore(), global.FractionHealthyBranch)
	glb.Assertf(lrb != nil, "can't find latest reliable branch")

	slotFound := false
	var root common.VCommitment
	var brID base.TransactionID

	if slot <= lrb.Slot() {
		multistate.IterateBranchChainBack(glb.StateStore(), lrb, func(branchID *base.TransactionID, branch *multistate.BranchData) bool {
			if slotFound = branchID.Slot() == slot; slotFound {
				root = branch.Root
				brID = *branchID
			}
			return !slotFound
		})
	}
	glb.Assertf(slotFound, "cannot find branch with slot %d in the main sequence of branches", slot)

	glb.Infof("baseline branch is %s (hex = %s)", brID.String(), brID.StringHex())
	glb.Infof("\nUTXOs with slot %d:\n", slot)

	rdr, err := multistate.NewReadable(glb.StateStore(), root)
	glb.AssertNoError(err)

	var o *ledger.Output
	var err1 error
	count := 0
	err = rdr.IterateUTXOsInSlot(slot, func(oid base.OutputID, oData []byte) bool {
		// CLI uses latest library version for parsing outputs
		o, err1 = ledger.OutputFromBytesAtSlot(oData, base.MaxSlot)
		glb.AssertNoError(err1)
		glb.Infof("%s", oid.String())
		if glb.IsVerbose() {
			glb.Infof("%s", o.LinesVerbose("     "))
		} else {
			glb.Infof("%s", o.Lines("     "))
		}
		count++
		return true
	})
	glb.AssertNoError(err)
	glb.Infof("-------------------\nTOTAL %d UTXOs", count)
}
