package txinput_queue

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
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
		GossipTxBytesToPeers(txBytes []byte, txid base.TransactionID, except ...peer.ID)
		MustPersistTxBytes(tx *transaction.Transaction)
		CheckTxSenderConfig() (checkSeq, checkNonSeq bool)
		MaxConcurrentAttachers() int
		GetOwnSequencerID() *base.ChainID
		AttachFun() func(tx *transaction.Transaction, opts ...attacher.AttachTxOption)
		EvidenceNumberOfTxDependencies(n int)
		// EvidenceValidTxFromPeer records peer activity only after the transaction
		// parsed and passed the signature check.
		EvidenceValidTxFromPeer(id peer.ID)
		// EvidenceTxVersionMismatch lets peering penalize a peer that keeps sending
		// transactions from an incompatible version.
		EvidenceTxVersionMismatch(id peer.ID)
		// RecordBranchSlotFromPeers bumps the high-water mark of branch slots heard
		// from peers. It is the forward-sync anchor.
		RecordBranchSlotFromPeers(slot uint32)
		PostEventNewMiningTx(txid base.TransactionID, txBytes []byte)
		GetVertex(txid base.TransactionID) *vertex.WrappedTx
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
		inGate       *inGate[base.TransactionID]
		sendersMutex sync.Mutex
		txSenders    map[base.HolderID]*seenTimestamps
		// sender pace config
		checkSeq    bool
		checkNonSeq bool
		// deadlock prevention: latest attached sequencer tx timestamp (attacher-cap rate control)
		latestAttachedTimestamp atomic.Int64
		// slot of the last early mine transit the operator was warned about
		earlyMineWarnedSlot uint32
		// transactions refused at parsing, reported at most once per parseFailureReportPeriod
		parseFailures parseFailures
		// mine solutions admitted unsolicited, by signer, predecessor and VRF output, with
		// their slot for pruning: later copies of one solution are dropped
		mineSolutionsMutex sync.Mutex
		mineSolutions      map[string]uint32
		mineSolutionsSlot  uint32
		// mine outputs produced by transits this node validated at ingress, with their
		// bytes: a later transit spending one is validated against it
		mineOutputsMutex sync.Mutex
		mineOutputs      map[base.OutputID][]byte
		mineOutputsSlot  uint32
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
		parseFailedCounter    prometheus.Counter
	}

	// parseFailures accumulates the transactions refused at parsing between two warnings
	parseFailures struct {
		mutex      sync.Mutex
		lastReport time.Time
		count      int
		bySource   map[string]int
		lastErr    string
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
	inGateMaxSize           = 200_000
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

	// a sender on another transaction version is refused at parsing, every transaction of it;
	// one warning per period sums them up
	parseFailureReportPeriod = 10 * time.Second
)

func init() {
	util.Assertf(concentrationTolerance <= keepTimestamps, "wrong constants: expected concentrationTolerance <= keepTimestamps")
}

