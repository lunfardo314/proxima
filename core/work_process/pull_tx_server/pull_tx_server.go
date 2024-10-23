package pull_tx_server

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/work_process"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util"
	"github.com/prometheus/client_golang/prometheus"
)

type (
	environment interface {
		global.NodeGlobal
		TxBytesStore() global.TxBytesStore
		StateStore() global.StateStore
		SendTxBytesWithMetadataToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool
	}

	Input struct {
		TxID   ledger.TransactionID
		PeerID peer.ID
	}

	PullTxServer struct {
		environment
		*work_process.WorkProcess[*Input]
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
	ret.WorkProcess = work_process.New[*Input](env, Name, ret.consume)
	ret.WorkProcess.Start()
	ret.registerMetrics()
	return ret
}

func (d *PullTxServer) consume(inp *Input) {
	txBytesWithMetadata := d.TxBytesStore().GetTxBytesWithMetadata(&inp.TxID)
	if len(txBytesWithMetadata) == 0 {
		d.Tracef(TraceTag, "NOT FOUND %s, request from %s", inp.TxID.StringShort, peering.ShortPeerIDString(inp.PeerID))
		return
	}
	metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	util.AssertNoError(err)
	metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
	util.AssertNoError(err)

	go d.SendTxBytesWithMetadataToPeer(inp.PeerID, txBytes, metadata)
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
