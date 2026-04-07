package memdag

import (
	"fmt"
	"sort"
	"sync"
	"time"
	"weak"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/exp/maps"
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.Store
		DisableMemDAGGC() bool
		PostEventTxDeleted(txid base.TransactionID)
		IsSynced() bool
		SnapshotBranchID() base.TransactionID
		// IsVertexReferencedBySequencer returns true if the vertex is still referenced by
		// the sequencer's tippool, backlog, or own milestones. Returns false if no sequencer is running.
		IsVertexReferencedBySequencer(vid *vertex.WrappedTx) bool
	}

	_vertexRecord struct {
		*vertex.WrappedTx              // strong pointer to protect against GC
		weak.Pointer[vertex.WrappedTx] // weak pointer
	}
	// MemDAG is a global map of all in-memory vertices of the transaction DAG
	// branchVertexRecord tracks the set of vertices confirmed in a branch's past cone.
	// Used for fine-grained pruning: when a branch is deep enough behind the LRB,
	// all its vertices become prunable immediately.
	branchVertexRecord struct {
		predecessorBranchID base.TransactionID
		vertices            set.Set[*vertex.WrappedTx]
	}

	MemDAG struct {
		environment

		// cache of vertices as weak pointers. Key of the map is transaction id. Value of the map is *vertex.WrappedTx.
		// The pointer value *vertex.WrappedTx is used as a unique identifier of the transaction while being
		// loaded into the memory.
		// The vertices map may be seen as encoding table between transaction id and
		// more economic (memory-wise) yet transient in-memory id *vertex.WrappedTx
		// in most other data structures, such as attachers, transactions are represented as *vertex.WrappedTx
		mutex    sync.RWMutex
		vertices map[base.TransactionID]_vertexRecord

		latestBranchSlot        uint32
		latestHealthyBranchSlot uint32

		// per-branch vertex tracking for fine-grained pruning.
		// branchVertices maps branch ID to its past cone vertex set + predecessor link.
		// Protected by mutex (same as vertices map).
		branchVertices map[base.TransactionID]*branchVertexRecord

		metrics
	}

	metrics struct {
		numVerticesGauge prometheus.Gauge
		pipelineGauge    prometheus.Gauge
	}
)

func New(env environment) *MemDAG {
	ret := &MemDAG{
		environment:    env,
		vertices:       make(map[base.TransactionID]_vertexRecord),
		branchVertices: make(map[base.TransactionID]*branchVertexRecord),
	}
	if env != nil {
		ret.registerMetrics()
		if env.DisableMemDAGGC() {
			env.Log().Infof("[memdag cleanup] DISABLED")
		} else {
			ret.RepeatInBackground("memdag-GC", gcLoopPeriod, func() bool {
				nDetached, nDeleted := ret.doGC()
				if nDetached > 0 || nDeleted > 0 {
					env.Log().Infof("[memdag GC] detached: %d, deleted: %d", nDetached, nDeleted)
				}
				return true
			}, true)
		}

		ret.RepeatInBackground("memdag-stats", 10*time.Second, func() bool {
			nVertices := ret.NumVertices()
			pipeline := nVertices + ret.Counter("wait")
			env.Log().Infof("[memdag stats] vertices: %d", nVertices)
			ret.numVerticesGauge.Set(float64(nVertices))
			ret.pipelineGauge.Set(float64(pipeline))
			return true
		})
	}
	return ret
}

const (
	// vertexTTLSlots: wall-clock TTL — evict vertices added more than N wall-clock slots ago.
	vertexTTLSlots = 24
	// vertexLedgerTTLSlots: ledger-time TTL — evict vertices whose transaction slot is more than
	// N slots behind the latest committed branch. Handles forward-sync where vertices are
	// "fresh" by wall clock but ancient by ledger time.
	// Reduced from 48 to 12 to accelerate cleanup of orphaned vertices.
	vertexLedgerTTLSlots = 12

	// gcLoopPeriod: how often the full GC pass runs (unless triggered earlier by RequestPrune).
	gcLoopPeriod = 5 * time.Second

	// branchPruneDepth: vertices confirmed in branches this many slots behind the LRB
	// become eligible for detachment (if not referenced by sequencer).
	branchPruneDepth uint32 = 3
	// maxBranchVertexRecords: maximum entries in the branchVertices map.
	// If exceeded, force a cleanup regardless of LRB position.
	maxBranchVertexRecords = 20
	// staleLRBSlots: if the LRB is this many slots old, clear the branch tracking map entirely.
	staleLRBSlots uint32 = 24 // same as vertexTTLSlots
)

