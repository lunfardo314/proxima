package db

import (
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/proxi/glb"
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

	dbCmd.PersistentFlags().StringVar(&stateDBName, general.ConfigKeyMultiStateDbName, "", "name of the ledger multi-state DB")
	err := viper.BindPFlag(general.ConfigKeyMultiStateDbName, dbCmd.PersistentFlags().Lookup(general.ConfigKeyMultiStateDbName))
	glb.AssertNoError(err)

	dbCmd.PersistentFlags().StringVar(&txStoreDB, general.ConfigKeyTxStoreName, "", "name of the transaction store")
	err = viper.BindPFlag(general.ConfigKeyTxStoreName, dbCmd.PersistentFlags().Lookup(general.ConfigKeyTxStoreName))
	glb.AssertNoError(err)

	dbCmd.InitDefaultHelpCmd()
	initDBInfoCmd(dbCmd)
	initDBTreeCmd(dbCmd)
	initDBDistributeCmd(dbCmd)
	initDbGenesis(dbCmd)

	rootCmd.AddCommand(dbCmd)
}

func displayDBNames() {
	glb.Infof("Multi-state store DB: '%s'", GetMultiStateStoreName())
	glb.Infof("Transaction store DB: '%s'", GetTxStoreName())
}

func GetMultiStateStoreName() string {
	ret := viper.GetString(general.ConfigKeyMultiStateDbName)
	if ret == "" {
		ret = "(not set)"
	}
	return ret
}

func GetTxStoreName() string {
	ret := viper.GetString(general.ConfigKeyTxStoreName)
	if ret == "" {
		ret = "(not set)"
	}
	return ret
}
