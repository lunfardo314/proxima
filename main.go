package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/lunfardo314/proxima/core/core_modules/snapshot_restore"
	"github.com/lunfardo314/proxima/node"
	"github.com/lunfardo314/proxima/util/restart"
)

func main() {
	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT, syscall.SIGTERM)

	n := node.New()
	go func() {
		<-killChan
		n.Stop()
	}()

	// initialize and start node
	n.Start()
	// wait until all active processes stops
	n.WaitAllWorkProcessesStopped()
	// only now close databases
	n.WaitAllDBClosed()

	// Check if state cleanup requested a restart
	if snapshot_restore.CleanupRequestedFlag.Load() {
		snapshotFile, _ := snapshot_restore.SnapshotFileForRestore.Load().(string)
		n.Log().Infof("state cleanup requested, restarting node to restore from: %s", snapshotFile)

		binary, err := os.Executable()
		if err != nil {
			n.Log().Errorf("failed to get executable path: %v - exiting with code 1", err)
			os.Exit(1)
		}

		if restart.SelfRestart == nil {
			n.Log().Errorf("self-restart not supported on this platform - exiting with code 1")
			os.Exit(1)
		}

		err = restart.SelfRestart(binary, os.Args, os.Environ())
		if err != nil {
			n.Log().Errorf("self-restart failed: %v - exiting with code 1", err)
			os.Exit(1)
		}
	}

	n.Log().Infof("Hasta la próxima, baby! I'll be back")
}
