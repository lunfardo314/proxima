package pull_tx_server

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/peering"
	"github.com/prometheus/client_golang/prometheus"
)

type (
	environment interface {
		global.NodeGlobal
		TxBytesStore() global.TxBytesStore
		// GetTxBytes checks the write-behind buffer first, then the store.
		GetTxBytes(txid *base.TransactionID) []byte
		StateStore() global.Store
		SendTxBytesToPeer(id peer.ID, txBytes []byte, txid base.TransactionID) bool
	}

	Input struct {
		TxID   base.TransactionID
		PeerID peer.ID
	}

	PullTxServer struct {
		environment
		*core_modules.CoreModule[*Input]
		responseToPullCounter prometheus.Counter
	}
)

const (
	Name     = "pullTxServer"
	TraceTag = Name
)

func New(env environment) *PullTxServer {
	ret := &PullTxServer{
		environment: env,
	}
	ret.CoreModule = core_modules.New[*Input](env, Name, ret.consume)
	ret.CoreModule.Start()
	ret.registerMetrics()
	return ret
}

func (d *PullTxServer) consume(inp *Input) {
	txBytes := d.GetTxBytes(&inp.TxID)
	if len(txBytes) == 0 {
		d.Tracef(TraceTag, "NOT FOUND %s, request from %s", inp.TxID.StringShort, peering.ShortPeerIDString(inp.PeerID))
		return
	}
	// sending only queues the response on the peer's bounded backlog and returns, so there is
	// nothing to get off this goroutine
	d.SendTxBytesToPeer(inp.PeerID, txBytes, inp.TxID)
	d.responseToPullCounter.Inc()

	d.Tracef(TraceTag, "FOUND %s -> %s", inp.TxID.StringShort, peering.ShortPeerIDString(inp.PeerID))
}

func (d *PullTxServer) registerMetrics() {
	d.responseToPullCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_response_to_pull_counter",
		Help: "counts responses to pull requests",
	})
	d.MetricsRegistry().MustRegister(d.responseToPullCounter)
}
