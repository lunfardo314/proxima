package db_cmd

import (
	"bytes"
	"encoding/hex"
	"sort"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

var slotsBackDBInfo int

func initDBInfoCmd() *cobra.Command {
	dbInfoCmd := &cobra.Command{
		Use:   "info",
		Short: "displays general info of the state DB",
		Args:  cobra.NoArgs,
		Run:   runDbInfoCmd,
	}
	dbInfoCmd.PersistentFlags().IntVarP(&slotsBackDBInfo, "slots", "s", -1, "maximum slots back. Default: all")

	dbInfoCmd.InitDefaultHelpCmd()
	return dbInfoCmd
}

func runDbInfoCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	branchData := multistate.FetchLatestBranches(glb.StateStore())
	if len(branchData) == 0 {
		glb.Infof("no branches found")
		return
	}
	glb.Infof("Total %d branches in the latest slot %d", len(branchData), branchData[0].Stem.Timestamp().Slot)

	sort.Slice(branchData, func(i, j int) bool {
		return bytes.Compare(branchData[i].SequencerID[:], branchData[j].SequencerID[:]) < 0
	})

	reader, err := multistate.NewSugaredReadableState(glb.StateStore(), branchData[0].Root)
	glb.AssertNoError(err)

	identityYAML := reader.MustLedgerIdentityBytes()
	lib, err := ledger.ParseLibraryFromYAML(identityYAML, ledger.GetEmbeddedFunctionResolver)
	glb.AssertNoError(err)

	earliestSlot := multistate.FetchEarliestSlot(glb.StateStore())
	glb.Infof("ledger time now is %s, earliest committed slot is %d", ledger.TimeNow().String(), earliestSlot)

	h := lib.LibraryHash()
	constants := ledger.ConstantsFromLibrary(lib)

	glb.Infof("ledger library hash: %s", hex.EncodeToString(h[:]))
	glb.Verbosef("\n----------------- Ledger state identity ----------------")
	glb.Verbosef("%s", constants.String())
	glb.Infof("----------------- branch data ----------------------")
	for i, br := range branchData {
		glb.Infof("%3d %s", i, br.LinesShort().Join(", "))
	}
	glb.Infof("\n------------- Supply and inflation summary -------------")
	summary := multistate.FetchSummarySupply(glb.StateStore(), slotsBackDBInfo)
	glb.Infof("%s", summary.Lines("   ").String())
}
