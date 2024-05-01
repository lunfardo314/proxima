package main

import (
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/lunfardo314/proxima/util/lines"
)

func main() {
	// Set your own keypair
	priv, _, err := crypto.GenerateKeyPair(
		crypto.Ed25519, // Select your key type. Ed25519 are nice short
		-1,             // Select key length when possible (i.e. RSA).
	)

	//var idht *dht.IpfsDHT

	host, err := libp2p.New(
		// Use the keypair we generated
		libp2p.Identity(priv),
		// Multiple listen addresses
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/9000",      // regular tcp connections
			"/ip4/0.0.0.0/udp/9000/quic", // a UDP endpoint for the QUIC transport
		),
		// support TLS connections
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		// support Noise connections
		libp2p.Security(noise.ID, noise.New),
		// support QUIC
		//libp2p.Transport(libp2pquic.NewTransport),
		// support any other default transports (TCP)
		libp2p.DefaultTransports,
		// Let's prevent our peer from having too many
		// connections by attaching a connection manager.
		//libp2p.ConnectionManager(connmgr.NewConnManager(
		//	100,         // Lowwater
		//	400,         // HighWater,
		//	time.Minute, // InitialDelay
		//)),
		// Attempt to open ports using uPNP for NATed hosts.
		libp2p.NATPortMap(),
		// Let this host use the DHT to find other hosts
		//libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
		//	idht, err = dht.New(ctx, h)
		//	return idht, err
		//}),
		// Let this host use relays and advertise itself on relays if
		// it finds it is behind NAT. Use libp2p.Relay(options...) to
		// enable active relays and more.
		//libp2p.EnableAutoRelay(),
	)
	if err != nil {
		panic(err)
	}
	defer host.Close()

	fmt.Printf("Hello World, my hosts ID is %s\n", host.ID())
	ln := lines.New("    ")
	for _, a := range host.Addrs() {
		ln.Add(a.String())
	}

	fmt.Printf("Addresses:\n%s\n", lines.SliceToLines(host.Addrs(), "      "))
}
