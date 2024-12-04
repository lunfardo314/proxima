package node_cmd

import (
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initLastSeqCmd() *cobra.Command {
	getPeersInfoCmd := &cobra.Command{
		Use:   "last_seq",
		Short: `retrieves list of latest known sequncer milestones from the node`,
		Args:  cobra.NoArgs,
		Run:   runLastMilestonesCmd,
	}

	getPeersInfoCmd.InitDefaultHelpCmd()
	return getPeersInfoCmd
}

func runLastMilestonesCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	//
	lastSeq, err := glb.GetClient().GetLastKnownSequencerData()
	glb.AssertNoError(err)

	for _, seqID := range util.KeysSorted(lastSeq, util.StringsLess) {
		chainID, err := ledger.ChainIDFromHexString(seqID)
		glb.AssertNoError(err)

		sd := lastSeq[seqID]
		txid, err := ledger.TransactionIDFromHexString(sd.LatestMilestoneTxID)
		glb.AssertNoError(err)
		activity := time.Since(time.Unix(0, sd.LastActivityUnixNano))

		var branchID ledger.TransactionID
		if sd.LastBranchTxID != "" {
			branchID, err = ledger.TransactionIDFromHexString(sd.LastBranchTxID)
			glb.AssertNoError(err)
			glb.Infof("    %s :    %s   %5d  %v   %s", chainID, txid, sd.MilestoneCount, activity, branchID.StringShort())
		} else {
			glb.Infof("    %s :    %s   %5d  %v", chainID, txid, sd.MilestoneCount, activity)
		}
	}
}
