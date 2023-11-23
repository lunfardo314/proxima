package db

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/proxi_old/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	initSupply  uint64
	description string
	nowis       time.Time
)

func initDbGenesis(dbCmd *cobra.Command) {
	genesisCmd := &cobra.Command{
		Use:   "genesis [--state_db] [--tx_store_db] [--supply <supply>] [--desc 'description']",
		Short: "creates genesis ledger state database and transaction store database",
		Args:  cobra.NoArgs,
		Run:   runGenesis,
	}
	nowis = time.Now()
	genesisCmd.Flags().Uint64Var(&initSupply, "supply", genesis.DefaultSupply, fmt.Sprintf("initial supply (default is %s", util.GoThousands(genesis.DefaultSupply)))
	defaultDesc := fmt.Sprintf("genesis has been created at Unix time (nanoseconds) %d", nowis.UnixNano())
	genesisCmd.Flags().StringVar(&description, "desc", defaultDesc, fmt.Sprintf("default is '%s'", defaultDesc))

	dbCmd.AddCommand(genesisCmd)
}

func runGenesis(_ *cobra.Command, _ []string) {
	walletData := glb.GetWalletData()

	dbName := viper.GetString(general.ConfigKeyMultiStateDbName)
	util.Assertf(dbName != "", "genesis database name not set")

	txStoreName := viper.GetString(general.ConfigKeyTxStoreName)
	if txStoreName == "" {
		txStoreName = dbName + ".txstore"
	}

	mustNotExist(dbName)
	mustNotExist(txStoreName)

	libraryHash := easyfl.LibraryHash()

	glb.Infof("Creating genesis ledger state...")
	glb.Infof("Multi-state database : %s", dbName)
	glb.Infof("Transaction store database : %s", txStoreName)
	glb.Infof("Initial supply: %s", util.GoThousands(initSupply))
	glb.Infof("Description: '%s'", description)
	nowisTs := core.LogicalTimeFromTime(nowis)
	glb.Infof("Genesis time slot: %d", nowisTs.TimeSlot())
	glb.Infof("Genesis controller address: %s", walletData.Account.String())
	glb.Infof("Constraint library hash: %s", hex.EncodeToString(libraryHash[:]))

	if !glb.YesNoPrompt(fmt.Sprintf("Create Proxima genesis '%s' and transactions store '%s'?", dbName, txStoreName), true) {
		glb.Fatalf("exit: genesis database wasn't created")
	}
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(dbName, badger.DefaultOptions(dbName))
	stateStore := badger_adaptor.New(stateDb)

	bootstrapChainID, _ := genesis.InitLedgerState(genesis.StateIdentityData{
		Description:                description,
		InitialSupply:              initSupply,
		GenesisControllerPublicKey: walletData.PrivateKey.Public().(ed25519.PublicKey),
		BaselineTime:               core.BaselineTime,
		TimeTickDuration:           core.TimeTickDuration(),
		MaxTimeTickValueInTimeSlot: core.TimeTicksPerSlot - 1,
		GenesisTimeSlot:            core.LogicalTimeFromTime(nowis).TimeSlot(),
		CoreLibraryHash:            libraryHash,
	}, stateStore)
	glb.AssertNoError(stateDb.Close())

	glb.Infof("Genesis state DB '%s' has been created successfully.\nBootstrap sequencer chainID: %s", dbName, bootstrapChainID.String())

	txStoreDB := badger_adaptor.MustCreateOrOpenBadgerDB(txStoreName, badger.DefaultOptions(txStoreName))
	glb.AssertNoError(txStoreDB.Close())

	glb.Infof("Transaction store DB '%s' has been created successfully", dbName)

	glb.SetKeyValue(general.ConfigKeyMultiStateDbName, dbName)
	glb.SetKeyValue(general.ConfigKeyTxStoreName, txStoreName)
}

func mustNotExist(dir string) {
	_, err := os.Stat(dir)
	if err == nil {
		glb.Fatalf("'%s' already exists", dir)
	} else {
		if !os.IsNotExist(err) {
			glb.AssertNoError(err)
		}
	}
}
