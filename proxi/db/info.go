package db

import (
	"bytes"
	"sort"

	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/console"
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
	console.Infof("---------------- multi-state DB info ------------------")
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
		console.Infof("no branches found")
		return
	}
	console.Infof("Total %d latest branches (slot %d)", len(branchData), branchData[0].Stem.Timestamp().TimeSlot())

	sort.Slice(branchData, func(i, j int) bool {
		return bytes.Compare(branchData[i].SequencerID[:], branchData[j].SequencerID[:]) < 0
	})

	console.Infof(" ##: stemID, seqID, root\n------------------------------------------------------")
	for i, br := range branchData {
		console.Infof(" %2d: %s, %s, %s", i, br.Stem.IDShort(), br.SequencerID.Short(), br.Root.String())
	}

	reader, err := multistate.NewSugaredReadableState(stateStore, branchData[0].Root)
	console.NoError(err)

	id := genesis.MustStateIdentityDataFromBytes(reader.StateIdentityBytes())

	console.Infof("\n----------------- Ledger state identity ----------------")
	console.Infof("%s", id.String())
}
