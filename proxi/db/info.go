package db

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initDBInfoCmd(dbCmd *cobra.Command) {
	dbInfoCmd := &cobra.Command{
		Use:   "info",
		Short: "displays general info of the state DB",
		Args:  cobra.NoArgs,
		Run:   runDbInfoCmd,
	}
	dbInfoCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(dbInfoCmd)
}

func runDbInfoCmd(_ *cobra.Command, _ []string) {
	glb.Infof("---------------- multi-state DB info ------------------")
	displayDBNames()

	dbName := GetMultiStateStoreName()
	if dbName == "(not set)" {
		return
	}
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	defer stateDb.Close()

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

	DisplayBranchData(branchData)

	reader, err := multistate.NewSugaredReadableState(stateStore, branchData[0].Root)
	glb.AssertNoError(err)

	id := genesis.MustStateIdentityDataFromBytes(reader.MustStateIdentityBytes())

	glb.Verbosef("\n----------------- Ledger state identity ----------------")
	glb.Verbosef("%s", id.String())
}

func DisplayBranchData(branches []*multistate.BranchData) {
	glb.Infof("     %-23s %-20s %-20s %-20s %-70s",
		"stemID", "name (seqID)", "on-chain", "coverage", "root")
	glb.Infof(strings.Repeat("-", 150))
	for i, br := range branches {
		name := "(no name)"
		if msData := sequencer.ParseMilestoneData(br.SeqOutput.Output); msData != nil {
			name = msData.Name
		}
		name = fmt.Sprintf("%s (%s)", name, br.SequencerID.VeryShort())
		onChainAmount := br.SeqOutput.Output.Amount()
		glb.Infof(" %2d: %20s %10s %20s %20s    %-70s",
			i, br.Stem.IDShort(), name, util.GoThousands(onChainAmount), util.GoThousands(br.Coverage), br.Root.String())
	}
}
