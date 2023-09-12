package db_cmd

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
			displayNames()
		},
	}

	dbCmd.PersistentFlags().StringVar(&stateDBName, "state_db", "", "name of the ledger state DB")
	err := viper.BindPFlag("state_db", dbCmd.PersistentFlags().Lookup("state_db"))
	console.NoError(err)

	dbCmd.PersistentFlags().StringVar(&txStoreDB, "tx_store_db", "", "name of the transaction store")
	err = viper.BindPFlag("tx_store_db", dbCmd.PersistentFlags().Lookup("tx_store_db"))
	console.NoError(err)

	dbCmd.InitDefaultHelpCmd()
	initDBInfoCmd(dbCmd)

	rootCmd.AddCommand(dbCmd)
}

func displayNames() {
	console.Infof("state DB: '%s'", GetStateStoreName())
	console.Infof("tx store DB: '%s'", GetTxStoreName())
}

func GetStateStoreName() string {
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
