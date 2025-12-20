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
	}

	TxSenders struct {
		environment
		*core_modules.CoreModule[Input]
		txSenders map[txSenderID]*txSenderData
		// metrics
		metrics
	}

	txSenderID   string
	txSenderData struct {
		lastActivity time.Time
	}

	metrics struct {
	}
)

const (
	Name = "txSenders"

	cleanupPeriod = 10 * time.Second
	recreateMapPeriod
)

func New(env environment) *TxSenders {
	ret := &TxSenders{
		environment: env,
		txSenders:   make(map[txSenderID]*txSenderData),
	}
	ret.CoreModule = core_modules.New[Input](env, Name, ret.consume)
	ret.CoreModule.Start()

	ret.RepeatInBackground(Name+"_txSendersCleanup", cleanupPeriod, func() bool {
		ret.cleanup()
		return true
	})

	ret.RepeatInBackground(Name+"_recreateMap", recreateMapPeriod, func() bool {
		ret.recreateMap()
		return true
	})

	ret.registerMetrics()
	return ret
}

func (q *TxSenders) consume(inp Input) {
	if err := transaction.ParseSender(inp.Tx); err != nil {
		// ignore transaction with invalid signature
		return
	}
	acc := inp.Tx.SenderAddress().AccountID()
	senderData := q.txSenders[txSenderID(acc)]
	if senderData == nil {
		if !q.isAccountKnownInLRB(acc) {
			// account is not known in LRB. Ignore both tx and the sender
			return
		}
		q.txSenders[txSenderID(acc)] = &txSenderData{
			lastActivity: time.Now(),
		}
	}

}

func (q *TxSenders) isAccountKnownInLRB(acc ledger.AccountID) bool {
	return false
}

func (q *TxSenders) cleanup() {
}

func (q *TxSenders) recreateMap() {
}

func (q *TxSenders) registerMetrics() {
}
