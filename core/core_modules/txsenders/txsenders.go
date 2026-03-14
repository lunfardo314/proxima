package txsenders

import (
	"fmt"
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
		AttachTxFromPeer(tx *transaction.Transaction, metaData *txmetadata.TransactionMetadata, from peer.ID) error
		AttachTxFromAPI(tx *transaction.Transaction) error
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID, except ...peer.ID)
		CheckTxSenderConfig() (checkSeq, checkNonSeq bool)
	}

	input struct {
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
		timestamps [keepTimestamps]int64
		counter    byte
	}

	TxSenders struct {
		environment
		*core_modules.CoreModule[input]
		txSenders   map[base.HolderID]*seenTimestamps
		checkSeq    bool
		checkNonSeq bool
		// metrics
		metrics
	}

	metrics struct {
		gossipedCounter prometheus.Counter
	}
)

const (
	cmdCleanup    = byte(1)
	cmdRebuildMap = byte(2)

	Name = "txSenders"

	cleanupPeriod       = 10 * time.Second
	cleanupHorizonTicks = 360 * 127
	rebuildMapPeriod    = 5 * time.Minute

	keepTimestamps = 4
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
		txSenders:   make(map[base.HolderID]*seenTimestamps),
	}
	ret.CoreModule = core_modules.New[input](env, Name, ret.consume)
	ret.CoreModule.Start()
	ret.checkSeq, ret.checkNonSeq = env.CheckTxSenderConfig()
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

func (q *TxSenders) CheckTxSender(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata, fromPeer peer.ID, wanted bool) {
	q.Push(input{
		Tx:         tx,
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
	// parse signature (no validation, it is done by tx.ValidatePartialContext())
	holderID, err := inp.Tx.HolderID()
	if err != nil {
		txLogMsg := fmt.Sprintf("IGNORED: cannot parse holder ID: %v", err)
		q.LogTx(time.Now(), txLogMsg, inp.Tx.ID())

		q.Log().Warnf("tx %s: %s -> IGNORED", inp.Tx.IDShortString(), txLogMsg)
		return
	}
	if inp.Wanted {
		// transaction was pulled, so it passes
		q.attachAndGossip(&inp)
		return
	}
	// tx not pulled. Check cache

	seen := q.txSenders[holderID]
	if seen == nil {
		// it is a new sender, never seen in the cache
		// check if it is known in the LRB
		if !q.isHolderKnownInLRB(holderID) {
			// sender is new to LRB not known
			// that may be attack, or the node may be lagging behind the actual state
			if !inp.Tx.IsBranchTransaction() {
				// non-branch transactions with sender new to the ledger we ignore.
				// They can come back later, if pulled
				txLogMsg := fmt.Sprintf("tx sender %s is not known in LRB -> IGNORED", ledger.SigLock(holderID).String())
				q.LogTx(time.Now(), txLogMsg, inp.Tx.ID())

				q.Log().Warnf("tx %s : %s", inp.Tx.IDShortString(), txLogMsg)
				return
			}
			// TODO new branch transactions with unknown sender pass. This may be an attack vector
			//  we are leaving this for now because otherwise it may not be possible to sync old snapshots
		}
		seen = &seenTimestamps{}
		q.txSenders[holderID] = seen
	}

	var pass bool
	txTs := inp.Tx.Timestamp()
	lib := ledger.L(txTs.Slot)
	if inp.Tx.IsSequencerTransaction() {
		pass = !q.checkSeq || seen.sequencer.addTs(txTs.TicksSinceGenesis(), int64(lib.TransactionPaceSequencer))
	} else {
		pass = !q.checkNonSeq || seen.nonSequencer.addTs(txTs.TicksSinceGenesis(), int64(lib.TransactionPace))
	}
	q.txSenders[holderID] = seen
	if !pass {
		txLogMsg := fmt.Sprintf("timestamp is too close to another tx from the same sender %s -> IGNORED", holderID.String())
		q.LogTx(time.Now(), txLogMsg, inp.Tx.ID())

		q.Log().Warnf("tx %s: %s", inp.Tx.IDShortString(), txLogMsg)
		return
	}
	// send transaction for attachment
	q.attachAndGossip(&inp)
}

func (q *TxSenders) attachAndGossip(inp *input) {
	if inp.FromPeer == "" {
		if err := q.AttachTxFromAPI(inp.Tx); err != nil {
			q.Log().Warnf("attachAndGossip from API: '%v'", err)
			return
		}
	} else {
		if err := q.AttachTxFromPeer(inp.Tx, inp.TxMetaData, inp.FromPeer); err != nil {
			q.Log().Warnf("attachAndGossip from peer '%s': '%v'", inp.FromPeer, err)
			return
		}
	}
	if inp.Wanted {
		// no need to gossip
		return
	}
	// gossiping all new pre-validated and not pulled transactions from peers
	q.GossipTxBytesToPeers(inp.Tx.Bytes(), inp.TxMetaData, inp.Tx.ID())
	q.gossipedCounter.Inc()
}

func (q *TxSenders) isHolderKnownInLRB(acc base.HolderID) (ret bool) {
	if lrb := q.GetLatestReliableBranch(); lrb != nil {
		rdr := q.Branches().GetStateReaderForTheBranch(lrb.TxID())
		ret = rdr.IsKnownController(ledger.SigLock(acc).ControllerID())
	} else {
		ret = true
	}
	return
}

func (q *TxSenders) cleanup() {
	if ledger.IsReset() {
		return
	}
	nowTicks := ledger.TimeNow().TicksSinceGenesis()
	if nowTicks < cleanupHorizonTicks {
		return
	}
	maps.DeleteFunc(q.txSenders, func(_ base.HolderID, timestamps *seenTimestamps) bool {
		return timestamps.sequencer.lastestTicksSinceGenesis() < nowTicks-cleanupHorizonTicks &&
			timestamps.nonSequencer.lastestTicksSinceGenesis() < nowTicks-cleanupHorizonTicks
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

// addTs checks if ts is closer than allowed to any of already recorded, then tx will be ignored.
// Otherwise, ts is added to the ring buffer.
// Returns true if tx passes the check, otherwise it should be ignored
func (t *tsRingBuffer) addTs(ticksSinceGenesis, minAllowedDiff int64) (pass bool) {
	n := 0
	for _, ticks := range t.timestamps {
		if util.Abs(ticksSinceGenesis-ticks) < minAllowedDiff {
			n++
		}
		if n >= concentrationTolerance {
			break
		}
	}
	t.timestamps[t.counter] = ticksSinceGenesis
	t.counter = (t.counter + 1) % byte(keepTimestamps)
	return n < concentrationTolerance
}

func (t *tsRingBuffer) lastestTicksSinceGenesis() (ret int64) {
	for _, ticks := range t.timestamps {
		if ticks > ret {
			ret = ticks
		}
	}
	return
}
