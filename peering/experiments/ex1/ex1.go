package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/net/context"
)

func main() {
	// Create the libp2p host.
	//
	// Note that we are explicitly passing the listen address and restricting it to IPv4 over the
	// loopback interface (127.0.0.1).
	//
	// Setting the TCP port as 0 makes libp2p choose an available port for us.
	// You could, of course, specify one if you like.
	host, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		panic(err)
	}
	defer host.Close()

	const protocolID = "/ex1/1.0.0"

	// This gets called every time a peer connects
	// and opens a stream to this node.
	host.SetStreamHandler(protocolID, func(s network.Stream) {
		fmt.Printf("streamline handler called\n")
		go writeCounter(s)
		go readCounter(s)
	})

	// Print this node's addresses and ID
	fmt.Println("ID:", host.ID())
	fmt.Printf("Addresses:\n%s\n", lines.SliceToLines(host.Addrs(), "      "))
	fmt.Printf("connect to: %s/experiments/%s\n", host.Addrs()[0].String(), host.ID())

	// If we received a peer address, we should connect to it.

	if len(os.Args) > 1 {
		// Parse the multiaddr string.
		peerMA, err := multiaddr.NewMultiaddr(os.Args[1])
		if err != nil {
			panic(err)
		}
		peerAddrInfo, err := peer.AddrInfoFromP2pAddr(peerMA)
		if err != nil {
			panic(err)
		}

		// Connect to the node at the given address.
		fmt.Printf("connecting to %s...\n", os.Args[1])

		if err := host.Connect(context.Background(), *peerAddrInfo); err != nil {
			panic(err)
		}
		fmt.Println("Connected to", peerAddrInfo.String())

		// Open a stream with the given peer.
		s, err := host.NewStream(context.Background(), peerAddrInfo.ID, protocolID)
		if err != nil {
			panic(err)
		}
		defer s.Close()

		// Start the write and read threads.
		go writeCounter(s)
		go readCounter(s)
	}

	sigCh := make(chan os.Signal)
	signal.Notify(sigCh, syscall.SIGKILL, syscall.SIGINT)
	<-sigCh
}

func writeCounter(s network.Stream) {
	var counter uint64

	for {
		<-time.After(time.Second)
		counter++

		err := binary.Write(s, binary.BigEndian, counter)
		if err != nil {
			fmt.Printf("writeCounter error: %v\n", err)
			return
		}
	}
}

func readCounter(s network.Stream) {
	for {
		var counter uint64

		err := binary.Read(s, binary.BigEndian, &counter)
		if err != nil {
			fmt.Printf("readCounter error: %v\n", err)
			return
		}

		fmt.Printf("Received %d from %s\n", counter, s.ID())
	}
}
