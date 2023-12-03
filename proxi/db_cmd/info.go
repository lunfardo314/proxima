package db_cmd

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
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
	dbName := global.MultiStateDBName
	glb.FileMustExist(dbName)
	glb.Infof("---------------- multi-state DB info ------------------")
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	defer func() { _ = stateDb.Close() }()

	stateStore := badger_adaptor.New(stateDb)

	branchData := multistate.FetchLatestBranches(stateStore)
	if len(branchData) == 0 {
		glb.Infof("no branches found")
		return
	}
	glb.Infof("Total %d latest branches (slot %d)", len(branchData), branchData[0].Stem.Timestamp().TimeSlot())

	sort.Slice(branchData, func(i, j int) bool {
		return bytes.Compare(branchData[i].SequencerID[:], branchData[j].SequencerID[:]) < 0
	})

	reader, err := multistate.NewSugaredReadableState(stateStore, branchData[0].Root)
	glb.AssertNoError(err)

	id := genesis.MustLedgerIdentityDataFromBytes(reader.MustLedgerIdentityBytes())

	glb.Infof("\n----------------- Ledger state identity ----------------")
	glb.Infof("%s", id.String())
	glb.Infof("\n----------------- Top branch data ----------------------")
	DisplayBranchData(branchData)
	glb.Infof("\n------------- Supply and inflation summary -------------")
	summary := multistate.FetchSummarySupplyAndInflation(stateStore, slotsBack)
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
		supply := br.Stem.Output.MustStemLock().Supply
		glb.Infof(" %2d: %20s %10s %10s %20s %20s    %-70s",
			i, br.Stem.IDShort(), name, util.GoThousands(supply), util.GoThousands(onChainAmount),
			util.GoThousands(br.LedgerCoverage.Sum()), br.Root.String())
	}
}
