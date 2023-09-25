package db

import (
	"bytes"
	"sort"

	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
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

	glb.Infof(" ##: stemID, seqID, root, coverage\n------------------------------------------------------")
	for i, br := range branchData {
		glb.Infof(" %2d: %s, %s, %s, %s",
			i, br.Stem.IDShort(), br.SequencerID.Short(), br.Root.String(), util.GoThousands(br.Coverage))
	}

	reader, err := multistate.NewSugaredReadableState(stateStore, branchData[0].Root)
	glb.AssertNoError(err)

	id := genesis.MustStateIdentityDataFromBytes(reader.MustStateIdentityBytes())

	glb.Infof("\n----------------- Ledger state identity ----------------")
	glb.Infof("%s", id.String())
}
