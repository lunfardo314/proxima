package txsenders

import (
	"maps"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/prometheus/client_golang/prometheus"
)

// txsenders core module is designed to prevent spamming/DoS attacks.
// The main principle: each transaction is signed by a sender (public key). Spamming behavior is detected per-sender
// Workflow:
// - receives preparsed transactions from txinput
// - parses sender from signature, checks signature
// - delays of filters out transactions which indicate spam, coming from individual token holders
// - maintains in-memory sender cache with clock times, reputation scores etc
// - only senders known in LRB are eligible, otherwise their txs are deleted
// - gossips transactions that were not pulled by the not itself
// - passes transactions for attachment

type (
	environment interface {
		global.NodeGlobal
		GetLatestReliableBranch() (ret *multistate.BranchData)
		Branches() *branches.Branches
		TxInFromPeer(tx *transaction.Transaction, metaData *txmetadata.TransactionMetadata, from peer.ID) error
		TxInFromAPI(tx *transaction.Transaction) error
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID, except ...peer.ID)
	}

	input struct {
		Tx         *transaction.Transaction
		TxIDPrefix base.TransactionID
		TxMetaData *txmetadata.TransactionMetadata
		FromPeer   peer.ID
		Wanted     bool
		cmd        byte
	}

	seenTimestamps struct {
		sequencer    tsRingBuffer
		nonSequencer tsRingBuffer
	}

	tsRingBuffer struct {
		timestamps [keepTimestamps]base.LedgerTime
		counter    byte
	}

	TxSenders struct {
		environment
		*core_modules.CoreModule[input]
		txSenders map[txSenderID]*seenTimestamps
		// metrics
		metrics
	}

	txSenderID string

	metrics struct {
		gossipedCounter prometheus.Counter
	}
)

const (
	cmdCleanup    = byte(1)
	cmdRebuildMap = byte(2)

	Name = "txSenders"

	cleanupPeriod    = 10 * time.Second
	cleanupHorizon   = 360
	rebuildMapPeriod = 5 * time.Minute

	keepTimestamps = 5
	// concentrationTolerance is how many transactions is a pace window are tolerated
	// E.g. 1 means any transaction in the same pace window is ignored
	concentrationTolerance = 1
)

func init() {
	util.Assertf(concentrationTolerance <= keepTimestamps, "wrong constants: expected concentrationTolerance <= keepTimestamps")
}

func New(env environment) *TxSenders {
	ret := &TxSenders{
		environment: env,
		txSenders:   make(map[txSenderID]*seenTimestamps),
	}
	ret.CoreModule = core_modules.New[input](env, Name, ret.consume)
	ret.CoreModule.Start()

	ret.RepeatInBackground(Name+"_Cleanup", cleanupPeriod, func() bool {
		ret.Push(input{cmd: cmdCleanup}, true)
		return true
	})

	ret.RepeatInBackground(Name+"_recreateMap", rebuildMapPeriod, func() bool {
		ret.Push(input{cmd: cmdRebuildMap}, true)
		return true
	})

	ret.registerMetrics()
	return ret
}

func (q *TxSenders) CheckTxSender(tx *transaction.Transaction, txIDPrefix base.TransactionID, meta *txmetadata.TransactionMetadata, fromPeer peer.ID, wanted bool) {
	q.Push(input{
		Tx:         tx,
		TxIDPrefix: txIDPrefix,
		TxMetaData: meta,
		FromPeer:   fromPeer,
		Wanted:     wanted,
	})
}

