package db_cmd

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initMainChainCmd() *cobra.Command {
	dbMainChainCmd := &cobra.Command{
		Use:   "mainchain",
		Short: "outputs main chain of branches from the DB",
		Args:  cobra.MaximumNArgs(1),
		Run:   runMainChainCmd,
	}
	dbMainChainCmd.PersistentFlags().StringP("output", "o", "", "output file")

	dbMainChainCmd.InitDefaultHelpCmd()
	return dbMainChainCmd
}

func runMainChainCmd(_ *cobra.Command, args []string) {
	fname := viper.GetString("output")

	makeFile := fname != ""

	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	mainBranches := multistate.FetchHeaviestBranchChainNSlotsBack(glb.StateStore(), -1)
	if makeFile {
		outFile, err := os.Create(fname + ".branches")
		glb.AssertNoError(err)

		for _, bd := range mainBranches {
			_, _ = fmt.Fprintf(outFile, "%s, %d, %s, %s\n",
				bd.SequencerID.String(), bd.LedgerCoverage, bd.Stem.ID.String(), util.Th(bd.SequencerOutput.Output.Amount()))
		}
	}
	type seqData struct {
		numOccurrences int
		onChainBalance uint64
		name           string
	}
	bySeqID := make(map[ledger.ChainID]seqData)

	for _, bd := range mainBranches {
		sd := bySeqID[bd.SequencerID]
		sd.numOccurrences++
		if sd.onChainBalance == 0 {
			sd.onChainBalance = bd.SequencerOutput.Output.Amount()
		}
		if sd.name == "" {
			if md := ledger.ParseMilestoneData(bd.SequencerOutput.Output); md != nil {
				sd.name = md.Name
			}
		}
		bySeqID[bd.SequencerID] = sd
	}
	sorted := util.KeysSorted(bySeqID, func(k1, k2 ledger.ChainID) bool {
		return bySeqID[k1].onChainBalance > bySeqID[k2].onChainBalance
	})
	glb.Infof("stats by sequencer ID:")
	for _, k := range sorted {
		sd := bySeqID[k]
		glb.Infof("%10s %s  %8d (%2d%%)       %s", sd.name, k.StringShort(),
			sd.numOccurrences, (100*sd.numOccurrences)/len(mainBranches), util.Th(sd.onChainBalance))
	}
}
