package db

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initMainChainCmd(dbCmd *cobra.Command) {
	dbMainChainCmd := &cobra.Command{
		Use:   "mainchain",
		Short: "outputs main chain of branches from the DB",
		Args:  cobra.MaximumNArgs(1),
		Run:   runMainChainCmd,
	}
	dbMainChainCmd.PersistentFlags().StringP("output", "o", "", "output file")

	dbMainChainCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(dbMainChainCmd)
}

func runMainChainCmd(_ *cobra.Command, args []string) {
	fname := viper.GetString("output")

	makeFile := fname != ""
	if makeFile {
		glb.Infof("output file is %s", fname)
	} else {
		glb.Infof("main chain data wont be written to a file")
	}

	dbName := GetMultiStateStoreName()
	if dbName == "(not set)" {
		makeFile = false
	}
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	defer stateDb.Close()

	stateStore := badger_adaptor.New(stateDb)

	mainBranches := multistate.FetchHeaviestBranchChainNSlotsBack(stateStore, -1)
	if makeFile {
		outFile, err := os.Create(fname + ".branches")
		glb.AssertNoError(err)

		for _, bd := range mainBranches {
			_, _ = fmt.Fprintf(outFile, "%s, %d, %s, %s\n",
				bd.SequencerID.String(), bd.Coverage, bd.Stem.ID.String(), util.GoThousands(bd.SeqOutput.Output.Amount()))
		}
	}
	type seqData struct {
		numOccurrences int
		onChainBalance uint64
	}
	bySeqID := make(map[core.ChainID]seqData)

	for _, bd := range mainBranches {
		sd := bySeqID[bd.SequencerID]
		sd.numOccurrences++
		sd.onChainBalance = bd.SeqOutput.Output.Amount()
		bySeqID[bd.SequencerID] = sd
	}
	sorted := util.SortKeys(bySeqID, func(k1, k2 core.ChainID) bool {
		return bySeqID[k1].onChainBalance > bySeqID[k2].onChainBalance
	})
	glb.Infof("stats by sequencer ID:")
	for _, k := range sorted {
		sd := bySeqID[k]
		glb.Infof("%s  %8d       %d", k.Short(), sd.numOccurrences, sd.onChainBalance)
	}

}
