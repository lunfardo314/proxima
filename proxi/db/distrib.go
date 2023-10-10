package db

import (
	"strconv"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/txstore"
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
	glb.Assertf(len(args) > 0 && len(args)%2 == 0, "even-sized list of arguments is expected")

	dbName := GetMultiStateStoreName()
	glb.Assertf(dbName != "(not set)", "multi-state database not set")
	glb.Infof("Multi-state database: %s", dbName)

	txDBName := GetTxStoreName()
	glb.Infof("Transaction store database: %s", txDBName)

	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	defer stateDb.Close()

	distribution := make([]genesis.LockBalance, len(args)/2)
	var err error
	for i := 0; i < len(args); i += 2 {
		distribution[i/2].Lock, err = core.AddressED25519FromSource(args[i])
		glb.Assertf(err == nil, "%v in argument %d", err, i)
		distribution[i/2].Balance, err = strconv.ParseUint(args[i+1], 10, 64)
		glb.Assertf(err == nil, "%v in argument %d", err, i)
	}

	stateStore := badger_adaptor.New(stateDb)
	stateID, _, err := genesis.ScanGenesisState(stateStore)
	glb.AssertNoError(err)

	glb.Infof("Re-check the distribution list:")
	totalToDistribute := uint64(0)
	for i := range distribution {
		glb.Infof("%s -> %s", util.GoThousands(distribution[i].Balance), distribution[i].Lock.String())
		glb.Assertf(stateID.InitialSupply-distribution[i].Balance > totalToDistribute, "wrong distribution sum")
		totalToDistribute += distribution[i].Balance
	}
	glb.Infof("Total to distribute: %s", util.GoThousands(totalToDistribute))
	glb.Infof("Total initial supply: %s", util.GoThousands(stateID.InitialSupply))
	glb.Infof("Will remain on origin account: %s", util.GoThousands(stateID.InitialSupply-totalToDistribute))

	if !glb.YesNoPrompt("Continue?", false) {
		glb.Infof("Exit. Genesis state hasn't been modified")
		return
	}

	walletData := glb.GetWalletData()
	txBytes, err := txbuilder.DistributeInitialSupply(stateStore, walletData.PrivateKey, distribution)
	glb.AssertNoError(err)

	txID, _, err := transaction.IDAndTimestampFromTransactionBytes(txBytes)
	glb.AssertNoError(err)

	glb.Infof("New branch has been created. Distribution transaction ID: %s", txID.String())
	fname := txID.AsFileName()
	glb.Infof("Saving distribution transaction to the file '%s'", fname)
	err = transaction.SaveTransactionAsFile(txBytes, fname)
	glb.AssertNoError(err)
	glb.Infof("Success")

	if txDBName == "(not set)" {
		return
	}

	glb.Infof("Storing transaction into DB '%s'...", txDBName)

	// try to save distribution transaction in the transaction store
	var txDB *badger.DB
	err = util.CatchPanicOrError(func() error {
		txDB = badger_adaptor.MustCreateOrOpenBadgerDB(txDBName)
		return nil
	})

	if err != nil {
		glb.Infof("Warning: can't open tx store DB due to error '%v'", err)
		return
	}
	defer txDB.Close()

	err = txstore.NewSimpleTxBytesStore(badger_adaptor.New(txDB)).SaveTxBytes(txBytes)
	glb.AssertNoError(err)

	glb.Infof("Success")
}
