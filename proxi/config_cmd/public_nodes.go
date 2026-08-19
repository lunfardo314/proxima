package config_cmd

import "fmt"

// publicNode is one of the officially advertised Proxima nodes. This table is the
// single source of the addresses the generated configs offer: static peers and
// sync/snapshot sources in proxima.yaml, API endpoint hints in the wallet profile.
// Only nodes advertised as public belong here - a node that is reachable but not
// advertised must not be listed.
//
// HostID is the node's libp2p host ID, which changes whenever its host key is
// regenerated: after a key rotation this table must be updated and the binaries
// rebuilt, otherwise the peer entries it renders are stale. An empty HostID
// renders a placeholder to be filled in by hand.
type publicNode struct {
	Name        string
	IP          string
	PeeringPort int
	APIPort     int
	HostID      string
}

var publicNodes = []publicNode{
	{
		Name:        "hloc0",
		IP:          "65.21.170.230",
		PeeringPort: 4001,
		APIPort:     8001,
		HostID:      "12D3KooWG8zty1Yfbxw4mpiLW6Eoh6wTpPNdqn2B3f22XqArLsnT",
	},
	{
		Name:        "oseq1",
		IP:          "79.137.70.25",
		PeeringPort: 4001,
		APIPort:     8001,
		HostID:      "12D3KooWPG5KW9JuunvpU5kBm51HZKYv7taYbtx1ThdjXPN533u8",
	},
	{
		Name:        "oloc2",
		IP:          "51.254.47.76",
		PeeringPort: 4001,
		APIPort:     8001,
		HostID:      "12D3KooWCr6wCc67V7NRDJXK1iduefVvyA5tLWUHkvYdNpPdEt3t",
	},
}

// MultiAddr renders the libp2p address of the node, the value of a `peers` entry.
func (p publicNode) MultiAddr() string {
	hostID := p.HostID
	if hostID == "" {
		hostID = "<p2p host ID>"
	}
	return fmt.Sprintf("/ip4/%s/udp/%d/quic-v1/p2p/%s", p.IP, p.PeeringPort, hostID)
}

// APIEndpoint renders the node's API URL: a sync/snapshot source, or the endpoint
// a wallet talks to.
func (p publicNode) APIEndpoint() string {
	return fmt.Sprintf("http://%s:%d", p.IP, p.APIPort)
}
