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

		metrics
	}

	metrics struct {
		numVerticesGauge prometheus.Gauge
	}
)

func New(env environment) *MemDAG {
	ret := &MemDAG{
		environment: env,
		vertices:    make(map[base.TransactionID]_vertexRecord),
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

		ret.RepeatInBackground("memdag-stats", 10*time.Second, func() bool {
			nVertices := ret.NumVertices()
			env.Log().Infof("[memdag stats] vertices: %d", nVertices)
			ret.numVerticesGauge.Set(float64(nVertices))
			return true
		})
	}
	return ret
}

// vertexTTLSlots must be at least 6
const vertexTTLSlots = 24

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
	if !txid.IsSequencerTransaction() {
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
// -- collects those which are already expired and not referenced by other parts of the system (in different critical section)
// -- nullifies strong references of those expired thus preparing them for GC
func (d *MemDAG) doGC() (detached, deleted int) {
	expired := make([]*vertex.WrappedTx, 0)
	var deletedIDs []base.TransactionID

	if !d.IsSynced() {
		// if not synced, simplified scenario: just delete all GCed vertices
		d.WithGlobalWriteLock(func() {
			for txid, rec := range d.vertices {
				if rec.Pointer.Value() == nil {
					d.deleteFromMapNoLock(txid)
					deletedIDs = append(deletedIDs, txid)
					deleted++
				}
			}
		})
		d.postDeleteEvents(deletedIDs)
		return
	}
	// is synced.
	// collect those expired
	d.WithGlobalWriteLock(func() {
		slotNow := ledger.TimeNow().Slot
		for txid, rec := range d.vertices {
			if rec.Pointer.Value() == nil {
				d.deleteFromMapNoLock(txid)
				deletedIDs = append(deletedIDs, txid)
				deleted++
			} else {
				if rec.WrappedTx != nil && slotNow-rec.WrappedTx.SlotWhenAdded > vertexTTLSlots {
					expired = append(expired, rec.WrappedTx)
				}
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
					if !txid.IsSequencerTransaction() {
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

// EvidenceBranchSlot maintains cached values
func (d *MemDAG) EvidenceBranchSlot(s uint32, isHealthy bool) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.latestBranchSlot < s {
		d.latestBranchSlot = s
	}
	if isHealthy {
		if d.latestHealthyBranchSlot < s {
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
	d.MetricsRegistry().MustRegister(d.numVerticesGauge)
}
