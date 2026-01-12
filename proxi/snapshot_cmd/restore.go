package snapshot_cmd

import (
	"os"

	"github.com/lunfardo314/proxima/core/core_modules/state_cleanup"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	fname     string
	batchSize int
)

func initRestoreCmd() *cobra.Command {
	restoreCmd := &cobra.Command{
		Use:   "restore [<batch size>]",
		Short: "creates multi-state db from snapshot",
		Args:  cobra.MaximumNArgs(1),
		Run:   runRestoreCmd,
	}

	restoreCmd.PersistentFlags().StringVarP(&fname, "snapshot_file", "s", "", "snapshot file")
	err := viper.BindPFlag("snapshot_file", restoreCmd.PersistentFlags().Lookup("snapshot_file"))
	glb.AssertNoError(err)

	restoreCmd.PersistentFlags().IntVarP(&batchSize, "batch_size", "b", defaultBatchSize, "commit batch size (records)")
	err = viper.BindPFlag("batch_size", restoreCmd.PersistentFlags().Lookup("batch_size"))
	glb.AssertNoError(err)

	restoreCmd.InitDefaultHelpCmd()
	return restoreCmd
}

const defaultBatchSize = 4_000

func runRestoreCmd(_ *cobra.Command, _ []string) {
	// Find snapshot file if not specified
	if fname == "" {
		var ok bool
		fname, ok = findLatestSnapshotFile()
		glb.Assertf(ok, "can't find snapshot file")
	}
	glb.Infof("snapshot file: %s", fname)
	glb.Infof("batch size is %d", batchSize)

	// Check if DB already exists
	if _, err := os.Stat(global.MultiStateDBName); err == nil {
		glb.Infof("WARNING: database %s already exists, it will be overwritten", global.MultiStateDBName)
	}

	// Delete existing database if present
	if err := state_cleanup.DeleteDatabase(global.MultiStateDBName); err != nil {
		glb.Assertf(false, "failed to delete existing database: %v", err)
	}

	// Set up restore options
	opts := state_cleanup.DefaultRestoreOptions()
	opts.BatchSize = batchSize
	if glb.IsVerbose() {
		opts.Console = os.Stdout
	}

	// Use shared restore function
	stats, err := state_cleanup.RestoreFromSnapshot(fname, opts)
	glb.AssertNoError(err)

	glb.Infof("Success\nTotal %d records. By type:", stats.TotalRecords)
	glb.Infof("   Tx:       %d", stats.TxCount)
	glb.Infof("   UTXO:     %d", stats.UTXOCount)
	glb.Infof("   Chains:   %d", stats.ChainCount)
	glb.Infof("   Accounts: %d", stats.AccountsCount)
	glb.Infof("it took %v", stats.Duration)
}
