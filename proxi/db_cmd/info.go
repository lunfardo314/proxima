package db_cmd

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

var slotsBack int

func initDBInfoCmd() *cobra.Command {
	dbInfoCmd := &cobra.Command{
		Use:   "info",
		Short: "displays general info of the state DB",
		Args:  cobra.NoArgs,
		Run:   runDbInfoCmd,
	}
	dbInfoCmd.PersistentFlags().IntVarP(&slotsBack, "slots", "s", -1, "maximum slots back. Default: all")

	dbInfoCmd.InitDefaultHelpCmd()
	return dbInfoCmd
}

func runDbInfoCmd(_ *cobra.Command, _ []string) {
	glb.InitLedger()
	defer glb.CloseStateStore()

	branchData := multistate.FetchLatestBranches(glb.StateStore())
	if len(branchData) == 0 {
		glb.Infof("no branches found")
		return
	}
	glb.Infof("Total %d latest branches (slot %d)", len(branchData), branchData[0].Stem.Timestamp().Slot())

	sort.Slice(branchData, func(i, j int) bool {
		return bytes.Compare(branchData[i].SequencerID[:], branchData[j].SequencerID[:]) < 0
	})

	reader, err := multistate.NewSugaredReadableState(glb.StateStore(), branchData[0].Root)
	glb.AssertNoError(err)

	id := ledger.MustLedgerIdentityDataFromBytes(reader.MustLedgerIdentityBytes())

	glb.Verbosef("\n----------------- Ledger state identity ----------------")
	glb.Verbosef("%s", id.String())
	glb.Infof("----------------- Global branch data ----------------------")
	DisplayBranchData(branchData)
	glb.Infof("\n------------- Supply and inflation summary -------------")
	summary := multistate.FetchSummarySupplyAndInflation(glb.StateStore(), slotsBack)
	glb.Infof("%s", summary.Lines("   ").String())
}

func DisplayBranchData(branches []*multistate.BranchData) {
	glb.Infof("     %-23s %-22s %-20s %-20s %-20s %-70s",
		"stemID", "name (seqID)", "supply (diluted)", "on-chain", "coverage", "root")
	glb.Infof(strings.Repeat("-", 150))
	for i, br := range branches {
		name := "(no name)"
		if msData := txbuilder.ParseMilestoneData(br.SequencerOutput.Output); msData != nil {
			name = msData.Name
		}
		name = fmt.Sprintf("%s (%s)", name, br.SequencerID.StringVeryShort())
		onChainAmount := br.SequencerOutput.Output.Amount()
		supply := br.Supply
		glb.Infof(" %2d: %20s %10s %10s %20s %20s    %-70s",
			i, br.Stem.IDShort(), name, util.GoTh(supply), util.GoTh(onChainAmount),
			util.GoTh(br.LedgerCoverage.Sum()), br.Root.String())
	}
}
