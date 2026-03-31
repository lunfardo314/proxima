package memdag

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
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

		// pruneNeeded is an atomic flag set by external callers (branch commit, memory pressure)
		// to request LRB-depth pruning on the next tick of the prune loop.
		pruneNeeded atomic.Bool

		// per-branch vertex tracking for fine-grained pruning.
		// branchVertices maps branch ID to its past cone vertex set + predecessor link.
		// prunable is the global set of vertices eligible for removal (accumulated from
		// branches that fell behind the LRB by branchPruneDepth slots).
		// Both protected by mutex (same as vertices map).
		branchVertices map[base.TransactionID]*branchVertexRecord
		prunable       set.Set[*vertex.WrappedTx]

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
		prunable:       set.New[*vertex.WrappedTx](),
	}
	if env != nil {
		ret.registerMetrics()
		if env.DisableMemDAGGC() {
			env.Log().Infof("[memdag cleanup] DISABLED")
		} else {
			ret.RepeatInBackground("memdag-GC", 5*time.Second, func() bool {
				nDetached, nDeleted := ret.doGC()
				if nDetached > 0 || nDeleted > 0 {
					env.Log().Infof("[memdag GC] detached: %d, deleted: %d", nDetached, nDeleted)
				}
				return true
			}, true)
		}

		// LRB-depth prune loop: checks atomic flag every 1 second.
		// External callers (branch commit, memory pressure) set the flag via RequestPrune().
		ret.RepeatInBackground("memdag-prune", pruneLoopPeriod, func() bool {
			if ret.pruneNeeded.CompareAndSwap(true, false) {
				nDetached, nDeleted := ret.doLRBDepthPrune()
				if nDetached > 0 || nDeleted > 0 {
					env.Log().Infof("[memdag prune] LRB-depth: detached: %d, deleted: %d, stress: %d%%",
						nDetached, nDeleted, env.MemoryStressLevel())
				}
			}
			return true
		})

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
	vertexLedgerTTLSlots = 48

	// pruneLoopPeriod: how often the prune loop checks the pruneNeeded flag.
	pruneLoopPeriod = 1 * time.Second

	// branchPruneDepth: branches this many slots behind the LRB have their vertices
	// moved to the prunable set. All vertices confirmed in those branches become eligible
	// for removal from the memDAG.
	branchPruneDepth uint32 = 2
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

// doGC traverses all known transaction IDs and:
// -- deletes those with weak pointers GC-ed
// -- collects those which are expired by wall-clock TTL or ledger-time TTL
// -- nullifies strong references of those expired thus preparing them for GC
//
// Expiration criteria (either triggers eviction):
//   - wall-clock: vertex was added more than vertexTTLSlots wall-clock slots ago (only when synced)
//   - ledger-time: vertex's transaction slot is more than vertexLedgerTTLSlots behind the latest
//     committed branch (always active — handles forward-sync where vertices are "fresh" by wall clock
//     but ancient by ledger time)
func (d *MemDAG) doGC() (detached, deleted int) {
	expired := make([]*vertex.WrappedTx, 0)
	var deletedIDs []base.TransactionID
	synced := d.IsSynced()

	d.WithGlobalWriteLock(func() {
		slotNow := ledger.TimeNow().Slot
		latestBranch := d.latestBranchSlot

		for txid, rec := range d.vertices {
			if rec.Pointer.Value() == nil {
				d.deleteFromMapNoLock(txid)
				deletedIDs = append(deletedIDs, txid)
				deleted++
				continue
			}
			if rec.WrappedTx == nil {
				continue
			}
			// wall-clock TTL (only when synced)
			wallClockExpired := synced && slotNow-rec.WrappedTx.SlotWhenAdded > vertexTTLSlots
			// ledger-time TTL (always active)
			ledgerTimeExpired := latestBranch > 0 && txid.Slot()+vertexLedgerTTLSlots < latestBranch
			if wallClockExpired || ledgerTimeExpired {
				expired = append(expired, rec.WrappedTx)
			}
		}
	})
	d.postDeleteEvents(deletedIDs)
	deletedIDs = deletedIDs[:0]

	if len(expired) == 0 {
		return
	}
	for _, vid := range expired {
		vid.ConvertToDetached()
	}
	d.WithGlobalWriteLock(func() {
		for _, vid := range expired {
			txid := vid.ID()
			if rec, found := d.vertices[txid]; found {
				if rec.Value() == nil {
					d.deleteFromMapNoLock(txid)
					deletedIDs = append(deletedIDs, txid)
					deleted++
				} else {
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

// RequestPrune sets the atomic flag to trigger pruning on the next tick.
// Called by branch commit, branch disposal, memory pressure handlers, etc.
func (d *MemDAG) RequestPrune() {
	d.pruneNeeded.Store(true)
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
		d.updatePrunableSetNoLock()
	}
}

// updatePrunableSetNoLock scans branchVertices for branches that are deep enough behind
// the LRB and moves their vertices into the prunable set. Also removes rootless forks.
// Caller must hold d.mutex.
func (d *MemDAG) updatePrunableSetNoLock() {
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
		d.prunable = set.New[*vertex.WrappedTx]()
		return
	}

	// find branches deep enough behind the LRB
	var toRemove []base.TransactionID
	for branchID, rec := range d.branchVertices {
		if branchID.Slot()+branchPruneDepth <= healthySlot {
			// move vertices to prunable set
			rec.vertices.ForEach(func(vid *vertex.WrappedTx) bool {
				d.prunable.Insert(vid)
				return true
			})
			rec.vertices = nil // help GC
			toRemove = append(toRemove, branchID)
		}
	}
	for _, branchID := range toRemove {
		delete(d.branchVertices, branchID)
	}

	// remove rootless fork branches: branches whose predecessor is not in the map
	// and is not the LRB or newer
	changed := true
	for changed {
		changed = false
		for branchID, rec := range d.branchVertices {
			predSlot := rec.predecessorBranchID.Slot()
			_, predInMap := d.branchVertices[rec.predecessorBranchID]
			if !predInMap && predSlot+branchPruneDepth <= healthySlot {
				// predecessor was already pruned — this is an orphaned fork
				rec.vertices.ForEach(func(vid *vertex.WrappedTx) bool {
					d.prunable.Insert(vid)
					return true
				})
				rec.vertices = nil
				delete(d.branchVertices, branchID)
				changed = true
			}
		}
	}
}

// doLRBDepthPrune detaches and removes prunable vertices from the memDAG.
// Vertices are prunable if they are in the prunable set (confirmed in branches deep behind LRB).
//
// Follows the same pattern as doGC:
// 1. Collect prunable vertices under global write lock
// 2. Call ConvertToDetached outside the lock (breaks reference graph)
// 3. Nullify strong refs under global write lock
func (d *MemDAG) doLRBDepthPrune() (detached, deleted int) {
	var toDetach []*vertex.WrappedTx
	var deletedIDs []base.TransactionID

	d.WithGlobalWriteLock(func() {
		d.updatePrunableSetNoLock()

		for txid, rec := range d.vertices {
			if rec.Pointer.Value() == nil {
				d.deleteFromMapNoLock(txid)
				deletedIDs = append(deletedIDs, txid)
				deleted++
				continue
			}
			if rec.WrappedTx == nil {
				continue
			}
			if d.prunable.Contains(rec.WrappedTx) {
				toDetach = append(toDetach, rec.WrappedTx)
				d.prunable.Remove(rec.WrappedTx)
			}
		}
	})
	d.postDeleteEvents(deletedIDs)
	deletedIDs = deletedIDs[:0]

	if len(toDetach) == 0 {
		return
	}

	// ConvertToDetached outside global lock — breaks the reference graph
	// (clears Inputs/Endorsements) so weak pointers can eventually go nil
	for _, vid := range toDetach {
		vid.ConvertToDetached()
	}

	// nullify strong refs
	d.WithGlobalWriteLock(func() {
		for _, vid := range toDetach {
			txid := vid.ID()
			if rec, found := d.vertices[txid]; found {
				if rec.Value() == nil {
					d.deleteFromMapNoLock(txid)
					deletedIDs = append(deletedIDs, txid)
					deleted++
				} else {
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

// EvidenceBranchSlot maintains cached values and triggers prunable set update when LRB advances.
func (d *MemDAG) EvidenceBranchSlot(s uint32, isHealthy bool) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.latestBranchSlot < s {
		d.latestBranchSlot = s
	}
	if isHealthy {
		prevHealthy := d.latestHealthyBranchSlot
		if s > prevHealthy {
			d.latestHealthyBranchSlot = s
			// LRB advanced — update the prunable set and signal the pruner
			d.updatePrunableSetNoLock()
			d.pruneNeeded.Store(true)
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
