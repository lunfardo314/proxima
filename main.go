package main

import (
	"os"
	"os/signal"

	"github.com/lunfardo314/proxima/node"
)

func main() {
	n := node.Start()

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, os.Interrupt)
	<-killChan

	n.Stop()
}
