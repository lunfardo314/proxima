package db_cmd

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

var (
	outputFile  string
	outputFname string
	slotsBack   uint16
)

func initMainChainCmd() *cobra.Command {
	dbMainChainCmd := &cobra.Command{
		Use:   "mainchain_stats",
		Short: "calculates frquencies of sequencers along the main chain of branches from the DB",
		Args:  cobra.NoArgs,
		Run:   runMainChainCmd,
	}
	dbMainChainCmd.PersistentFlags().StringVarP(&outputFile, "output", "o", "", "output file")
	dbMainChainCmd.PersistentFlags().Uint16VarP(&slotsBack, "slots_back", "s", 1000, "limit maximum how many slots back")

	dbMainChainCmd.InitDefaultHelpCmd()
	return dbMainChainCmd
}

func runMainChainCmd(_ *cobra.Command, _ []string) {
	makeFile := outputFname != ""

	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	var mainBranches []*multistate.BranchData
	if slotsBack == 0 {
		mainBranches = multistate.FetchHeaviestBranchChainNSlotsBack(glb.StateStore(), -1)
	} else {
		mainBranches = multistate.FetchHeaviestBranchChainNSlotsBack(glb.StateStore(), int(slotsBack))
	}

	if makeFile {
		outFile, err := os.Create(outputFname + ".branches")
		glb.AssertNoError(err)

		for _, bd := range mainBranches {
			_, _ = fmt.Fprintf(outFile, "%s, %d, %s, %s\n",
				bd.SequencerID.String(), bd.CoverageDelta, bd.Stem.ID.String(), util.Th(bd.SequencerOutput.Output.TokenBalance()))
		}
	}
	type seqData struct {
		numOccurrences int
		onChainBalance uint64
		name           string
	}
	bySeqID := make(map[base.ChainID]seqData)

	for _, bd := range mainBranches {
		sd := bySeqID[bd.SequencerID]
		sd.numOccurrences++
		if sd.onChainBalance == 0 {
			sd.onChainBalance = bd.SequencerOutput.Output.TokenBalance()
		}
		if sd.name == "" {
			if md, err := ledger.ParseSequencerData(bd.SequencerOutput.Output); err == nil {
				sd.name = md.Name()
			}
		}
		bySeqID[bd.SequencerID] = sd
	}
	sorted := util.KeysSorted(bySeqID, func(k1, k2 base.ChainID) bool {
		return bySeqID[k1].onChainBalance > bySeqID[k2].onChainBalance
	})
	glb.Infof("stats by sequencer id:")
	for _, k := range sorted {
		sd := bySeqID[k]
		glb.Infof("%10s %s  %8d (%2d%%)       %s", sd.name, k.String(),
			sd.numOccurrences, (100*sd.numOccurrences)/len(mainBranches), util.Th(sd.onChainBalance))
	}
}
