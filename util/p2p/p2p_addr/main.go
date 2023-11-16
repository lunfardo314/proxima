package main

import (
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/lunfardo314/proxima/util/lines"
)

func main() {
	host, err := libp2p.New()
	if err != nil {
		panic(err)
	}
	defer host.Close()

	fmt.Printf("Hello World, my host ID is %s\n", host.ID())
	ln := lines.New("    ")
	for _, a := range host.Addrs() {
		ln.Add(a.String())
	}

	fmt.Printf("Addresses:\n%s\n", lines.SliceToLines(host.Addrs(), "      "))
}