func (d *MemDAG) WithGlobalWriteLock(fun func()) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	fun()
}

func (d *MemDAG) GetVertexNoLock(txid base.TransactionID) *vertex.WrappedTx {
	if rec, found := d.vertices[txid]; found {
		return rec.Value()
	}
	return nil
}

func (d *MemDAG) GetVertex(txid base.TransactionID) *vertex.WrappedTx {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.GetVertexNoLock(txid)
}

func (d *MemDAG) NumVertices() int {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return len(d.vertices)
}

func (d *MemDAG) AddVertexNoLock(vid *vertex.WrappedTx) {
	txid := vid.ID()
	util.Assertf(d.GetVertexNoLock(txid) == nil, "d.GetVertexNoLock(vid.id())==nil")
	vid.SlotWhenAdded = ledger.TimeNow().Slot
	d.vertices[txid] = _vertexRecord{
		Pointer:   weak.Make(vid),
		WrappedTx: vid,
	}
}

// deleteFromMapNoLock removes the vertex from the map without posting events.
// Caller must collect the txid and post events after releasing the lock.
func (d *MemDAG) deleteFromMapNoLock(txid base.TransactionID) {
	if !txid.IsSequencerTransaction() && d.Counter("nonseq") > 0 {
		d.DecCounter("nonseq")
	}
	delete(d.vertices, txid)
}

// postDeleteEvents posts TxDeleted events outside the write lock to avoid
// holding the lock while the events pipeline (including WebSocket writes) drains.
func (d *MemDAG) postDeleteEvents(deletedIDs []base.TransactionID) {
	for _, txid := range deletedIDs {
		d.PostEventTxDeleted(txid)
	}
}

