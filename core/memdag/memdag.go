package memdag

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/sema"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/exp/maps"
)

type (
	Environment interface {
		StateStore() global.StateStore
		MetricsRegistry() *prometheus.Registry
	}

	// MemDAG is a global map of all in-memory vertices of the transaction DAG
	MemDAG struct {
		Environment

		// cache of vertices. Key of the map is transaction ID. Value of the map is *vertex.WrappedTx.
		// The pointer value *vertex.WrappedTx is used as a unique identified of the transaction while being
		// loaded into the memory.
		// The vertices map may be seen as encoding table between transaction ID and
		// more economic (memory-wise) yet transient in-memory ID *vertex.WrappedTx
		// in most other data structure, such as attacher, transactions are represented as *vertex.WrappedTx
		// MemDAG is constantly garbage-collected by the pruner
		mutex    *sema.Sema
		vertices map[ledger.TransactionID]*vertex.WrappedTx
		// latestBranchSlot maintained by EvidenceBranchSlot
		latestBranchSlot        ledger.Slot
		latestHealthyBranchSlot ledger.Slot

		// cache of state readers. One state (trie) reader for the branch/root. When accessed through the cache,
		// reading is highly optimized because each state reader keeps its trie cache, so consequent calls to
		// HasUTXO, GetUTXO and similar does not require database involvement during attachment and solidification
		// in the same slot.
		// Inactive cached readers with their trie caches are constantly cleaned up by the pruner
		stateReadersMutex sync.RWMutex
		stateReaders      map[ledger.TransactionID]*cachedStateReader
	}

	cachedStateReader struct {
		global.IndexedStateReader
		rootRecord   *multistate.RootRecord
		lastActivity time.Time
	}
)

func New(env Environment) *MemDAG {
	return &MemDAG{
		Environment:  env,
		mutex:        sema.New(1 * time.Second),
		vertices:     make(map[ledger.TransactionID]*vertex.WrappedTx),
		stateReaders: make(map[ledger.TransactionID]*cachedStateReader),
	}
}

const sharedStateReaderCacheSize = 3000

func (d *MemDAG) WithGlobalWriteLock(fun func()) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	fun()
}

func (d *MemDAG) GetVertexNoLock(txid *ledger.TransactionID) *vertex.WrappedTx {
	return d.vertices[*txid]
}

func (d *MemDAG) GetVertex(txid *ledger.TransactionID) *vertex.WrappedTx {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.GetVertexNoLock(txid)
}

// NumVertices number of vertices
func (d *MemDAG) NumVertices() int {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return len(d.vertices)
}

func (d *MemDAG) NumStateReaders() int {
	d.stateReadersMutex.RLock()
	defer d.stateReadersMutex.RUnlock()

	return len(d.stateReaders)
}

func (d *MemDAG) AddVertexNoLock(vid *vertex.WrappedTx) {
	util.Assertf(d.GetVertexNoLock(&vid.ID) == nil, "d.GetVertexNoLock(vid.ID())==nil")
	d.vertices[vid.ID] = vid
}

// PurgeDeletedVertices with global lock
func (d *MemDAG) PurgeDeletedVertices(deleted []*vertex.WrappedTx) {
	d.WithGlobalWriteLock(func() {
		for _, vid := range deleted {
			delete(d.vertices, vid.ID)
		}
	})
}

func _stateReaderCacheTTL() time.Duration {
	return 3 * ledger.SlotDuration()
}

func (d *MemDAG) PurgeCachedStateReaders() (int, int) {
	toDelete := make([]ledger.TransactionID, 0)
	ttl := _stateReaderCacheTTL()

	d.stateReadersMutex.Lock()
	defer d.stateReadersMutex.Unlock()

	for txid, b := range d.stateReaders {
		if time.Since(b.lastActivity) > ttl {
			toDelete = append(toDelete, txid)
		}
	}
	for i := range toDelete {
		delete(d.stateReaders, toDelete[i])
	}
	return len(toDelete), len(d.stateReaders)
}

func (d *MemDAG) GetStateReaderForTheBranch(branch *ledger.TransactionID) global.IndexedStateReader {
	ret, _ := d.GetStateReaderForTheBranchExt(branch)
	return ret
}

