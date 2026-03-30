package txinput_queue

import (
	"fmt"
	"maps"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
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

// TxInputQueue is the consolidated transaction input module.
// It handles dedup, parsing, stage-2 validation (signature, sender pace),
// persistence, gossip, rate-control gating, and attachment — all in one queue.

type (
	environment interface {
		global.NodeGlobal
		SelfPeerID() peer.ID
		GetLatestReliableBranch() (ret *multistate.BranchData)
		Branches() *branches.Branches
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID, except ...peer.ID)
		MustPersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata, txid ...base.TransactionID)
		CheckTxSenderConfig() (checkSeq, checkNonSeq bool)
		MaxConcurrentAttachers() int
		GetOwnSequencerID() *base.ChainID
		AttachFun() func(tx *transaction.Transaction, opts ...attacher.AttachTxOption)
		EvidenceNumberOfTxDependencies(n int)
	}

	Input struct {
		Cmd byte
		// PrefixTxID a prefix of transaction bytes received with CmdFromPeer, otherwise uninterpreted.
		// Should not be trusted. used only for gossip optimization and consistency checking
		// Real txid is calculated during base validation
		PrefixTxID base.TransactionID
		TxBytes    []byte
		TxMetaData *txmetadata.TransactionMetadata
		FromPeer   peer.ID
	}

	seenTimestamps struct {
		sequencer    tsRingBuffer
		nonSequencer tsRingBuffer
	}

	tsRingBuffer struct {
		timestamps [keepTimestamps]int64
		counter    byte
	}

	TxInputQueue struct {
		environment
		*core_modules.CoreModule[Input]
		inGate    *inGate[base.TransactionID]
		txSenders map[base.HolderID]*seenTimestamps
		// sender pace config
		checkSeq    bool
		checkNonSeq bool
		// deadlock prevention: latest attached sequencer tx timestamp
		latestAttachedTimestamp atomic.Int64
		// metrics
		metrics
	}

	metrics struct {
		inputTxCounter        prometheus.Counter
		pulledTxCounter       prometheus.Counter
		filterHitCounter      prometheus.Counter
		nonSequencerTxCounter prometheus.Counter
		txBytesSizeReceived   prometheus.Gauge
		gossipedCounter       prometheus.Counter
	}
)

const (
	CmdFromPeer = byte(iota)
	CmdFromAPI
)

const (
	Name = "txInputQueue"

	inGateBlackListTTLSlots = 60 // 10 min
	cleanIfExceeds          = 10_000
	blackListCleanupPeriod  = 10 * time.Second
	recreateMapPeriod       = time.Minute

	// sender pace constants (from txsenders)
	senderCleanupPeriod       = 10 * time.Second
	senderCleanupHorizonTicks = 360 * 127
	senderRebuildMapPeriod    = 5 * time.Minute
	keepTimestamps            = 4
	concentrationTolerance    = 1

	// time bounds
	maxSlotsInTheFuture = 6
)

func init() {
	util.Assertf(concentrationTolerance <= keepTimestamps, "wrong constants: expected concentrationTolerance <= keepTimestamps")
}

func New(env environment) *TxInputQueue {
	ret := &TxInputQueue{
		environment: env,
		inGate:      newInGate[base.TransactionID](inGateBlackListTTLSlots*ledger.L(0).SlotDuration(), cleanIfExceeds),
		txSenders:   make(map[base.HolderID]*seenTimestamps),
	}
	ret.checkSeq, ret.checkNonSeq = env.CheckTxSenderConfig()
	ret.CoreModule = core_modules.New[Input](env, Name, ret.consume)
	ret.CoreModule.Start()

	// inGate maintenance
	ret.RepeatInBackground(Name+"_inGateCleanup", blackListCleanupPeriod, func() bool {
		ret.inGate.purgeInGate()
		return true
	})
	ret.RepeatInBackground(Name+"_recreateMap", recreateMapPeriod, func() bool {
		ret.inGate.recreateMap()
		return true
	})

	// sender map maintenance
	ret.RepeatInBackground(Name+"_senderCleanup", senderCleanupPeriod, func() bool {
		ret.cleanupSenders()
		return true
	})
	ret.RepeatInBackground(Name+"_senderRebuildMap", senderRebuildMapPeriod, func() bool {
		ret.txSenders = maps.Clone(ret.txSenders)
		return true
	})

	ret.registerMetrics()
	return ret
}

func (q *TxInputQueue) consume(inp Input) {
	q.inputTxCounter.Inc()
	q.txBytesSizeReceived.Set(float64(len(inp.TxBytes)))

	switch inp.Cmd {
	case CmdFromPeer:
		q.fromPeer(&inp)
	case CmdFromAPI:
		q.fromAPI(&inp)
	default:
		q.Log().Fatalf("TxInputQueue: wrong cmd")
	}
}

