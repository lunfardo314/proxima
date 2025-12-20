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
)

// txsenders core module is designed to prevent spamming/DoS attacks.
// The main principle: each transaction is signed by a sender (public key). Spamming behavior is detected per-sender
// Workflow:
// - receives preparsed transactions from txinput
// - parses sender from signature, checks signature
// - delays of filters out transactions which indicate spam, coming from individual token holders
// - maintains in-memory sender cache with clock times, reputation scores etc
// - only senders known in LRB are eligible, otherwise their txs are deleted

type (
	environment interface {
		global.NodeGlobal
		GetLatestReliableBranch() (ret *multistate.BranchData)
		Branches() *branches.Branches
	}

	Input struct {
		Tx         *transaction.Transaction
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
		timestamps [5]base.LedgerTime
		counter    byte
	}

	TxSenders struct {
		environment
		*core_modules.CoreModule[Input]
		txSenders map[txSenderID]*seenTimestamps
		// metrics
		metrics
	}

	txSenderID string

	metrics struct {
	}
)

const (
	cmdCleanup    = byte(1)
	cmdRebuildMap = byte(2)

	Name = "txSenders"

	cleanupPeriod    = 10 * time.Second
	cleanupHorizon   = 360
	rebuildMapPeriod = 5 * time.Minute
)

func New(env environment) *TxSenders {
	ret := &TxSenders{
		environment: env,
		txSenders:   make(map[txSenderID]*seenTimestamps),
	}
	ret.CoreModule = core_modules.New[Input](env, Name, ret.consume)
	ret.CoreModule.Start()

	ret.RepeatInBackground(Name+"_txSendersCleanup", cleanupPeriod, func() bool {
		ret.Push(Input{cmd: cmdCleanup}, true)
		return true
	})

	ret.RepeatInBackground(Name+"_recreateMap", rebuildMapPeriod, func() bool {
		ret.Push(Input{cmd: cmdRebuildMap}, true)
		return true
	})

	ret.registerMetrics()
	return ret
}

func (q *TxSenders) consume(inp Input) {
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
		return
	}
	if inp.Wanted {
		// send for attachment without caching
		return
	}
	acc := inp.Tx.SenderAddress().AccountID()

	seen := q.txSenders[txSenderID(acc)]
	if seen == nil {
		if !q.isAccountKnownInLRB(acc) {
			// sender account not known -> ignore tx
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
	if pass {
		q.txSenders[txSenderID(acc)] = seen
		// send transaction for attachment
	}
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
}

// addTs if ts is closer than allowed to any of already recorded, the tx will be ignored.
// Otherwise, ts is added to the ring buffer
func (t *tsRingBuffer) addTs(ts base.LedgerTime, minAllowedDiff int64) bool {
	for _, ts1 := range t.timestamps {
		if util.Abs(base.DiffTicks(ts1, ts)) < minAllowedDiff {
			return false
		}
	}
	t.timestamps[t.counter] = ts
	t.counter = (t.counter + 1) % byte(len(t.timestamps))
	return true
}

func (t *tsRingBuffer) lastestTs() (ret base.LedgerTime) {
	for _, ts := range t.timestamps {
		if ts.After(ret) {
			ret = ts
		}
	}
	return
}
