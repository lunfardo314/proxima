package db

import (
	"os"
	"strconv"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/proxi/config"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initDBDistributeCmd(dbCmd *cobra.Command) {
	dbDistributeCmd := &cobra.Command{
		Use: "distribute",
		Short: `creates initial distribution of genesis branch. 
Arguments must be a list of pairs <lockSource> <balance>`,
		Run: runDBDistributeCmd,
	}
	dbDistributeCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(dbDistributeCmd)
}

func runDBDistributeCmd(_ *cobra.Command, args []string) {
	console.Assertf(len(args) > 0 && len(args)%2 == 0, "even-sized list of arguments is expected")

	dbName := GetMultiStateStoreName()
	console.Assertf(dbName != "(not set)", "multi-state database not set")

	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	defer stateDb.Close()

	distribution := make([]txbuilder.LockBalance, len(args)/2)
	var err error
	for i := 0; i < len(args); i += 2 {
		distribution[i/2].Lock, err = core.AddressED25519FromSource(args[i])
		console.Assertf(err == nil, "%v in argument %d", err, i)
		distribution[i/2].Balance, err = strconv.ParseUint(args[i+1], 10, 64)
		console.Assertf(err == nil, "%v in argument %d", err, i)
	}

	console.Infof("Check distribution list:")
	for i := range distribution {
		console.Infof("%s -> %s", util.GoThousands(distribution[i].Balance), distribution[i].Lock.String())
	}

	if !console.YesNoPrompt("Continue?", false) {
		return
	}

	stateStore := badger_adaptor.New(stateDb)
	config.GetPrivateKey()
	txBytes, err := genesis.DistributeInitialSupply(stateStore, config.GetPrivateKey(), distribution)
	console.NoError(err)

	txStoreDb := GetTxStoreName()
	var txDB *badger.DB
	err = util.CatchPanicOrError(func() error {
		txDB = badger_adaptor.MustCreateOrOpenBadgerDB(txStoreDb)
		return nil
	})

	// TODO
	if err != nil {
		console.Infof("Warning: can't open tx store DB due to error '%v'", err)
		console.Infof("Saving distribution transaction to the file 'distribution.tx'")
		err = os.WriteFile("distribution.tx", txBytes, 0644)
		console.NoError(err)
		return
	}
	defer txDB.Close()

	err = transaction.StoreTransactionBytes(txBytes, badger_adaptor.New(txDB))
	console.NoError(err)
}
