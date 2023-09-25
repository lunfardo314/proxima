package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/lunfardo314/proxima/proxi/api"
	"github.com/lunfardo314/proxima/proxi/db"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/proxi/info_cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	initRootCmd()
	glb.Init(rootCmd)
	info_cmd.Init(rootCmd)
	db.Init(rootCmd)
	api.Init(rootCmd)
}

const DefaultTagAlongFee = 500

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

	rootCmd.PersistentFlags().StringP("config", "c", "", "config file (default is proxi.yaml)")
	err := viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	rootCmd.PersistentFlags().String("wallet.name", "", "wallet name")
	err = viper.BindPFlag("wallet.name", rootCmd.PersistentFlags().Lookup("wallet.name"))
	glb.AssertNoError(err)

	rootCmd.PersistentFlags().String("wallet.pk", "", "an ED25519 private key in hexadecimal")
	err = viper.BindPFlag("wallet.pk", rootCmd.PersistentFlags().Lookup("wallet.pk"))
	glb.AssertNoError(err)

	rootCmd.PersistentFlags().String("wallet.account", "", "Address25519 lock of the account")
	err = viper.BindPFlag("wallet.account", rootCmd.PersistentFlags().Lookup("wallet.account"))
	glb.AssertNoError(err)

	rootCmd.PersistentFlags().String("wallet.sequencer", "", "Sequencer, controlled by the wallet")
	err = viper.BindPFlag("wallet.sequencer", rootCmd.PersistentFlags().Lookup("wallet.sequencer"))
	glb.AssertNoError(err)

	rootCmd.PersistentFlags().Bool("force", false, "bypass yes/no prompts with the default") // fixme not working
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbose")
	rootCmd.PersistentFlags().StringP("target", "t", "", "target account")

	rootCmd.PersistentFlags().String("tag-along.sequencer", "", "tag-along sequencer ID")
	err = viper.BindPFlag("tag-along.sequencer", rootCmd.PersistentFlags().Lookup("tag-along.sequencer"))
	glb.AssertNoError(err)

	rootCmd.PersistentFlags().Uint64("tag-along.fee", DefaultTagAlongFee, "tag-along fee")
	err = viper.BindPFlag("tag-along.fee", rootCmd.PersistentFlags().Lookup("tag-along.fee"))
	glb.AssertNoError(err)
}

func main() {
	glb.Infof("Command line: '%s'\n", strings.Join(os.Args, " "))
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
