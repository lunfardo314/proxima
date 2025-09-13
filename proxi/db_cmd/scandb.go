package db_cmd

import (
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initScanDBCmd() *cobra.Command {
	dbScanCmd := &cobra.Command{
		Use:   "scan",
		Short: "scans multistate DB and check consistency",
		Args:  cobra.NoArgs,
		Run:   runScanDBCmd,
	}
	//dbInfoCmd.PersistentFlags().IntVarP(&slotsBackDBInfo, "slots", "s", -1, "maximum slots back. Default: all")

	dbScanCmd.InitDefaultHelpCmd()
	return dbScanCmd
}

func runScanDBCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	multistate.IterateSlotsBack(glb.StateStore(), func(slot uint32, roots []multistate.RootRecord) bool {
		branches := multistate.FetchBranchDataMulti(glb.StateStore(), roots...)
		if len(branches) == 0 {
			return true
		}
		glb.Infof("----------- slot %d: %d branches", slot, len(branches))

		for i, br := range branches {
			rdr, err := multistate.NewReadable(glb.StateStore(), br.Root)
			glb.AssertNoError(err)

			scanned := rdr.ScanState()
			inconsistencies := len(scanned.Inconsistencies) > 0 || scanned.Supply != br.Supply
			glb.Infof("%3d  %s, on all %d chains: %s, UTXOs: %d",
				i, br.LinesShort().Join(", "), len(scanned.Chains), util.Th(scanned.TotalOnChains), scanned.NumUTXOs)
			if inconsistencies {
				glb.Infof("   inconsistencies found:\n%s", scanned.Lines("        ").String())
			}
			glb.Assertf(inconsistencies == false, "-> fast fail")
		}
		return true
	})

}
