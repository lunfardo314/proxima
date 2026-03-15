package multispam_cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/lunfardo314/proxima/multispam"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "run multi-sender spammer",
		Args:  cobra.NoArgs,
		Run:   runRunCmd,
	}
	cmd.Flags().IntP("senders", "n", 0, "number of senders to use (default: all)")
	cmd.Flags().Duration("max-duration", 0, "stop after duration (e.g. 10m, 1h)")
	cmd.Flags().Int64("max-transactions", 0, "stop after total transaction count")
	return cmd
}

func runRunCmd(cmd *cobra.Command, _ []string) {
	configFile := viper.GetString("multispam-config")
	cfg, err := multispam.LoadConfig(configFile)
	glb.AssertNoError(err)

	// Use first API host from multispam config so wallet config is not required
	firstHost := cfg.APIHosts[0]
	viper.Set("api.endpoint", firstHost.URL)
	if firstHost.Timeout > 0 {
		viper.Set("api.timeout_sec", int(firstHost.Timeout.Seconds()))
	}

	glb.InitLedgerFromNode()

	numSenders, _ := cmd.Flags().GetInt("senders")
	maxDuration, _ := cmd.Flags().GetDuration("max-duration")
	maxTx, _ := cmd.Flags().GetInt64("max-transactions")

	coord, err := multispam.NewCoordinator(multispam.CoordinatorParams{
		Config:          cfg,
		NumSenders:      numSenders,
		MaxDuration:     maxDuration,
		MaxTransactions: maxTx,
		LogFunc: func(format string, args ...any) {
			glb.Infof(format, args...)
		},
	})
	glb.AssertNoError(err)

	// Handle Ctrl+C
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		glb.Infof("shutting down...")
		cancel()
		// Second signal forces exit
		<-sigCh
		os.Exit(1)
	}()

	if maxDuration > 0 {
		glb.Infof("max duration: %v", maxDuration)
	}
	if maxTx > 0 {
		glb.Infof("max transactions: %d", maxTx)
	}

	err = coord.Run(ctx)
	glb.AssertNoError(err)
}
