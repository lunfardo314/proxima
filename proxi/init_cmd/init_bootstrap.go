package init_cmd

import (
	"strconv"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
)

func initBootstrapAccountCmd() *cobra.Command {
	bootstrapAccountCmd := &cobra.Command{
		Use:   "bootstrap_account [amount, default 1_000_000]",
		Short: "creates transaction store DB. Creates bootstrap output on bootstrap address with specified amount",
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Args: cobra.MaximumNArgs(1),
		Run:  runBootstrapAccount,
	}
	return bootstrapAccountCmd
}

const (
	defaultBootstrapBalance = uint64(1_000_000)
	minimumBootstrapBalance = uint64(10_000)
)

func runBootstrapAccount(_ *cobra.Command, args []string) {
	// initialize ledger
	glb.FileMustExist(global.MultiStateDBName)
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName, badger.DefaultOptions(global.MultiStateDBName))
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateDb.Close() }()

	multistate.InitLedgerFromStore(stateStore)
	privKey := glb.MustGetPrivateKey()

	// create transaction store database
	glb.FileMustNotExist(global.TxStoreDBName)
	glb.Infof("Transaction store database name: '%s'", global.TxStoreDBName)

	bootstrapBalance := defaultBootstrapBalance
	if len(args) > 0 {
		b, err := strconv.Atoi(args[0])
		glb.AssertNoError(err)
		bootstrapBalance = uint64(b)
	}
	if bootstrapBalance < minimumBootstrapBalance {
		glb.Fatalf("bootstrap account balance must be at least %s", util.GoTh(minimumBootstrapBalance))
	}
	addr := glb.GetWalletData().Account
	glb.Infof("Transaction store DB will be created. %s tokens from the bootstrap chain will be sent to bootstrap address %s", util.GoTh(bootstrapBalance), addr.String())
	if !glb.YesNoPrompt("Proceed?", true) {
		glb.Fatalf("exit: bootstrap account wasn't created")
	}

	txStoreDB := badger_adaptor.New(badger_adaptor.MustCreateOrOpenBadgerDB(global.TxStoreDBName, badger.DefaultOptions(global.TxStoreDBName)))
	txStore := txstore.NewSimpleTxBytesStore(txStoreDB)
	defer func() { _ = txStoreDB.Close() }()

	txBytesBootstrapBalance, txid, err := txbuilder.DistributeInitialSupplyExt(stateStore, privKey, []ledger.LockBalance{
		{addr, bootstrapBalance},
	})
	glb.AssertNoError(err)

	rr, found := multistate.FetchRootRecord(stateStore, txid)
	glb.Assertf(found, "inconsistency: can't find root record")
	_, err = txStore.PersistTxBytesWithMetadata(txBytesBootstrapBalance, &txmetadata.TransactionMetadata{
		StateRoot:      rr.Root,
		LedgerCoverage: util.Ref(rr.LedgerCoverage.LatestDelta()),
		SlotInflation:  util.Ref(rr.SlotInflation),
		Supply:         util.Ref(rr.Supply),
	})
	glb.AssertNoError(err)
	util.Assertf(len(txStore.GetTxBytesWithMetadata(&txid)) > 0, "inconsistency: just stored transaction has not been found")

	glb.Infof("Success. Bootstrap account has been created in the transaction %s", txid.String())
}