func (d *MemDAG) GetStateReaderForTheBranchExt(branch *ledger.TransactionID) (global.IndexedStateReader, *multistate.RootRecord) {
	util.Assertf(branch != nil, "branch != nil")
	util.Assertf(branch.IsBranchTransaction(), "GetStateReaderForTheBranchExt: branch tx expected. Got: %s", branch.StringShort())

	d.stateReadersMutex.Lock()
	defer d.stateReadersMutex.Unlock()

	ret := d.stateReaders[*branch]
	if ret != nil {
		ret.lastActivity = time.Now()
		return ret.IndexedStateReader, ret.rootRecord
	}
	rootRecord, found := multistate.FetchRootRecord(d.StateStore(), *branch)
	if !found {
		return nil, nil
	}
	d.stateReaders[*branch] = &cachedStateReader{
		IndexedStateReader: multistate.MustNewReadable(d.StateStore(), rootRecord.Root, sharedStateReaderCacheSize),
		rootRecord:         &rootRecord,
		lastActivity:       time.Now(),
	}
	return d.stateReaders[*branch], &rootRecord
}

func (d *MemDAG) GetStemWrappedOutput(branch *ledger.TransactionID) (ret vertex.WrappedOutput) {
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

// WaitUntilTransactionInHeaviestState for testing mostly
func (d *MemDAG) WaitUntilTransactionInHeaviestState(txid ledger.TransactionID, timeout ...time.Duration) (*vertex.WrappedTx, error) {
	deadline := time.Now().Add(10 * time.Minute)
	if len(timeout) > 0 {
		deadline = time.Now().Add(timeout[0])
	}
	for {
		rdr, baseline := d.HeaviestStateForLatestTimeSlotWithBaseline()
		if rdr.KnowsCommittedTransaction(&txid) {
			return baseline, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("WaitUntilTransactionInHeaviestState: timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// EvidenceBranchSlot maintains cached values
func (d *MemDAG) EvidenceBranchSlot(s ledger.Slot, isHealthy bool) {
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
func (d *MemDAG) LatestBranchSlots() (slot, healthySlot ledger.Slot, synced bool) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	if d.latestBranchSlot == 0 {
		d.latestBranchSlot = multistate.FetchLatestCommittedSlot(d.StateStore())
		if d.latestBranchSlot == 0 {
			synced = true
		}
	}
	if d.latestHealthyBranchSlot == 0 {
		// TODO take into account
		healthyExists := false
		d.latestHealthyBranchSlot, healthyExists = multistate.FindLatestHealthySlot(d.StateStore(), global.FractionHealthyBranch)
		util.Assertf(healthyExists, "assume healthy slot exists: FIX IT")
	}
	nowSlot := ledger.TimeNow().Slot()
	// synced criterion. latest slot max 3 behind, latest healthy max 6 behind
	slot, healthySlot = d.latestBranchSlot, d.latestHealthyBranchSlot
	const (
		latestSlotBehindMax        = 2
		latestHealthySlotBehindMax = 6
	)
	synced = synced || (slot+latestSlotBehindMax > nowSlot && healthySlot+latestHealthySlotBehindMax > nowSlot)
	return
}

func (d *MemDAG) LatestHealthySlot() ledger.Slot {
	_, ret, _ := d.LatestBranchSlots()
	return ret
}

func (d *MemDAG) ParseMilestoneData(msVID *vertex.WrappedTx) (ret *ledger.MilestoneData) {
	msVID.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			ret = ledger.ParseMilestoneData(v.Tx.SequencerOutput().Output)
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			seqOut, _ := v.SequencerOutputs()
			ret = ledger.ParseMilestoneData(seqOut)
		},
	})
	return
}

// Vertices to avoid global lock while traversing all utangle
func (d *MemDAG) Vertices(filterByID ...func(txid *ledger.TransactionID) bool) []*vertex.WrappedTx {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	if len(filterByID) == 0 {
		return maps.Values(d.vertices)
	}
	return util.ValuesFiltered(d.vertices, func(vid *vertex.WrappedTx) bool {
		return filterByID[0](&vid.ID)
	})
}

func (d *MemDAG) VerticesDescending() []*vertex.WrappedTx {
	ret := d.Vertices()
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().After(ret[j].Timestamp())
	})
	return ret
}

func (d *MemDAG) TipBranchHasTransaction(branchID, txid *ledger.TransactionID) bool {
	if rdr, _ := d.GetStateReaderForTheBranchExt(branchID); !util.IsNil(rdr) {
		return rdr.KnowsCommittedTransaction(txid)
	}
	return false
}
