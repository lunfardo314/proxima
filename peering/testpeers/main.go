package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: testpeers <peer index 0-4>")
		os.Exit(0)
	}
	hostIdx, err := strconv.Atoi(os.Args[1])
	util.AssertNoError(err)
	if hostIdx < 0 || hostIdx > 4 {
		panic("host index must be from 0 to 4")
	}

	cfg := peering.MakeConfigFor(5, hostIdx)

	ctx, stopFun := context.WithCancel(context.Background())

	host, err := peering.New(cfg, ctx)
	util.AssertNoError(err)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-killChan
		stopFun()
	}()

	host.Run()
	<-ctx.Done()
}
