package db

import (
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	stateDBName string
	txStoreDB   string
)

func Init(rootCmd *cobra.Command) {
	dbCmd := &cobra.Command{
		Use:   "db [<subcommand>]",
		Short: "specifies subcommand on the database",
		Args:  cobra.MaximumNArgs(1),
		Run: func(_ *cobra.Command, _ []string) {
			displayDBNames()
		},
	}

	dbCmd.PersistentFlags().StringVar(&stateDBName, "state_db", "", "name of the ledger state DB")
	err := viper.BindPFlag("state_db", dbCmd.PersistentFlags().Lookup("state_db"))
	console.AssertNoError(err)

	dbCmd.PersistentFlags().StringVar(&txStoreDB, "tx_store_db", "", "name of the transaction store")
	err = viper.BindPFlag("tx_store_db", dbCmd.PersistentFlags().Lookup("tx_store_db"))
	console.AssertNoError(err)

	dbCmd.InitDefaultHelpCmd()
	initDBInfoCmd(dbCmd)
	initDBTreeCmd(dbCmd)
	initDBDistributeCmd(dbCmd)
	initDbGenesis(dbCmd)

	rootCmd.AddCommand(dbCmd)
}

func displayDBNames() {
	console.Infof("Multi-state store DB: '%s'", GetMultiStateStoreName())
	console.Infof("Transaction store DB: '%s'", GetTxStoreName())
}

func GetMultiStateStoreName() string {
	ret := viper.GetString("state_db")
	if ret == "" {
		ret = "(not set)"
	}
	return ret
}

func GetTxStoreName() string {
	ret := viper.GetString("tx_store_db")
	if ret == "" {
		ret = "(not set)"
	}
	return ret
}