// doGC is the unified pruning loop. Traverses all vertices and applies three expiration
// criteria (any triggers detachment):
//
//  1. TTL: wall-clock TTL (only when synced) or ledger-time TTL (always active)
//  2. LRB-confirmed: vertex's slot is old enough AND vertex is NOT in any remaining
//     branchVertices set (was confirmed in a branch that has been cleaned up)
//  3. Orphaned: same check as 2 — vertex old enough and not tracked by any recent branch.
//     Catches both confirmed txs and orphaned seq txs that were never in any branch.
//
// Pattern: collect expired under global lock → ConvertToDetached outside lock → nullify under lock.
func (d *MemDAG) doGC() (detached, deleted int) {
	type expiredEntry struct {
		vid    *vertex.WrappedTx
		reason string // "wallclock_ttl", "ledger_ttl", "confirmed_or_orphan"
	}
	expired := make([]expiredEntry, 0)
	var deletedIDs []base.TransactionID
	synced := d.IsSynced()

	d.WithGlobalWriteLock(func() {
		slotNow := ledger.TimeNow().Slot
		latestBranch := d.latestBranchSlot
		healthySlot := d.latestHealthyBranchSlot

		for txid, rec := range d.vertices {
			if rec.Pointer.Value() == nil {
				d.LogTx(time.Now(), "GC: map entry DELETED (weak ptr nil)", txid)
				d.deleteFromMapNoLock(txid)
				deletedIDs = append(deletedIDs, txid)
				deleted++
				continue
			}
			if rec.WrappedTx == nil {
				// already detached, waiting for weak pointer to go nil
				continue
			}

			// criterion 1: TTL — wall-clock or ledger-time expiry
			wallClockExpired := synced && slotNow-rec.WrappedTx.SlotWhenAdded > vertexTTLSlots
			ledgerTimeExpired := latestBranch > 0 && txid.Slot()+vertexLedgerTTLSlots < latestBranch
			if wallClockExpired && !d.IsVertexReferencedBySequencer(rec.WrappedTx) {
				expired = append(expired, expiredEntry{rec.WrappedTx, "wallclock_ttl"})
				continue
			}
			if ledgerTimeExpired && !d.IsVertexReferencedBySequencer(rec.WrappedTx) {
				expired = append(expired, expiredEntry{rec.WrappedTx, "ledger_ttl"})
				continue
			}

			// criterion 2: confirmed deep — vertex is in a branch set that is branchPruneDepth
			// slots behind the LRB. Wall-clock age check protects forward-sync vertices.
			wallClockOldEnough := slotNow > branchPruneDepth && rec.WrappedTx.SlotWhenAdded+branchPruneDepth < slotNow
			if wallClockOldEnough && d.isConfirmedDeepNoLock(rec.WrappedTx, healthySlot) && !d.IsVertexReferencedBySequencer(rec.WrappedTx) {
				expired = append(expired, expiredEntry{rec.WrappedTx, "confirmed_deep"})
			}
		}

		// clean up branchVertices after pruning check (so deep sets are available for isConfirmedDeepNoLock)
		d.cleanupBranchVerticesNoLock()
	})
	d.postDeleteEvents(deletedIDs)
	deletedIDs = deletedIDs[:0]

	if len(expired) == 0 {
		return
	}
	for _, e := range expired {
		e.vid.ConvertToDetached()
	}
	d.WithGlobalWriteLock(func() {
		for _, e := range expired {
			txid := e.vid.ID()
			if rec, found := d.vertices[txid]; found {
				if rec.Value() == nil {
					d.LogTx(time.Now(), fmt.Sprintf("GC: DETACHED+DELETED reason=%s", e.reason), txid)
					d.deleteFromMapNoLock(txid)
					deletedIDs = append(deletedIDs, txid)
					deleted++
				} else {
					d.LogTx(time.Now(), fmt.Sprintf("GC: DETACHED reason=%s", e.reason), txid)
					if !txid.IsSequencerTransaction() && d.Counter("nonseq") > 0 {
						d.DecCounter("nonseq")
					}
					rec.WrappedTx = nil
					d.vertices[txid] = rec
					detached++
				}
			}
		}
	})
	d.postDeleteEvents(deletedIDs)
	return
}

// RequestPrune is a no-op retained for interface compatibility.
// All pruning is handled by the unified doGC loop on its regular period.
func (d *MemDAG) RequestPrune() {
}

// RegisterBranchVertices records the set of vertices confirmed in a branch's past cone.
// Called from the milestone attacher before the PastCone is discarded.
// When the branch falls behind the LRB by branchPruneDepth slots, all its vertices
// become prunable.
func (d *MemDAG) RegisterBranchVertices(branchID base.TransactionID, predecessorBranchID base.TransactionID, vertices set.Set[*vertex.WrappedTx]) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.branchVertices[branchID] = &branchVertexRecord{
		predecessorBranchID: predecessorBranchID,
		vertices:            vertices,
	}

	// if map grows too large, force cleanup
	if len(d.branchVertices) > maxBranchVertexRecords {
		d.cleanupBranchVerticesNoLock()
	}
}

// cleanupBranchVerticesNoLock removes deep branches and rootless forks from the
// branchVertices map. After cleanup, vertices not in any remaining set are candidates
// for criteria 2+3 in doGC (confirmed or orphaned).
// Caller must hold d.mutex.
func (d *MemDAG) cleanupBranchVerticesNoLock() {
	healthySlot := d.latestHealthyBranchSlot
	if healthySlot == 0 {
		return
	}

	// special case: stale LRB — clear everything
	nowSlot := ledger.TimeNow().Slot
	if nowSlot > staleLRBSlots && healthySlot < nowSlot-staleLRBSlots {
		for branchID, rec := range d.branchVertices {
			rec.vertices = nil // help GC
			delete(d.branchVertices, branchID)
		}
		return
	}

	// remove branches deep enough behind the LRB
	for branchID, rec := range d.branchVertices {
		if branchID.Slot()+branchPruneDepth <= healthySlot {
			rec.vertices = nil // help GC
			delete(d.branchVertices, branchID)
		}
	}

	// remove rootless fork branches: predecessor not in the map and old enough
	changed := true
	for changed {
		changed = false
		for branchID, rec := range d.branchVertices {
			predSlot := rec.predecessorBranchID.Slot()
			_, predInMap := d.branchVertices[rec.predecessorBranchID]
			if !predInMap && predSlot+branchPruneDepth <= healthySlot {
				rec.vertices = nil
				delete(d.branchVertices, branchID)
				changed = true
			}
		}
	}
}