// fromPeer handles transactions received from P2P gossip.
func (q *TxInputQueue) fromPeer(inp *Input) {
	// dedup check using message prefix
	pass, wanted := q.inGate.checkPass(inp.PrefixTxID)
	if !pass {
		q.filterHitCounter.Inc()
		return
	}
	// parse (stage 1)
	tx, err := transaction.Parse(inp.TxBytes)
	if err != nil {
		q.Log().Warnf("TxInputQueue: %v", err)
		return
	}
	// consistency check: message prefix must match real txid
	if tx.ID() != inp.PrefixTxID {
		q.Log().Warnf("TxInputQueue: tx message prefix (%s) != real txid (%s). Transaction IGNORED", inp.PrefixTxID.String(), tx.IDString())
		return
	}

	metaData := inp.TxMetaData
	if metaData == nil {
		metaData = new(txmetadata.TransactionMetadata)
	}
	if wanted {
		metaData.SourceTypeNonPersistent = txmetadata.SourceTypePulled
	}
	if inp.FromPeer == q.SelfPeerID() {
		q.LogTx(time.Now(), "received from sequencer", inp.PrefixTxID)
	} else {
		q.LogTx(time.Now(), fmt.Sprintf("received from peer %s", inp.FromPeer), inp.PrefixTxID)
	}

	q.processValidated(tx, metaData, inp.FromPeer, wanted)
}

// fromAPI handles transactions received from the API.
func (q *TxInputQueue) fromAPI(inp *Input) {
	from := txmetadata.SourceTypeAPI
	if inp.TxMetaData != nil {
		from = inp.TxMetaData.SourceTypeNonPersistent
	}
	tx, err := transaction.Parse(inp.TxBytes)
	if err != nil {
		q.Log().Warnf("TxInputQueue from '%s': %v", from.String(), err)
		return
	}
	txid := tx.ID()
	pass, _ := q.inGate.checkPass(txid)
	if !pass {
		q.filterHitCounter.Inc()
		return
	}
	q.LogTx(time.Now(), "received from API", txid)

	meta := &txmetadata.TransactionMetadata{
		SourceTypeNonPersistent: txmetadata.SourceTypeAPI,
	}
	if inp.TxMetaData != nil {
		meta.TxBytesReceived = inp.TxMetaData.TxBytesReceived
	}
	q.processValidated(tx, meta, "", false)
}

// processValidated runs stage-2 validation, persists, gossips, and decides attach/drop.
// This consolidates the former txsenders → attachTx → seq_attach/nonseq_attach pipeline.
func (q *TxInputQueue) processValidated(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata, fromPeer peer.ID, wanted bool) {
	txid := tx.ID()

	// --- stage 2: sender pace control (skip for pulled/wanted txs) ---
	if !wanted {
		if !q.checkSenderPace(tx) {
			return
		}
	}

	// --- time bounds check ---
	enforceTimeBounds := meta.SourceTypeNonPersistent == txmetadata.SourceTypeAPI ||
		meta.SourceTypeNonPersistent == txmetadata.SourceTypePeer
	if err := q.checkTimestampUpperBound(tx); err != nil {
		if enforceTimeBounds {
			msg := fmt.Sprintf("enforcing time bounds (from peer %s): %v", fromPeer, err)
			q.LogTx(time.Now(), msg, txid)
			q.Log().Warnf("%s -- %s", msg, txid.StringShort())
			attacher.InvalidateTxID(txid, q.attacherEnv(), err)
			return
		}
		q.LogTx(time.Now(), err.Error(), txid)
		q.Log().Warnf("(from peer '%s') %v -- %s", fromPeer, err, txid.StringShort())
	}

	// --- partial context validation (signature etc) ---
	if err := tx.ValidatePartialContext(true); err != nil {
		err = fmt.Errorf("error while pre-validating transaction %s: '%w'", txid.StringShort(), err)
		q.LogTx(time.Now(), err.Error(), txid)
		attacher.InvalidateTxID(txid, q.attacherEnv(), err)
		return
	}

	q.EvidenceNumberOfTxDependencies(tx.NumInputs() + tx.NumEndorsements())

	if !txid.IsSequencerTransaction() {
		q.nonSequencerTxCounter.Inc()
	}

	// --- persist to txstore ---
	q.MustPersistTxBytesWithMetadata(tx.Bytes(), meta, txid)

	// --- gossip (non-pulled only) ---
	if !wanted {
		q.GossipTxBytesToPeers(tx.Bytes(), meta, txid)
		q.gossipedCounter.Inc()
	}

	// --- attach gate decision ---
	pulled := wanted ||
		meta.SourceTypeNonPersistent == txmetadata.SourceTypePulled ||
		meta.SourceTypeNonPersistent == txmetadata.SourceTypeTxStore

	if !q.shouldAttach(tx, pulled) {
		return
	}

	// --- clock alignment: wait for ledger time before attaching ---
	attachOpts := []attacher.AttachTxOption{
		attacher.WithTransactionMetadata(meta),
		attacher.WithInvokedBy("txInput"),
		attacher.WithEnforceTimestampBeforeRealTime,
	}

	txTime := ledger.ClockTime(txid.Timestamp())
	if time.Until(txTime) <= 0 {
		q.doAttach(tx, attachOpts)
	} else {
		go func() {
			q.IncCounter("wait")
			defer q.DecCounter("wait")

			if !q.ClockCatchUpWithLedgerTime(txid.Timestamp()) {
				return
			}
			q.doAttach(tx, attachOpts)
		}()
	}
}

