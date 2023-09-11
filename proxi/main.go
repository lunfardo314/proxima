/*
Copyright © 2023 NAME HERE <EMAIL ADDRESS>
*/
package main

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/proxi/log"
	"github.com/lunfardo314/proxima/proxi/setup"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	configFile string
)

var rootCmd = &cobra.Command{
	Use:   "proxi",
	Short: "a simple CLI for the Proxima project",
	Long: `proxi is a CLI tool for the Proxima project.
It provides:
      - database level access to the Proxima ledger for admin purposes, including genesis creation
      - access to ledger via the Proxima node API. This includes simple wallet functions
`,
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		//viper.SetConfigFile(configFile)
		if err := viper.ReadInConfig(); err == nil {
			_, _ = fmt.Fprintf(os.Stderr, "Using proxi profile: %s\n", viper.ConfigFileUsed())
		}
	},
}

func init() {
	cobra.OnInitialize(initConfig)

	initRoot()
	log.Init(rootCmd)
	setup.Init(rootCmd)
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if configFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(configFile)
	} else {
		viper.AddConfigPath(".")
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)
		// Search config in current directory with name ".proxi" (without extension).
		viper.AddConfigPath(home)

		viper.SetConfigType("yaml")
		viper.SetConfigName(".proxi")
	}

	viper.AutomaticEnv() // read in environment variables that match
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
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if err := viper.ReadInConfig(); err == nil {
				_, _ = fmt.Fprintf(os.Stderr, "Using proxi profile: %s\n", viper.ConfigFileUsed())
			}
		},
	}

	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "", "config file (default is .proxi.yaml)")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
