package db

import (
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

var outputFile string

func initDBTreeCmd(dbCmd *cobra.Command) {
	dbTreeCmd := &cobra.Command{
		Use:   "tree",
		Short: "create .DOT file for the tree of all branches",
		Args:  cobra.NoArgs,
		Run:   runDbTreeCmd,
	}
	dbTreeCmd.PersistentFlags().StringVarP(&outputFile, "output", "o", "", "output file")

	dbTreeCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(dbTreeCmd)
}

func runDbTreeCmd(_ *cobra.Command, _ []string) {
	dbName := GetMultiStateStoreName()
	if dbName == "(not set)" {
		return
	}
	outFile := outputFile
	if outFile == "" {
		outFile = dbName + "_TREE"
	}
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	defer stateDb.Close()

	stateStore := badger_adaptor.New(stateDb)

	utangle.SaveTree(stateStore, outFile)
}
