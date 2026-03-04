package node_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/proxi/node_cmd/delegate"
	"github.com/lunfardo314/proxima/proxi/node_cmd/seq_cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func Init() *cobra.Command {
	nodeCmd := &cobra.Command{
		Use:   "node [<subcommand>]",
		Short: "specifies node API subcommand",
		Args:  cobra.NoArgs,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
	}

	nodeCmd.PersistentFlags().StringP("config", "c", "", "proxi config profile name")
	err := viper.BindPFlag("config", nodeCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	nodeCmd.PersistentFlags().String("private_key", "", "ED25519 private key (hex encoded)")
	err = viper.BindPFlag("private_key", nodeCmd.PersistentFlags().Lookup("private_key"))
	glb.AssertNoError(err)

	nodeCmd.PersistentFlags().String("api.endpoint", "", "<DNS name>:port")
	err = viper.BindPFlag("api.endpoint", nodeCmd.PersistentFlags().Lookup("api.endpoint"))
	glb.AssertNoError(err)

	nodeCmd.PersistentFlags().BoolP("nowait", "n", false, "do not wait for inclusion")
	err = viper.BindPFlag("nowait", nodeCmd.PersistentFlags().Lookup("nowait"))
	glb.AssertNoError(err)

	nodeCmd.PersistentFlags().BoolVarP(&glb.UseAlternativeTagAlongSequencer, "tag_along.alt", "a", false, "use alternative tag-along sequencer")
	err = viper.BindPFlag("tag_along.alt", nodeCmd.PersistentFlags().Lookup("tag_along.alt"))
	glb.AssertNoError(err)

	nodeCmd.PersistentFlags().IntVarP(&glb.TargetInclusionDepth, "depth", "d", 1, "target inclusion depth")
	err = viper.BindPFlag("depth", nodeCmd.PersistentFlags().Lookup("depth"))
	glb.AssertNoError(err)

	nodeCmd.InitDefaultHelpCmd()
	nodeCmd.AddCommand(
		initGetOutputsCmd(),
		initGetChainOutputCmd(),
		initCompactOutputsCmd(),
		initBalanceCmd(),
		initTransferCmd(),
		initSpamCmd(),
		initMakeChainCmd(),
		initKillChainCmd(),
		initNodeInfoCmd(),
		seq_cmd.Init(),
		initSeqSetupCmd(),
		initSyncInfoCmd(),
		initPeersInfoCmd(),
		initReliableBranchCmd(),
		initFaucetServerCmd(),
		initGetFundsCmd(),
		initLastSeqCmd(),
		delegate.Init(),
		initAllChainsCmd(),
		initNodeGetLedgerIDCmd(),
		initChainCmd(),
		initGetInactiveCmd(),
		initTxLogCmd(),
		initGetSnapshotCmd(),
	)
	return nodeCmd
}
