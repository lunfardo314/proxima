/*
Copyright © 2023 NAME HERE <EMAIL ADDRESS>
*/
package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/proxi/genesis_cmd"
	"github.com/lunfardo314/proxima/proxi/setup"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	initRoot()
	console.Init(rootCmd)
	setup.Init(rootCmd)
	genesis_cmd.InitGenesisCmd(rootCmd)
}

var (
	configFile    string
	privateKeyStr string
)

var rootCmd = &cobra.Command{
	Use:   "proxi",
	Short: "a simple CLI for the Proxima project",
	Long: `proxi is a CLI tool for the Proxima project.
It provides:
      - database level access to the Proxima ledger for admin purposes, including genesis creation
      - access to ledger via the Proxima node API. This includes simple wallet functions
`,
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		initConfig()
	},
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if configFile != "" {
		// Use config file from the flag.
		viper.SetConfigName(configFile)
	} else {
		viper.AddConfigPath(".")
		viper.SetConfigName(".proxi")
		viper.SetConfigType("yaml")
	}

	viper.AutomaticEnv() // read in environment variables that match

	if err := viper.ReadInConfig(); err == nil {
		_, _ = fmt.Fprintf(os.Stderr, "Using config profile: %s\n", viper.ConfigFileUsed())
	}
}

func initRoot() {
	rootCmd = &cobra.Command{
		Use:   "proxi",
		Short: "a simple CLI for the Proxima project",
		Long: `proxi is a CLI tool for the Proxima project.
It provides:
      - database level access to the Proxima ledger for admin purposes, including genesis creation
      - access to ledger via the Proxima node API. This includes simple wallet functions
`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			initConfig()

			if addr := setup.AddressBytes(); len(addr) > 0 {
				console.Infof("Private key corresponds to address %s", hex.EncodeToString(addr))
			} else {
				console.Infof("Private key not set")
			}
		},
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}

	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "", "config file (default is .proxi.yaml)")
	rootCmd.PersistentFlags().StringVar(&privateKeyStr, "private_key", "", "an ED25519 private key in hexadecimal")
	err := viper.BindPFlag("private_key", rootCmd.PersistentFlags().Lookup("private_key"))
	console.NoError(err)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
