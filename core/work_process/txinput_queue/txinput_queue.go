package txinput_queue

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/work_process"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

// transaction input queue to bugger incoming transactions from peers and from API
// Maintains bloom filter and check repeating transactions (with small probability of false positives)

type (
	environment interface {
		global.NodeGlobal
		TxInFromPeer(tx *transaction.Transaction, metaData *txmetadata.TransactionMetadata, from peer.ID) error
		TxInFromAPI(tx *transaction.Transaction, trace bool) error
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int
	}

	Input struct {
		Cmd        byte
		TxBytes    []byte
		TxMetaData *txmetadata.TransactionMetadata
		FromPeer   peer.ID
		TraceFlag  bool
	}

	TxInputQueue struct {
		environment
		*work_process.WorkProcess[Input]
		bloomFilter    map[ledger.TransactionIDVeryShort4]time.Time
		bloomFilterTTL time.Duration
	}
)

const (
	CmdFromPeer = byte(iota)
	CmdFromAPI
	CmdPurge
)

const (
	Name                  = "txInputQueue"
	bloomFilterTTLInSlots = 12
	purgePeriod           = 5 * time.Second
)

func New(env environment) *TxInputQueue {
	ret := &TxInputQueue{
		environment:    env,
		bloomFilter:    make(map[ledger.TransactionIDVeryShort4]time.Time),
		bloomFilterTTL: bloomFilterTTLInSlots * ledger.L().ID.SlotDuration(),
	}
	ret.WorkProcess = work_process.New[Input](env, Name, ret.consume)
	ret.WorkProcess.Start()

	ret.RepeatInBackground(Name+"_purge", purgePeriod, func() bool {
		ret.Push(Input{Cmd: CmdPurge}, true)
		return true
	})
	return ret
}

func (q *TxInputQueue) consume(inp Input) {
	switch inp.Cmd {
	case CmdFromPeer:
		q.fromPeer(&inp)
	case CmdFromAPI:
		q.fromAPI(&inp)
	case CmdPurge:
		q.purge()
	default:
		q.Log().Fatalf("TxInputQueue: wrong cmd")
	}
}

func (q *TxInputQueue) fromPeer(inp *Input) {
	tx, err := transaction.FromBytes(inp.TxBytes)
	if err != nil {
		q.Log().Warn("TxInputQueue: %v", err)
		return
	}
	metaData := inp.TxMetaData
	if metaData == nil {
		metaData = &txmetadata.TransactionMetadata{}
	}

	if inp.TxMetaData.IsResponseToPull {
		// do not check bloom filter if transaction was pulled
		if err = q.TxInFromPeer(tx, metaData, inp.FromPeer); err != nil {
			q.Log().Warn("TxInputQueue from peer %s: %v", inp.FromPeer.String(), err)
		}
		return
	}
	// check bloom filter
	if _, hit := q.bloomFilter[tx.ID().VeryShortID4()]; hit {
		// filter hit, ignore transaction. May be rare false positive!!
		q.Log().Infof(">>>>>>>>>>>>>>>>>>>>>>>>>>> REPEATING input filter hit %s", tx.IDShortString())
		return
	}
	q.Log().Infof(">>>>>>>>>>>>>>>>>>>>>>>>>>> BEW TX %s", tx.IDShortString())

	// not in filter -> definitely new transaction
	q.bloomFilter[tx.ID().VeryShortID4()] = time.Now().Add(q.bloomFilterTTL)

	if err = q.TxInFromPeer(tx, metaData, inp.FromPeer); err != nil {
		q.Log().Warn("TxInputQueue from peer %s: %v", inp.FromPeer.String(), err)
		return
	}
	// gossiping all new pre-validated and not pulled transactions from peers
	q.GossipTxBytesToPeers(inp.TxBytes, inp.TxMetaData, inp.FromPeer)
}

func (q *TxInputQueue) fromAPI(inp *Input) {
	tx, err := transaction.FromBytes(inp.TxBytes)
	if err != nil {
		q.Log().Warn("TxInputQueue from API: %v", err)
		return
	}
	if err = q.TxInFromAPI(tx, inp.TraceFlag); err != nil {
		q.Log().Warn("TxInputQueue from API: %v", err)
		return
	}
	// gossiping all pre-validated transactions from API
	q.GossipTxBytesToPeers(inp.TxBytes, inp.TxMetaData, inp.FromPeer)
}

func (q *TxInputQueue) purge() {
	nowis := time.Now()
	toDelete := util.KeysFilteredByValues(q.bloomFilter, func(_ ledger.TransactionIDVeryShort4, deadline time.Time) bool {
		return deadline.After(nowis)
	})
	for _, k := range toDelete {
		delete(q.bloomFilter, k)
	}
}
