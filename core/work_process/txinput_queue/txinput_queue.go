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

type (
	environment interface {
		global.NodeGlobal
		TxInFromPeer(tx *transaction.Transaction, metaData *txmetadata.TransactionMetadata, from peer.ID) error
		TxBytesInFromAPI(txBytes []byte, trace bool) (*ledger.TransactionID, error)
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
	TxInputCmdFromPeer = byte(iota)
	TxInputCmdFromAPI
	TxInputCmdPurge
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

	ret.RepeatInBackground(Name+"_purge", purgePeriod, func() bool {
		ret.Push(Input{Cmd: TxInputCmdPurge}, true)
		return true
	})
	return ret
}

func (q *TxInputQueue) consume(inp Input) {
	switch inp.Cmd {
	case TxInputCmdFromPeer:
		q.fromPeer(&inp)
	case TxInputCmdFromAPI:
		q.fromAPI(&inp)
	case TxInputCmdPurge:
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
	q.Assertf(inp.TxMetaData != nil, "TxInputQueue: inp.TxMetaData != nil")

	if inp.TxMetaData.IsResponseToPull {
		// do not check bloom filter if transaction was pulled
		if err = q.TxInFromPeer(tx, inp.TxMetaData, inp.FromPeer); err != nil {
			q.Log().Warn("TxInputQueue from peer %s: %v", inp.FromPeer.String(), err)
		}
		return
	}
	if _, hit := q.bloomFilter[tx.ID().VeryShortID4()]; hit {
		// filter hit, ignore transaction. May be false positive (rare) !!
		return
	}
	// not in filter -> definitely new transaction
	q.bloomFilter[tx.ID().VeryShortID4()] = time.Now().Add(q.bloomFilterTTL)

	if err = q.TxInFromPeer(tx, inp.TxMetaData, inp.FromPeer); err != nil {
		q.Log().Warn("TxInputQueue from peer %s: %v", inp.FromPeer.String(), err)
	}
}

func (q *TxInputQueue) fromAPI(inp *Input) {
	if _, err := q.TxBytesInFromAPI(inp.TxBytes, inp.TraceFlag); err != nil {
		q.Log().Warn("TxInputQueue from API: %v", err)
	}
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