func New(env environment) *TxInputQueue {
	ret := &TxInputQueue{
		environment:   env,
		inGate:        newInGate[base.TransactionID](inGateBlackListTTLSlots*ledger.L(0).SlotDuration(), cleanIfExceeds, inGateMaxSize),
		txSenders:     make(map[base.HolderID]*seenTimestamps),
		mineSolutions: make(map[string]uint32),
		mineOutputs:   make(map[base.OutputID][]byte),
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
		ret.sendersMutex.Lock()
		ret.txSenders = maps.Clone(ret.txSenders)
		ret.sendersMutex.Unlock()
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
	// A sequencer transaction older than the memDAG TTL brings no news: it is committed or
	// orphaned, and a past cone that still needs it pulls it. Replayed unsolicited, it would
	// re-attach retained vertices of its past cone, each with its own attacher. Dropped before
	// dedup, persist and gossip, so a replay goes no further than the first node.
	if inp.PrefixTxID.IsSequencerTransaction() &&
		inp.PrefixTxID.Slot()+memdag.VertexTTLSlots() < ledger.TimeNow().Slot &&
		!q.inGate.wasPulled(inp.PrefixTxID) {
		q.IncCounter("old_seq_drop")
		return
	}
	// dedup check using message prefix
	pass, wanted := q.inGate.checkPass(inp.PrefixTxID)
	if !pass {
		q.filterHitCounter.Inc()
		return
	}
	// parse (stage 1)
	tx, err := transaction.Parse(inp.TxBytes)
	if err != nil {
		q.inGate.unsee(inp.PrefixTxID)
		q.evidenceParseFailure("peer "+inp.FromPeer.ShortString(), err)
		// A peer on another transaction version fails here on every transaction.
		// Let peering penalize one that keeps doing it; a single mismatch is noise.
		if errors.Is(err, transaction.ErrTxVersionMismatch) {
			q.EvidenceTxVersionMismatch(inp.FromPeer)
		}
		return
	}
	// consistency check: message prefix must match real txid
	if tx.ID() != inp.PrefixTxID {
		q.inGate.unsee(inp.PrefixTxID)
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
		q.evidenceParseFailure("API ("+from.String()+")", err)
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
			// Drop, but do NOT mark the txid BAD: the upper bound is this node's
			// wall clock, not ledger validity. A tx too far in the future here may
			// be perfectly valid and accepted by a node with a faster clock, and
			// becomes acceptable on this node once its clock advances. Marking it
			// BAD would make a transient clock-skew rejection sticky and could
			// diverge local views of a still-valid txid.
			msg := fmt.Sprintf("enforcing time bounds (from peer %s): %v", fromPeer, err)
			q.LogTx(time.Now(), msg, txid)
			q.Log().Warnf("%s -- %s", msg, txid.StringShort())
			return
		}
		q.LogTx(time.Now(), err.Error(), txid)
		q.Log().Warnf("(from peer '%s') %v -- %s", fromPeer, err, txid.StringShort())
	}

	// --- fair-launch mine proof-of-work floor (spam gate) ---
	// A mining-shaped transaction is exempt from the unknown-sender pace gate
	// (a fresh miner's holder is legitimately not yet known on the ledger), so
	// without this an attacker can flood valid-signature junk shaped like a mine
	// transit (throwaway key, fabricated input) that gets persisted to the
	// append-only txstore and gossiped before full validation. Require at least
	// the mine floor difficulty of proof-of-work here, before persist/gossip and
	// the miner fan-out. Every genuine transit satisfies K >= floor, so this
	// never rejects a valid transit; it only sheds the cheapest junk. The
	// authoritative, live-difficulty PoW check stays in the mineLock constraint
	// at full validation (this cannot enforce the live retargeted difficulty: a
	// lagging node may not hold the exact predecessor it is computed against).
	if !wanted && tx.IsMiningTransaction() {
		// Quiet start: the mineLock refuses any transit stamped before the start
		// slot, but that verdict comes at full validation, after persist and
		// gossip. Drop an early transit here so a rushing miner's work does not
		// travel the network, and tell the operator who is rushing (once per slot).
		if slot, until := tx.Timestamp().Slot, ledger.L(tx.Timestamp().Slot).DisableMiningUntilSlot; slot < until {
			q.IncCounter("mine_early_drop")
			q.LogTx(time.Now(), fmt.Sprintf("mining tx before the start slot %d -> IGNORED", until), txid)
			if q.earlyMineWarnedSlot != slot {
				q.earlyMineWarnedSlot = slot
				q.Log().Warnf("tx %s: mining tx before the start slot %d (opens %s) -> IGNORED; a miner is rushing the quiet start",
					txid.StringShort(), until, ledger.ClockTime(base.T(until, 0)).UTC().Format(time.DateTime+" UTC"))
			}
			return
		}
		if !mineProofOfWorkMeetsFloor(tx) {
			q.IncCounter("mine_pow_drop")
			q.LogTx(time.Now(), "mining tx below floor proof-of-work -> IGNORED", txid)
			q.WarnTopicf("rate_control", 1, "tx %s: mining tx below floor proof-of-work -> IGNORED", txid.StringShort())
			return
		}
	}

	// --- partial context validation (signature etc) ---
	if err := tx.ValidatePartialContext(true); err != nil {
		err = fmt.Errorf("error while pre-validating transaction %s: '%w'", txid.StringShort(), err)
		q.LogTx(time.Now(), err.Error(), txid)
		attacher.InvalidateTxID(txid, q.attacherEnv(), err)
		return
	}

	// Count only transactions that parsed and passed the signature check as churn
	// activity. Raw gossip frames include ones refused at parsing, so a peer on an
	// incompatible version could otherwise look active while sending only junk.
	if fromPeer != "" && fromPeer != q.SelfPeerID() {
		q.EvidenceValidTxFromPeer(fromPeer)
	}

	// --- one transit per mine solution ---
	// A miner may send one solution as several transits, one per sequencer, so that
	// whichever sequencer includes first holds one. The copies conflict by construction
	// (they spend the same mine output), only one can be included, and each costs the
	// network a full transaction. The first copy of a solution to arrive unsolicited is
	// admitted and later ones are dropped before persist and gossip; an improvement has
	// another VRF output and passes. A pulled copy always passes: some milestone needs
	// it. Checked after the signature, so the signer in the key is genuine and a junk
	// transaction reusing another miner's proof cannot crowd out the miner's own.
	if !wanted && tx.IsMiningTransaction() && !q.firstCopyOfMineSolution(tx) {
		q.IncCounter("mine_copy_drop")
		q.LogTx(time.Now(), "copy of an already received mine solution -> IGNORED", txid)
		return
	}

	// --- mine transit validated at ingress ---
	// The floor gate above reads an unverified proof, and the floor is far below the
	// live difficulty, so floor-passing junk is cheap. An unsolicited transit is
	// therefore validated in full against its predecessor when that predecessor is
	// proven, which holds it to the live difficulty before it is relayed. One whose
	// predecessor is not proven here is kept but neither gossiped nor streamed, so
	// junk on a fabricated input goes no further than the node it was sent to; a
	// milestone that needs it pulls it.
	relay := true
	if !wanted && tx.IsMiningTransaction() {
		valid, known := q.validateMineTransit(tx)
		switch {
		case !known:
			relay = false
			q.IncCounter("mine_unknown_pred")
			q.LogTx(time.Now(), "mining tx with predecessor not proven on this node -> not relayed", txid)
		case !valid:
			q.IncCounter("mine_invalid_drop")
			q.LogTx(time.Now(), "invalid mining tx -> IGNORED", txid)
			q.WarnTopicf("rate_control", 1, "tx %s: invalid mining tx -> IGNORED", txid.StringShort())
			return
		}
	}

	// record the branch slot for forward-sync anchoring. Only genuine peer branches
	// count: not API submissions (fromPeer == "") and not our own gossiped txs
	// (fromPeer == self), so a lone sequencer never self-triggers forward-sync.
	if tx.IsBranchTransaction() && fromPeer != "" && fromPeer != q.SelfPeerID() {
		q.RecordBranchSlotFromPeers(tx.Timestamp().Slot)
	}

	q.EvidenceNumberOfTxDependencies(tx.NumInputs() + tx.NumEndorsements())

	if !txid.IsSequencerTransaction() {
		q.nonSequencerTxCounter.Inc()
	}

	// --- persist to txstore ---
	q.MustPersistTxBytes(tx)

	// --- gossip (non-pulled only) ---
	if !wanted && relay {
		q.GossipTxBytesToPeers(tx.Bytes(), txid)
		q.gossipedCounter.Inc()
	}

	// Stream fair-launch mine-chain transits to subscribed miners. Posted here,
	// after persistence and before the attach gate below: the gate drops
	// unsolicited non-sequencer transactions on a node without a local
	// sequencer, so a later hook would never fire on an access node. The event
	// carries the raw bytes precisely because the transaction is not
	// constraint-validated yet — subscribers verify the mine-chain rules,
	// proof of work included, for themselves.
	if tx.IsMiningTransaction() && relay {
		q.PostEventNewMiningTx(txid, tx.Bytes())
	}

	// --- attach gate decision ---
	pulled := wanted ||
		meta.SourceTypeNonPersistent == txmetadata.SourceTypePulled ||
		meta.SourceTypeNonPersistent == txmetadata.SourceTypeTxStore ||
		// Own sequencer milestones must always attach — they are this node's own backbone
		// and cannot be pulled back if dropped. The local proposer tags them
		// SourceTypeSequencer; that field is non-persistent, so it is never set on
		// peer-gossiped txs (which arrive with no source tag). Without this exemption the
		// branches-only gossip gate (see shouldAttach) would drop our own non-branch
		// milestones. `wanted` stays false, so the milestone is still gossiped to peers.
		meta.SourceTypeNonPersistent == txmetadata.SourceTypeSequencer

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
			// The bootstrap gate compares the transaction's slot with this node's LRB, so it is
			// judged again now: at reception the LRB was behind the slot of any transaction that
			// came early, and a sender whose clock runs ahead would pass it every slot.
			if !pulled && q.dropStaleBootstrapTx(tx) {
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

// shouldAttach decides whether to attach a transaction or only keep it in the txstore.
// Pulled (solicited) transactions always attach. Unsolicited gossip is subject to the
// resource gates below plus a sync-mode filter: while the node is in sync mode (a forward-sync
// target is pending, global.SyncTargetsPending()), ONLY
// branches are attached. Non-seq and non-branch sequencer milestones feed the sequencer
// backlog / tippool, which are not needed for catch-up — they stay in the txstore and
// are pulled on demand if a branch's past cone requires them. Branches anchor lineage
// and advance committed state, so they keep attaching (subject to the attacher cap).
// See kb/sync_semantics.md §3-§4.
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

	syncing := global.SyncTargetsPending()

	if txid.IsSequencerTransaction() {
		if syncing && !txid.IsBranchTransaction() {
			q.IncCounter("sync_drop")
			return false
		}
		return q.shouldAttachSequencer(tx)
	}
	// non-sequencer
	if syncing {
		q.IncCounter("sync_drop")
		return false
	}
	return q.shouldAttachNonSeq(tx)
}

// shouldAttachSequencer applies the attacher-cap rate control with anti-starvation
// (older txs pass even at the cap). Far-ahead sequencer/branch gossip is intentionally
// NOT shed here: it must be allowed to attach so its past-cone recursion reaches the
// depth cap and flips the sync-mode counter that triggers forward sync.
func (q *TxInputQueue) shouldAttachSequencer(tx *transaction.Transaction) bool {
	txid := tx.ID()
	if q.dropStaleBootstrapTx(tx) {
		return false
	}
	txTicks := txid.Timestamp().TicksSinceGenesis()

	// Attachers of milestones already older than the memDAG TTL are catch-up or re-attach work
	// that this gate never admitted. Outside forward sync they do not count: stuck ones would
	// otherwise hold the cap for a whole TTL and the node would refuse every live milestone.
	nAtt := attacher.NumAttachers()
	if !global.SyncTargetsPending() {
		nAtt -= attacher.NumPastAttachers()
	}
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

// staleBootstrapTx reports whether an unsolicited sequencer transaction is a bootstrap
// transaction this node must not attach: one whose explicit baseline this node does not need,
// because it does not see the network as stuck at the transaction's slot (the same condition
// under which the bootstrap proposer issues one). A bootstrap transaction re-anchors a stuck
// network to a committed state; when this node's LRB shows branches around that slot, the
// sender's LRB is frozen and its transaction would only re-spend a long-committed chain output
// every slot. Such a transaction is ignored, never invalidated: it is valid against its own
// baseline and another sequencer may still consolidate it, in which case the branch's past cone
// pulls it in. Returns the explicit baseline and this node's LRB slot for the log line.
func (q *TxInputQueue) staleBootstrapTx(tx *transaction.Transaction) (baseline base.TransactionID, lrbSlot uint32, stale bool) {
	baseline, isBootstrap := tx.ExplicitBaseline()
	if !isBootstrap {
		return
	}
	lrb := q.Branches().FindLatestReliableBranch()
	if lrb == nil {
		return
	}
	lrbSlot = lrb.Stem.ID.Slot()
	stale = !global.NetworkStuckAt(lrbSlot, tx.Slot())
	return
}

// dropStaleBootstrapTx applies staleBootstrapTx and accounts for the drop.
func (q *TxInputQueue) dropStaleBootstrapTx(tx *transaction.Transaction) bool {
	baseline, lrbSlot, stale := q.staleBootstrapTx(tx)
	if !stale {
		return false
	}
	txid := tx.ID()
	q.IncCounter("bootstrap_drop")
	msg := fmt.Sprintf("bootstrap tx %s of sequencer %s (%s) on explicit baseline %s IGNORED: the network is branching (LRB slot %d)",
		txid.StringShort(), tx.SequencerTransactionData().SequencerID.StringShort(), sequencerName(tx), baseline.StringShort(), lrbSlot)
	q.LogTx(time.Now(), msg, txid)
	q.Log().Warnf("%s", msg)
	return true
}

// sequencerName is the name a sequencer transaction declares in its sequencer output data.
func sequencerName(tx *transaction.Transaction) string {
	if seqData := tx.SequencerTransactionData(); seqData != nil && seqData.SequencerOutputData != nil &&
		seqData.SequencerOutputData.SequencerData != nil {
		return seqData.SequencerOutputData.SequencerData.Name()
	}
	return "(N/A)"
}

// shouldAttachNonSeq decides whether to attach an unsolicited non-sequencer transaction.
// Non-seq transactions don't spawn attacher goroutines, so they're cheap to attach.
//
//   - Access node (no local sequencer): drop all. The access node doesn't issue transactions,
//     it only constructs the DAG from others. Everything it needs can be pulled.
//   - Sequencer node: always attach if tx targets local sequencer (this is the sequencer's
//     mempool — if dropped, it can't be pulled back). Drop all others.
//     The overall non-seq rate is controlled by the sequencer's attachment budget, not by dropping.
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

	q.sendersMutex.Lock()
	seen := q.txSenders[holderID]
	q.sendersMutex.Unlock()

	if seen == nil {
		if !q.isHolderKnownInLRB(holderID) {
			// An unknown sender is ambiguous: it may be spam, or this node may be lagging
			// and not yet have seen the sender funded. Two exemptions resolve it.
			// Mining transactions taking the opening payout option: a fresh miner's
			// holder ID is not yet known on the ledger, and the opening is how it
			// becomes known; the mineLock structure gates the tx. The other options
			// are for miners the ledger knows, so a miner that keeps changing its key
			// is held to the opening, the dearest option (kb/mine_payout_options.md).
			// Branch transactions: the check is made against THIS node's LRB, so refusing a
			// branch because a stale view does not recognize its sequencer would block the
			// catch-up that would fix the staleness. Dropping a non-branch tx is cheap by
			// comparison — it can be pulled back later.
			if !tx.IsBranchTransaction() && !mineOpeningTx(tx) {
				txLogMsg := fmt.Sprintf("tx sender %s is not known in LRB -> IGNORED", ledger.SigLock(holderID).String())
				q.LogTx(time.Now(), txLogMsg, tx.ID())
				q.WarnTopicf("rate_control", 1, "tx %s : %s", tx.IDShortString(), txLogMsg)
				return false
			}
		}
		seen = &seenTimestamps{}
	}

	var pass bool
	txTs := tx.Timestamp()
	lib := ledger.L(txTs.Slot)
	switch {
	case tx.IsBranchTransaction():
		// branches are pace-exempt (the final pre-branch consolidation may land
		// one tick before the branch); mirrors the ledger scanInputs exemption.
		pass = true
	case tx.IsMiningTransaction():
		// a miner improving its own solution for a contested slot submits two
		// transits stamped in the same slot (kb/mine_conflict_rule.md); the
		// floor proof-of-work gate below is what bounds mine transactions
		pass = true
	case tx.IsSequencerTransaction():
		pass = !q.checkSeq || seen.sequencer.addTs(txTs.TicksSinceGenesis(), int64(lib.TransactionPaceSequencer))
	default:
		pass = !q.checkNonSeq || seen.nonSequencer.addTs(txTs.TicksSinceGenesis(), int64(lib.TransactionPace))
	}

	q.sendersMutex.Lock()
	q.txSenders[holderID] = seen
	q.sendersMutex.Unlock()
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
	q.sendersMutex.Lock()
	defer q.sendersMutex.Unlock()

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

// mineSolutionSlots is how long a solution is remembered: the tag-along window, within
// which a transit can still be included
const mineSolutionSlots = 32

// firstCopyOfMineSolution records the solution a mine transit carries and reports
// whether it is the first transit seen with it: same signer, same predecessor, same
// VRF output. A transit whose VRF output cannot be read is let through to validation.
func (q *TxInputQueue) firstCopyOfMineSolution(tx *transaction.Transaction) bool {
	beta, ok := tx.MineVRFOutput()
	if !ok {
		return true
	}
	holderID, err := tx.HolderID()
	if err != nil {
		return true
	}
	pred := tx.MustInputAt(0)
	key := string(holderID[:]) + string(pred[:]) + string(beta)
	slot := tx.Slot()

	q.mineSolutionsMutex.Lock()
	defer q.mineSolutionsMutex.Unlock()

	if _, seen := q.mineSolutions[key]; seen {
		return false
	}
	q.mineSolutions[key] = slot
	if slot > q.mineSolutionsSlot {
		q.mineSolutionsSlot = slot
		for k, sl := range q.mineSolutions {
			if sl+mineSolutionSlots < slot {
				delete(q.mineSolutions, k)
			}
		}
	}
	return true
}

// validateMineTransit validates a mine transit in full against the output it
// spends. known is false when that output is not proven on this node; valid then
// says nothing. A valid transit's mine output is remembered for its successors.
func (q *TxInputQueue) validateMineTransit(tx *transaction.Transaction) (valid, known bool) {
	pred := tx.MustInputAt(0)
	predBytes, known := q.provenMineOutput(pred)
	if !known {
		return false, false
	}
	if validateAgainst(tx, predBytes) != nil {
		return false, true
	}
	q.addMineOutput(tx.OutputID(0), tx.MustOutputDataAt(0), tx.Slot())
	return true, true
}

// validateAgainst validates a single-input transaction in full context, its input
// being predBytes. It works on a fresh parse: the full context must not stay on the
// transaction being attached.
func validateAgainst(tx *transaction.Transaction, predBytes []byte) error {
	vtx, err := transaction.Parse(tx.Bytes())
	if err != nil {
		return err
	}
	pred := tx.MustInputAt(0)
	err = vtx.SetFullContextWithFetch(func(oid base.OutputID) ([]byte, bool) {
		return predBytes, oid == pred
	})
	if err != nil {
		return err
	}
	return vtx.ValidateFullContext()
}

// provenMineOutput returns the bytes of a mine output this node can vouch for: one
// in the latest reliable branch state, or one newer than that branch produced by a
// transit this node validated, at ingress or in the memDAG. Spending anything older
// is a conflict in the branch's lineage, and the pace relief of a long gap would
// lower the required difficulty toward the floor.
func (q *TxInputQueue) provenMineOutput(oid base.OutputID) ([]byte, bool) {
	lrb := q.GetLatestReliableBranch()
	if lrb == nil {
		return nil, false
	}
	lrbID := lrb.TxID()
	if data, ok := q.Branches().GetStateReaderForTheBranch(lrbID).GetUTXO(oid); ok {
		return data, true
	}
	if oid.Slot() <= lrbID.Slot() {
		return nil, false
	}
	q.mineOutputsMutex.Lock()
	data, ok := q.mineOutputs[oid]
	q.mineOutputsMutex.Unlock()
	if ok {
		return data, true
	}
	vid := q.GetVertex(oid.TransactionID())
	if vid == nil {
		return nil, false
	}
	fromTx := func(tx *transaction.Transaction) {
		if vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) && vid.GetTxStatusNoLock() != vertex.Bad &&
			int(oid.Index()) < tx.NumProducedOutputs() {
			data, ok = tx.MustOutputDataAt(oid.Index()), true
		}
	}
	vid.RUnwrap(vertex.UnwrapOptions{
		Vertex:         func(v *vertex.Vertex) { fromTx(v.Transaction) },
		DetachedVertex: func(v *vertex.DetachedVertex) { fromTx(v.Transaction) },
	})
	return data, ok
}

// addMineOutput remembers a validated mine output for mineSolutionSlots
func (q *TxInputQueue) addMineOutput(oid base.OutputID, data []byte, slot uint32) {
	q.mineOutputsMutex.Lock()
	defer q.mineOutputsMutex.Unlock()

	q.mineOutputs[oid] = data
	if slot > q.mineOutputsSlot {
		q.mineOutputsSlot = slot
		for id := range q.mineOutputs {
			if id.Slot()+mineSolutionSlots < slot {
				delete(q.mineOutputs, id)
			}
		}
	}
}

// mineOpeningTx reports whether tx is a mining transaction taking the opening
// payout option, the one a holder unknown to the ledger may take.
func mineOpeningTx(tx *transaction.Transaction) bool {
	if !tx.IsMiningTransaction() {
		return false
	}
	option, ok := tx.MinePayoutOption()
	return ok && option == txbuildercore.MinePayoutOpening
}

// mineProofOfWorkMeetsFloor reports whether a mining-shaped transaction carries
// at least the mine floor difficulty of proof-of-work: the VRF output value the
// covenant tests (tx.MineProofOfWork64) has its low `floor` bits zero. The
// proof is decoded, not verified: the gate needs no ledger state and sheds
// zero-work junk before persist/gossip. The constraint's required difficulty K
// is always >= the floor, so a genuine transit always passes; the authoritative
// vrfVerify at the live K runs at full validation.
func mineProofOfWorkMeetsFloor(tx *transaction.Transaction) bool {
	floor := ledger.L(tx.Timestamp().Slot).MineFloorDifficulty
	if floor == 0 {
		return true
	}
	v, ok := tx.MineProofOfWork64()
	return ok && minePoWMeetsK(v, floor)
}

// minePoWMeetsK mirrors _minePoWOK in def/lock_mine.easyfl
// (equal(lshift64(rshift64($0,$1),$1), $0)): zeroing the low k bits of v leaves
// it unchanged exactly when those bits were already zero. A shift count >= 64
// yields 0, matching easyfl's rshift64/lshift64 convention (the mine difficulty
// is capped well below 64).
func minePoWMeetsK(v, k uint64) bool {
	return (v>>k)<<k == v
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

// evidenceParseFailure counts a transaction refused at parsing and warns at most once per
// parseFailureReportPeriod, naming how many were refused since the last warning, from which
// sources, and the latest error. Without the source a refused transaction cannot be traced: it
// has no ID or sender before it parses.
func (q *TxInputQueue) evidenceParseFailure(source string, err error) {
	q.parseFailedCounter.Inc()

	f := &q.parseFailures
	f.mutex.Lock()
	if f.bySource == nil {
		f.bySource = make(map[string]int)
	}
	f.count++
	f.bySource[source]++
	f.lastErr = err.Error()
	if time.Since(f.lastReport) < parseFailureReportPeriod {
		f.mutex.Unlock()
		return
	}
	count, lastErr := f.count, f.lastErr
	sources := make([]string, 0, len(f.bySource))
	for src, n := range f.bySource {
		sources = append(sources, fmt.Sprintf("%s: %d", src, n))
	}
	slices.Sort(sources)
	f.count, f.bySource, f.lastReport = 0, make(map[string]int), time.Now()
	f.mutex.Unlock()

	q.Log().Warnf("TxInputQueue: %d transaction(s) refused at parsing since the last report (%s); latest: %s",
		count, strings.Join(sources, ", "), lastErr)
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
		Help: "number of transactions rejected by the inGate as already seen",
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
	q.parseFailedCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_parseFailed",
		Help: "number of transactions refused at parsing, e.g. on another transaction version",
	})

	q.MetricsRegistry().MustRegister(
		q.inputTxCounter,
		q.pulledTxCounter,
		q.filterHitCounter,
		q.nonSequencerTxCounter,
		q.txBytesSizeReceived,
		q.gossipedCounter,
		q.parseFailedCounter,
	)
}

// AddPulledTransaction adds transaction short id to the wanted filter.
func (q *TxInputQueue) AddPulledTransaction(txid base.TransactionID) {
	q.inGate.addPulled(txid)
}
