package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/node"
)

func main() {
	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT, syscall.SIGTERM)

	ctx, globalStopFun := context.WithCancel(context.Background())

	go func() {
		<-killChan
		global.SetShutDown()
		globalStopFun()
	}()

	n := node.New(ctx)

	n.Run()
	<-ctx.Done()

	n.Stop()
}
