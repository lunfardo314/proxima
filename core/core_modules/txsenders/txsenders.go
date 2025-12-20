package txsenders

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
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
	}

	Input struct {
		Tx         *transaction.Transaction
		TxMetaData *txmetadata.TransactionMetadata
		FromPeer   peer.ID
		Wanted     bool
		cmd        byte
	}

	TxSenders struct {
		environment
		*core_modules.CoreModule[Input]
		txSenders         map[txSenderID]time.Time
		requiredGapSeq    time.Duration
		requiredGapNonSeq time.Duration
		// metrics
		metrics
	}

	txSenderID string

	metrics struct {
	}
)

const (
	cmdTx = byte(0)
	cmdCleanup
	cmdRebuildMap

	Name = "txSenders"

	cleanupPeriod    = 10 * time.Second
	cleanupHorizon   = time.Hour
	rebuildMapPeriod = 5 * time.Minute
)

func New(env environment) *TxSenders {
	ret := &TxSenders{
		environment:       env,
		txSenders:         make(map[txSenderID]time.Time),
		requiredGapSeq:    time.Duration(ledger.Const.TransactionPaceSequencer) * ledger.Const.TickDuration,
		requiredGapNonSeq: time.Duration(ledger.Const.TransactionPace) * ledger.Const.TickDuration,
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
	if inp.cmd == cmdCleanup {
		q.cleanup()
		return
	}
	if inp.cmd == cmdRebuildMap {
		q.rebuildMap()
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

	if lastSeen, inCache := q.txSenders[txSenderID(acc)]; inCache {
		var timeGapAtLeast time.Duration
		if inp.Tx.IsSequencerTransaction() {
			timeGapAtLeast = q.requiredGapSeq
		} else {
			timeGapAtLeast = q.requiredGapNonSeq
		}
		if time.Since(lastSeen) < timeGapAtLeast {
			// ignore tx -> too close in time
			return
		}
	} else {
		if !q.isAccountKnownInLRB(acc) {
			// ignore tx -> unknown account
			return
		}
	}
	q.txSenders[txSenderID(acc)] = time.Now()
	// send transaction for attachment
}

func (q *TxSenders) isAccountKnownInLRB(acc ledger.AccountID) bool {
	return false
}

func (q *TxSenders) cleanup() {
}

func (q *TxSenders) rebuildMap() {
}

func (q *TxSenders) registerMetrics() {
}
