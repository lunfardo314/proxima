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

	n.Run()
	<-n.Ctx().Done()

	n.WaitStop()
}