// isInAnyBranchSetNoLock checks if a vertex is in any branchVertices set.
// Caller must hold d.mutex.
func (d *MemDAG) isInAnyBranchSetNoLock(vid *vertex.WrappedTx) bool {
	for _, rec := range d.branchVertices {
		if rec.vertices.Contains(vid) {
			return true
		}
	}
	return false
}

// isConfirmedDeepNoLock checks if a vertex is confirmed in a branch that is deep enough
// behind the LRB (branchPruneDepth slots). Returns true if the vertex should be pruned
// because it's confirmed and deep.
// Caller must hold d.mutex.
func (d *MemDAG) isConfirmedDeepNoLock(vid *vertex.WrappedTx, healthySlot uint32) bool {
	for branchID, rec := range d.branchVertices {
		if branchID.Slot()+branchPruneDepth <= healthySlot && rec.vertices.Contains(vid) {
			return true
		}
	}
	return false
}

func (d *MemDAG) GetStemWrappedOutput(branch base.TransactionID) (ret vertex.WrappedOutput) {
	if vid := d.GetVertex(branch); vid != nil {
		ret = vid.StemWrappedOutput()
	}
	return
}

func (d *MemDAG) HeaviestStateForLatestTimeSlotWithBaseline() (multistate.SugaredStateReader, *vertex.WrappedTx) {
	branchRecords := multistate.FetchLatestBranches(d.StateStore())
	util.Assertf(len(branchRecords) > 0, "len(branchRecords)>0")

	return multistate.MakeSugared(multistate.MustNewReadable(d.StateStore(), branchRecords[0].Root, 0)),
		d.GetVertex(branchRecords[0].TxID())
}

func (d *MemDAG) HeaviestStateForLatestTimeSlot() multistate.SugaredStateReader {
	rootRecords := multistate.FetchLatestRootRecords(d.StateStore())
	util.Assertf(len(rootRecords) > 0, "len(rootRecords)>0")

	return multistate.MakeSugared(multistate.MustNewReadable(d.StateStore(), rootRecords[0].Root, 0))
}

//func (d *MemDAG) CheckTransactionInLRB(txid base.TransactionID, maxDepth int) (lrbid base.TransactionID, foundAtDepth int) {
//	lrb, atDepth := multistate.CheckTransactionInLRB(d.StateStore(), txid, maxDepth, global.FractionHealthyBranch)
//	foundAtDepth = atDepth
//	if lrb != nil {
//		lrbid = lrb.Stem.ID.TransactionID()
//	}
//	return
//}