// doAttach calls _attach. The timestamp assertion is in attacher.AttachTransaction.
func (q *TxInputQueue) doAttach(tx *transaction.Transaction, opts []attacher.AttachTxOption) {
	nowis := time.Now()
	tsTime := tx.TimestampTime()
	util.Assertf(nowis.After(tsTime), "nowis(%d).After(tsTime(%d))", nowis.UnixNano(), tsTime.UnixNano())

	q.AttachFun()(tx, opts...)
}

// shouldAttach decides whether to attach or drop the transaction.
// Pulled transactions always pass. Non-pulled transactions are subject to resource gates.
func (q *TxInputQueue) shouldAttach(tx *transaction.Transaction, pulled bool) bool {
	if pulled {
		return true
	}
	txid := tx.ID()

	// snapshot load shedding
	if q.IsSnapshotting() {
		q.IncCounter("tx_drop")
		return false
	}

	if txid.IsSequencerTransaction() {
		return q.shouldAttachSequencer(tx)
	}
	return q.shouldAttachNonSeq(tx)
}

// shouldAttachSequencer implements the attacher cap with deadlock prevention
// (from former seq_attach module).
func (q *TxInputQueue) shouldAttachSequencer(tx *transaction.Transaction) bool {
	txid := tx.ID()
	txTicks := txid.Timestamp().TicksSinceGenesis()

	nAtt := attacher.NumAttachers()
	if nAtt >= q.MaxConcurrentAttachers() {
		if txTicks >= q.latestAttachedTimestamp.Load() {
			q.Tracef("sync", "tx_drop seq %s: att=%d >= cap=%d, txTicks=%d >= latest=%d",
				txid.StringShort, nAtt, q.MaxConcurrentAttachers(), txTicks, q.latestAttachedTimestamp.Load())
			q.IncCounter("seq_drop")
			return false
		}
		q.Tracef("sync", "tx_pass seq (older) %s: att=%d >= cap=%d, txTicks=%d < latest=%d",
			txid.StringShort, nAtt, q.MaxConcurrentAttachers(), txTicks, q.latestAttachedTimestamp.Load())
	}

	// update latest attached timestamp (consume is single-goroutine, no concurrent writers)
	if txTicks > q.latestAttachedTimestamp.Load() {
		q.latestAttachedTimestamp.Store(txTicks)
	}
	return true
}

// shouldAttachNonSeq decides whether to attach an unsolicited non-sequencer transaction.
// Non-seq transactions don't spawn attacher goroutines, so they're cheap to attach.
//
// - Access node (no local sequencer): drop all. The access node doesn't issue transactions,
//   it only constructs the DAG from others. Everything it needs can be pulled.
// - Sequencer node: always attach if tx targets local sequencer (this is the sequencer's
//   mempool — if dropped, it can't be pulled back). Drop all others.
//   The overall non-seq rate is controlled by the sequencer's attachment budget, not by dropping.
func (q *TxInputQueue) shouldAttachNonSeq(tx *transaction.Transaction) bool {
	seqID := q.GetOwnSequencerID()
	if seqID == nil {
		// access node: drop all unsolicited non-seq transactions
		q.IncCounter("nonseq_drop")
		return false
	}
	if tx.HasOutputForSequencer(*seqID) {
		// targets local sequencer — always attach (sequencer mempool)
		return true
	}
	q.IncCounter("nonseq_drop")
	return false
}

// --- sender pace control (absorbed from txsenders) ---

