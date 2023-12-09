package db_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

var outputFile string

const defaultMaxSlotsBack = 100

func initDBTreeCmd() *cobra.Command {
	dbTreeCmd := &cobra.Command{
		Use:   fmt.Sprintf("tree [max slots back, default %d]", defaultMaxSlotsBack),
		Short: "create .DOT file for the tree of all branches",
		Args:  cobra.MaximumNArgs(1),
		Run:   runDbTreeCmd,
	}
	dbTreeCmd.PersistentFlags().StringVarP(&outputFile, "output", "o", "", "output file")

	dbTreeCmd.InitDefaultHelpCmd()
	return dbTreeCmd
}

func runDbTreeCmd(_ *cobra.Command, args []string) {
	dbName := global.MultiStateDBName
	pwdPath, err := os.Getwd()
	glb.AssertNoError(err)
	currentWorkingDir := filepath.Base(pwdPath)

	outFile := outputFile
	if outFile == "" {
		outFile = dbName + "_TREE_" + currentWorkingDir
	}

	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	defer stateDb.Close()

	stateStore := badger_adaptor.New(stateDb)

	numSlotsBack := defaultMaxSlotsBack
	if len(args) == 0 {
		utangle.SaveTree(stateStore, outFile, numSlotsBack)
	} else {
		var err error
		numSlotsBack, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
		utangle.SaveTree(stateStore, outFile, numSlotsBack)
	}
	glb.Infof("branch tree has been store in .DOT format in the file '%s', %d slots back", outFile, numSlotsBack)
}
