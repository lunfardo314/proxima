package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/lunfardo314/proxima/node"
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
	n.WaitAllWorkProcessesToStop()
	// only now close databases
	n.WaitAllDBClosed()

	n.Log().Infof("Hasta la vista, baby! I'll be back")
}
