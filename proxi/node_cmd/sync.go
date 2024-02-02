package node_cmd

import (
	"bytes"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initSyncInfoCmd() *cobra.Command {
	getSyncInfoCmd := &cobra.Command{
		Use:   "sync",
		Short: `retrieves sync info from the node`,
		Args:  cobra.NoArgs,
		Run:   runSyncInfoCmd,
	}

	getSyncInfoCmd.InitDefaultHelpCmd()
	return getSyncInfoCmd
}

func runSyncInfoCmd(_ *cobra.Command, _ []string) {
	syncInfo, err := getClient().GetSyncInfo()
	glb.AssertNoError(err)
	glb.Infof("  node synced: %v", syncInfo.Synced)
	glb.Infof("  in the sync window: %v", syncInfo.InSyncWindow)
	glb.Infof("  activity by sequencer:")
	sorted := util.KeysSorted(syncInfo.PerSequencer, func(k1, k2 ledger.ChainID) bool {
		return bytes.Compare(k1[:], k2[:]) < 0
	})
	for _, seqID := range sorted {
		si := syncInfo.PerSequencer[seqID]
		seenBack := time.Since(ledger.MustNewLedgerTime(ledger.Slot(si.LatestSeenSlot), 0).Time())
		bookedBack := time.Since(ledger.MustNewLedgerTime(ledger.Slot(si.LatestBookedSlot), 0).Time())
		active := seenBack < ledger.SlotDuration()
		glb.Infof("        %s : active/synced: %v/%v, last seen slot: %d (%v back), last booked slot: %d (%v back)",
			seqID.StringShort(), active, si.Synced, si.LatestSeenSlot, seenBack, si.LatestBookedSlot, bookedBack)
	}
}