// WaitUntilTransactionInHeaviestState for testing mostly
func (d *MemDAG) WaitUntilTransactionInHeaviestState(txid base.TransactionID, timeout ...time.Duration) (*vertex.WrappedTx, error) {
	deadline := time.Now().Add(10 * time.Minute)
	if len(timeout) > 0 {
		deadline = time.Now().Add(timeout[0])
	}
	for {
		rdr, baseline := d.HeaviestStateForLatestTimeSlotWithBaseline()
		if rdr.KnowsCommittedTransaction(txid) {
			return baseline, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("WaitUntilTransactionInHeaviestState: timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// EvidenceBranchSlot maintains cached values of latest branch and healthy branch slots.
func (d *MemDAG) EvidenceBranchSlot(s uint32, isHealthy bool) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.latestBranchSlot < s {
		d.latestBranchSlot = s
	}
	if isHealthy {
		if s > d.latestHealthyBranchSlot {
			d.latestHealthyBranchSlot = s
		}
	}
}

// LatestBranchSlots return latest committed slots and the sync flag.
// The latter indicates if current node is in sync with the network.
// If network is unreachable or nobody else is active it will return false
// Node is out of sync if current slots are behind from now
// Being synced or not is subjective
func (d *MemDAG) LatestBranchSlots() (slot, healthySlot uint32, synced bool) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.latestBranchSlot == 0 {
		d.latestBranchSlot = multistate.FetchLatestCommittedSlot(d.StateStore())
		if d.latestBranchSlot == 0 {
			synced = true
		}
	}
	if d.latestHealthyBranchSlot == 0 {
		healthyExists := false
		d.latestHealthyBranchSlot, healthyExists = multistate.FindLatestHealthySlot(d.StateStore(), global.FractionHealthyBranch)
		util.Assertf(healthyExists, "assume healthy slot exists: FIX IT")
	}
	nowSlot := ledger.TimeNow().Slot
	// synced criterion. latest slot max 3 behind, latest healthy max 6 behind
	slot, healthySlot = d.latestBranchSlot, d.latestHealthyBranchSlot
	const (
		latestSlotBehindMax        = 2
		latestHealthySlotBehindMax = 6
	)
	synced = synced || (slot+latestSlotBehindMax > nowSlot && healthySlot+latestHealthySlotBehindMax > nowSlot)
	return
}

func (d *MemDAG) LatestHealthySlot() uint32 {
	_, ret, _ := d.LatestBranchSlots()
	return ret
}

func (d *MemDAG) ParseMilestoneData(msVID *vertex.WrappedTx) (ret *seqdata.SequencerData) {
	msVID.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			if r, err := ledger.ParseSequencerData(v.SequencerOutput().Output); err == nil {
				ret = &r
			}
		},
		DetachedVertex: func(v *vertex.DetachedVertex) {
			if r, err := ledger.ParseSequencerData(v.SequencerOutput().Output); err == nil {
				ret = &r
			}
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			seqOut, _ := v.SequencerOutputs()
			if r, err := ledger.ParseSequencerData(seqOut); err == nil {
				ret = &r
			}
		},
	})
	return
}

// Vertices to avoid global lock while traversing all utangle
func (d *MemDAG) Vertices() []*vertex.WrappedTx {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	ret := make([]*vertex.WrappedTx, 0, len(d.vertices))
	for _, weakp := range d.vertices {
		if strongP := weakp.Value(); strongP != nil {
			ret = append(ret, strongP)
		}
	}
	return ret
}

func (d *MemDAG) VerticesWithExpirationFlag() map[*vertex.WrappedTx]bool {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	ret := make(map[*vertex.WrappedTx]bool, len(d.vertices))
	for _, weakp := range d.vertices {
		if strongP := weakp.Value(); strongP != nil {
			ret[strongP] = weakp.WrappedTx == nil
		}
	}
	return ret
}

func (d *MemDAG) VerticesFiltered(filterByID func(txid base.TransactionID) bool) []*vertex.WrappedTx {
	return util.PurgeSlice(d.Vertices(), func(vid *vertex.WrappedTx) bool {
		return filterByID(vid.ID())
	})
}

func (d *MemDAG) VerticesDescending() []*vertex.WrappedTx {
	ret := d.Vertices()
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().After(ret[j].Timestamp())
	})
	return ret
}

// RecreateVertexMap to avoid memory leak
func (d *MemDAG) RecreateVertexMap() {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	m := d.vertices
	d.vertices = maps.Clone(d.vertices)
	clear(m)
}

func (d *MemDAG) registerMetrics() {
	d.numVerticesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_memDAG_numVerticesGauge",
		Help: "number of vertices in the memDAG",
	})
	d.pipelineGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_pipeline_size",
		Help: "total transactions in the pipeline (vertices + solicited queue + clock wait)",
	})
	d.MetricsRegistry().MustRegister(d.numVerticesGauge, d.pipelineGauge)
}