func (q *TxInputQueue) checkSenderPace(tx *transaction.Transaction) bool {
	holderID, err := tx.HolderID()
	if err != nil {
		txLogMsg := fmt.Sprintf("IGNORED: cannot parse holder ID: %v", err)
		q.LogTx(time.Now(), txLogMsg, tx.ID())
		q.Log().Warnf("tx %s: %s -> IGNORED", tx.IDShortString(), txLogMsg)
		return false
	}

	seen := q.txSenders[holderID]
	if seen == nil {
		if !q.isHolderKnownInLRB(holderID) {
			if !tx.IsBranchTransaction() {
				txLogMsg := fmt.Sprintf("tx sender %s is not known in LRB -> IGNORED", ledger.SigLock(holderID).String())
				q.LogTx(time.Now(), txLogMsg, tx.ID())
				q.WarnTopicf("rate_control", 1, "tx %s : %s", tx.IDShortString(), txLogMsg)
				return false
			}
		}
		seen = &seenTimestamps{}
		q.txSenders[holderID] = seen
	}

	var pass bool
	txTs := tx.Timestamp()
	lib := ledger.L(txTs.Slot)
	if tx.IsSequencerTransaction() {
		pass = !q.checkSeq || seen.sequencer.addTs(txTs.TicksSinceGenesis(), int64(lib.TransactionPaceSequencer))
	} else {
		pass = !q.checkNonSeq || seen.nonSequencer.addTs(txTs.TicksSinceGenesis(), int64(lib.TransactionPace))
	}
	q.txSenders[holderID] = seen
	if !pass {
		txLogMsg := fmt.Sprintf("timestamp is too close to another tx from the same sender %s -> IGNORED", holderID.String())
		q.LogTx(time.Now(), txLogMsg, tx.ID())
		q.WarnTopicf("rate_control", 1, "tx %s: %s", tx.IDShortString(), txLogMsg)
		return false
	}
	return true
}

func (q *TxInputQueue) isHolderKnownInLRB(acc base.HolderID) (ret bool) {
	if lrb := q.GetLatestReliableBranch(); lrb != nil {
		rdr := q.Branches().GetStateReaderForTheBranch(lrb.TxID())
		ret = rdr.IsKnownController(ledger.SigLock(acc).ControllerID())
	} else {
		ret = true
	}
	return
}

func (q *TxInputQueue) cleanupSenders() {
	if ledger.IsReset() {
		return
	}
	nowTicks := ledger.TimeNow().TicksSinceGenesis()
	if nowTicks < senderCleanupHorizonTicks {
		return
	}
	maps.DeleteFunc(q.txSenders, func(_ base.HolderID, timestamps *seenTimestamps) bool {
		return timestamps.sequencer.lastestTicksSinceGenesis() < nowTicks-senderCleanupHorizonTicks &&
			timestamps.nonSequencer.lastestTicksSinceGenesis() < nowTicks-senderCleanupHorizonTicks
	})
}

func (q *TxInputQueue) checkTimestampUpperBound(tx *transaction.Transaction) error {
	ts := ledger.ClockTime(tx.Timestamp())
	upperBound := time.Now().Add(maxSlotsInTheFuture * ledger.SlotDuration())
	if ts.After(upperBound) {
		return fmt.Errorf("transaction is %d msec too far in the future", int64(ts.Sub(upperBound))/int64(time.Millisecond))
	}
	return nil
}

// attacherEnv returns the attacher Environment via the workflow.
// This is a type assertion because the workflow implements attacher.Environment.
func (q *TxInputQueue) attacherEnv() attacher.Environment {
	return q.environment.(attacher.Environment)
}

// --- ring buffer for sender pace (absorbed from txsenders) ---

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

// --- metrics ---

func (q *TxInputQueue) registerMetrics() {
	q.inputTxCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_in",
		Help: "input queue counter",
	})
	q.pulledTxCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_pulled",
		Help: "number of pulled transactions",
	})
	q.filterHitCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_repeating",
		Help: "number of bloom filter hit",
	})
	q.nonSequencerTxCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_nonSequencer",
		Help: "number of non-sequencer transactions",
	})
	q.txBytesSizeReceived = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_txInputQueue_txBytesSize",
		Help: "size of the received transaction bytes",
	})
	q.gossipedCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_gossiped",
		Help: "number of gossiped",
	})

	q.MetricsRegistry().MustRegister(
		q.inputTxCounter,
		q.pulledTxCounter,
		q.filterHitCounter,
		q.nonSequencerTxCounter,
		q.txBytesSizeReceived,
		q.gossipedCounter,
	)
}

// AddPulledTransaction adds transaction short id to the wanted filter.
func (q *TxInputQueue) AddPulledTransaction(txid base.TransactionID) {
	q.inGate.addPulled(txid)
}
