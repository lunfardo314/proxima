package workflow

import (
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"
)

const (
	// branchListMaxSlots bounds the slot range of one PullBranchList request, so one request costs
	// a node at most this many slot scans of its txstore.
	branchListMaxSlots = 150

	// The txstore keeps at least this much history behind the dagstores' sealed branch: the reach
	// of forward sync, which other nodes serve from their txstore.
	defaultTxStoreKeepSlots = 17480 // about two days
	minTxStoreKeepSlots     = 8740  // about one day
	purgeRoundPeriod        = 10 * time.Second
	purgeSlotsPerRound      = 20
)

// txStorePurger deletes the node's txstore from the oldest slot up to the floor: the newest sealed
// branch a trusted dagstore announced, on this node's own lineage, minus keepSlots. The floor only
// rises; without a heartbeat nothing is deleted.
type txStorePurger struct {
	keepSlots   uint32
	floor       atomic.Uint32
	floorGauge  prometheus.Gauge
	slotsPurged prometheus.Counter
}

// serveDAGStores answers the lineage requests of the node's trusted dagstores, from the txstore
// alone: the lineage a dagstore needs reaches back as far as the txstore does, the ledger state
// does not. It also starts the purger, which needs its own opt-in: declaring a dagstore never
// deletes anything by itself.
func (w *Workflow) serveDAGStores() {
	if !w.peers.HasTrustedDAGStores() {
		return
	}
	w.peers.OnPullLRB(w.answerPullLRB)
	w.peers.OnPullBranchList(w.answerPullBranchList)
	if viper.GetBool("txstore.purge") {
		w.startTxStorePurger()
	}
}

func (w *Workflow) answerPullLRB(from peer.ID) {
	lrb := w.GetLatestReliableBranch()
	if lrb == nil {
		return
	}
	r := peering.LRBReply{LRB: lrb.Stem.ID.TransactionID()}
	if src, ok := w.TxBytesStore().(txstore.Iterable); ok {
		r.OldestSlot, _ = txstore.OldestSlot(src)
	}
	if w.purger != nil {
		r.PurgeFloor = w.purger.floor.Load()
	}
	w.peers.SendLRBReply(from, r)
}

func (w *Workflow) answerPullBranchList(from peer.ID, fromSlot, toSlot uint32) {
	src, ok := w.TxBytesStore().(txstore.Iterable)
	if !ok || toSlot < fromSlot || toSlot-fromSlot >= branchListMaxSlots {
		return
	}
	entries := make([]peering.BranchListEntry, 0)
	for slot := fromSlot; ; slot++ {
		for _, branchID := range txstore.BranchesInSlot(src, slot) {
			tx, err := transaction.Parse(w.TxBytesStore().GetTxBytes(&branchID))
			if err != nil {
				continue
			}
			if pred, ok := tx.StemPredecessor(); ok {
				entries = append(entries, peering.BranchListEntry{Branch: branchID, Predecessor: pred})
			}
		}
		if slot == toSlot {
			break
		}
	}
	w.peers.SendBranchListReply(from, fromSlot, toSlot, entries)
}

func (w *Workflow) startTxStorePurger() {
	store, ok := w.TxBytesStore().(*txstore.SimpleTxBytesStore)
	if !ok {
		w.Log().Warnf("[txstore purger] the txstore cannot delete; purging is off")
		return
	}
	keep := viper.GetInt("txstore.keep_slots")
	if keep == 0 {
		keep = defaultTxStoreKeepSlots
	}
	if keep < minTxStoreKeepSlots {
		w.Log().Warnf("[txstore purger] txstore.keep_slots %d is below the minimum, %d is used", keep, minTxStoreKeepSlots)
		keep = minTxStoreKeepSlots
	}
	p := &txStorePurger{
		keepSlots: uint32(keep),
		floorGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "proxima_txstore_purge_floor",
			Help: "slot below which the txstore is deleted: the trusted dagstores' sealed branch minus txstore.keep_slots",
		}),
		slotsPurged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "proxima_txstore_slots_purged",
			Help: "slots deleted from the txstore by the purger",
		}),
	}
	w.MetricsRegistry().MustRegister(p.floorGauge, p.slotsPurged)
	w.purger = p

	w.peers.OnDAGStoreHeartbeat(func(from peer.ID, trusted bool, hb peering.DAGStoreHeartbeat) {
		if trusted {
			w.raisePurgeFloor(hb.SealedBranch)
		}
	})
	w.RepeatInBackground("txstore_purger", purgeRoundPeriod, func() bool {
		w.purgeRound(store)
		return true
	})
	w.Log().Infof("[txstore purger] on: keeping %d slots behind the trusted dagstores' sealed branch", p.keepSlots)
}

// raisePurgeFloor takes a sealed branch announced by a trusted dagstore. It counts only if this
// node has committed it, i.e. it is on the node's own lineage: a node on another fork deletes
// nothing until a branch of its own lineage is sealed.
func (w *Workflow) raisePurgeFloor(sealed base.TransactionID) {
	if !sealed.IsBranchTransaction() || sealed.Slot() <= w.purger.keepSlots {
		return
	}
	if _, found := multistate.FetchRootRecord(w.StateStore(), sealed); !found {
		return
	}
	floor := sealed.Slot() - w.purger.keepSlots
	for {
		cur := w.purger.floor.Load()
		if floor <= cur {
			return
		}
		if w.purger.floor.CompareAndSwap(cur, floor) {
			w.purger.floorGauge.Set(float64(floor))
			return
		}
	}
}

// purgeRound deletes up to purgeSlotsPerRound slots below the floor, oldest first.
func (w *Workflow) purgeRound(store *txstore.SimpleTxBytesStore) {
	floor := w.purger.floor.Load()
	for i := 0; i < purgeSlotsPerRound; i++ {
		oldest, found := txstore.OldestSlot(store)
		if !found || oldest >= floor {
			return
		}
		n, err := store.DeleteSlot(oldest)
		if err != nil {
			w.Log().Errorf("[txstore purger] slot %d: %v", oldest, err)
			return
		}
		w.purger.slotsPurged.Inc()
		w.Tracef("txstore_purger", "deleted slot %d: %d transactions", oldest, n)
	}
}

// TxStoreKeepSlots is how many slots of history the txstore keeps while the purger runs, 0 when
// it does not: older transactions are then only at the trusted dagstores.
func (w *Workflow) TxStoreKeepSlots() uint32 {
	if w.purger == nil {
		return 0
	}
	return w.purger.keepSlots
}

// IsPurgedTxID tells whether the txid is older than what this node's txstore keeps.
func (w *Workflow) IsPurgedTxID(txid base.TransactionID) bool {
	keep := w.TxStoreKeepSlots()
	return keep > 0 && txid.Slot()+keep < ledger.SlotNow()
}
