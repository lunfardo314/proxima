package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/node"
)

func main() {
	n := node.Start()

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, os.Interrupt, syscall.SIGTERM)
	<-killChan

	global.SetShutDown()
	n.Stop()
}
