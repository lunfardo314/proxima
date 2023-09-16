package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/lunfardo314/proxima/proxi/config"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/proxi/db"
	"github.com/lunfardo314/proxima/proxi/info_cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	initRootCmd()
	console.Init(rootCmd)
	config.Init(rootCmd)
	db.Init(rootCmd)
	info_cmd.Init(rootCmd)
	db.Init(rootCmd)
}

var rootCmd = &cobra.Command{
	Use:   "proxi",
	Short: "a simple CLI for the Proxima project",
	Long: `proxi is a CLI tool for the Proxima project.
It provides:
      - database level access to the Proxima ledger for admin purposes, including genesis creation
      - access to ledger via the Proxima node API. This includes simple wallet functions
`,
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		readInConfig()
	},
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

// readInConfig reads in config file and ENV variables if set.
func readInConfig() {

	configName := viper.GetString("config")
	if configName == "" {
		configName = "proxi"
	}
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	viper.SetConfigName(configName)
	viper.SetConfigFile("./" + configName + ".yaml")

	viper.AutomaticEnv() // read in environment variables that match

	_, _ = fmt.Fprintf(os.Stderr, "Config profile: %s\n", viper.ConfigFileUsed())

	if err := viper.ReadInConfig(); err == nil {
		_, _ = fmt.Fprintf(os.Stderr, "Using config profile: %s\n", viper.ConfigFileUsed())
	}
}

func initRootCmd() {
	rootCmd = &cobra.Command{
		Use:   "proxi",
		Short: "a simple CLI for the Proxima project",
		Long: `proxi is a CLI tool for the Proxima project.
It provides:
      - database level access to the Proxima ledger for admin purposes, including genesis creation
      - access to ledger via the Proxima node API. This includes simple wallet functions
`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			readInConfig()
		},
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}

	rootCmd.PersistentFlags().StringVarP(&config.ConfigName, "config", "c", "", "config file (default is proxi.yaml)")

	rootCmd.PersistentFlags().String("private_key", "", "an ED25519 private key in hexadecimal")
	err := viper.BindPFlag("private_key", rootCmd.PersistentFlags().Lookup("private_key"))
	console.AssertNoError(err)
}

func main() {
	console.Infof("------ proxi invoked. Command line: '%s'\n", strings.Join(os.Args, " "))
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