func (q *TxSenders) consume(inp input) {
	switch inp.cmd {
	case cmdCleanup:
		q.cleanup()
		return
	case cmdRebuildMap:
		q.txSenders = maps.Clone(q.txSenders)
		return
	}
	// new tx
	if err := transaction.ParseSender(inp.Tx); err != nil {
		// ignore transaction with invalid signature
		q.Log().Warnf("tx %s has invalid signture -> IGNORED", inp.Tx.IDShortString())
		return
	}
	if inp.Wanted {
		// send for attachment without caching
		q.attachAndGossip(&inp)
		return
	}
	acc := inp.Tx.SenderAddress().AccountID()

	seen := q.txSenders[txSenderID(acc)]
	if seen == nil {
		if !q.isAccountKnownInLRB(acc) {
			// sender account not known -> ignore tx
			q.Log().Warnf("tx %s has a sender %s unknown in the LRB -> IGNORED", inp.Tx.IDShortString(), inp.Tx.SenderAddress().String())
			return
		}
		seen = &seenTimestamps{}
		q.txSenders[txSenderID(acc)] = seen
	}

	var pass bool
	if inp.Tx.IsSequencerTransaction() {
		pass = seen.sequencer.addTs(inp.Tx.Timestamp(), int64(ledger.Const.TransactionPaceSequencer))
	} else {
		pass = seen.nonSequencer.addTs(inp.Tx.Timestamp(), int64(ledger.Const.TransactionPace))
	}
	q.txSenders[txSenderID(acc)] = seen
	if !pass {
		q.Log().Warnf("timestamp of tx %s from sender %s is too close to another tx from the same sender-> IGNORED", inp.Tx.IDShortString(), inp.Tx.SenderAddress().String())
	}
	// send transaction for attachment
	q.attachAndGossip(&inp)
}

func (q *TxSenders) attachAndGossip(inp *input) {
	if inp.FromPeer == "" {
		if err := q.TxInFromAPI(inp.Tx); err != nil {
			q.Log().Warn("attachAndGossip from API: %v", err)
			return
		}
	} else {
		if err := q.TxInFromPeer(inp.Tx, inp.TxMetaData, inp.FromPeer); err != nil {
			q.Log().Warn("attachAndGossip from peer '%s': %v", inp.FromPeer, err)
			return
		}
	}
	if inp.Wanted {
		// no need to gossip
		return
	}
	// gossiping all new pre-validated and not pulled transactions from peers
	q.GossipTxBytesToPeers(inp.Tx.Bytes(), inp.TxMetaData, inp.TxIDPrefix)
	q.gossipedCounter.Inc()
}

func (q *TxSenders) isAccountKnownInLRB(acc ledger.AccountID) (ret bool) {
	if lrb := q.GetLatestReliableBranch(); lrb != nil {
		rdr := q.Branches().GetStateReaderForTheBranch(lrb.TxID())
		ret = rdr.IsKnownAccount(acc)
	}
	return
}

func (q *TxSenders) cleanup() {
	nowSlot := ledger.SlotNow()
	if nowSlot < cleanupHorizon {
		return
	}
	maps.DeleteFunc(q.txSenders, func(_ txSenderID, timestamps *seenTimestamps) bool {
		return timestamps.sequencer.lastestTs().Slot < nowSlot-cleanupHorizon && timestamps.nonSequencer.lastestTs().Slot < nowSlot
	})
}

func (q *TxSenders) registerMetrics() {
	q.gossipedCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_gossiped",
		Help: "number of gossiped",
	})
	q.MetricsRegistry().MustRegister(
		q.gossipedCounter,
	)

}

// addTs if ts is closer than allowed to any of already recorded, the tx will be ignored.
// Otherwise, ts is added to the ring buffer
func (t *tsRingBuffer) addTs(ts base.LedgerTime, minAllowedDiff int64) bool {
	n := 0
	for _, ts1 := range t.timestamps {
		if util.Abs(base.DiffTicks(ts1, ts)) < minAllowedDiff {
			n++
		}
		if n >= concentrationTolerance {
			break
		}
	}
	t.timestamps[t.counter] = ts
	t.counter = (t.counter + 1) % byte(keepTimestamps)
	return n >= concentrationTolerance
}

func (t *tsRingBuffer) lastestTs() (ret base.LedgerTime) {
	for _, ts := range t.timestamps {
		if ts.After(ret) {
			ret = ts
		}
	}
	return
}
