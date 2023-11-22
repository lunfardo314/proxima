package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/atomic"
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

	receiveCounter := 0
	host.OnReceiveTxBytes(func(from peer.ID, data []byte) {
		msg := string(data)
		if receiveCounter%100 == 0 {
			fmt.Printf("recv   <- %s\n", msg)
		}
		receiveCounter++
	})

	var exit atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		sendCounter := 0
		for !exit.Load() {
			msg := fmt.Sprintf("%d", sendCounter)
			n := host.GossipTxBytesToPeers([]byte(msg))
			if sendCounter%100 == 0 {
				fmt.Printf("send %d -> %s\n", n, msg)
			}
			sendCounter++
			//time.Sleep(10 * time.Millisecond)
		}
		wg.Done()
	}()

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-killChan
		exit.Store(true)
		stopFun()
	}()

	host.Run()
	<-ctx.Done()
	wg.Wait()
}
