package info_cmd

import (
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/proxi/config"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	supply      uint64
	description string
	nowis       time.Time
)

func Init(rootCmd *cobra.Command) {
	infoCmd := &cobra.Command{
		Use:   "info [-c <config name>]",
		Short: "displays information of the profile and the database",
		Args:  cobra.NoArgs,
		Run:   runInfoCmd,
	}
	nowis = time.Now()
	infoCmd.Flags().Uint64Var(&supply, "supply", genesis.DefaultSupply, fmt.Sprintf("initial supply (default is %s", util.GoThousands(genesis.DefaultSupply)))
	defaultDesc := fmt.Sprintf("genesis has been created at Unix time (nanoseconds) %d", nowis.UnixNano())
	infoCmd.Flags().StringVar(&description, "desc", defaultDesc, fmt.Sprintf("default is '%s'", defaultDesc))

	rootCmd.AddCommand(infoCmd)
}

func runInfoCmd(_ *cobra.Command, _ []string) {
	stateDbName := viper.GetString("state_db")
	if stateDbName == "" {
		stateDbName = "(not set)"
	}
	txStoreDbName := viper.GetString("tx_store_db")
	if txStoreDbName == "" {
		txStoreDbName = "(not set)"
	}

	console.Infof("Proxi config profile: %s", viper.ConfigFileUsed())
	console.Infof("Controlling address: %s", config.AddressHex())
	console.Infof("State DB name: %s", stateDbName)
	console.Infof("Transaction store DB name: %s", txStoreDbName)

	if stateDbName == "(not set)" {
		return
	}

	storeDB := badger_adaptor.MustCreateOrOpenBadgerDB(stateDbName, badger.DefaultOptions(stateDbName))
	console.NoError(storeDB.Close())

	//multistate.NewSugaredReadableState(badger_adaptor.New(stateDB), )

}
