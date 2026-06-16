package memdag

// Read-only memDAG introspection for the optional debug API (see node/debug_api.go).
// Built for the pin/leak investigation: census of the live set, filtered vertex
// queries, full vertex dump, and "who pins this vertex" reference search.
// Nothing here mutates state.

import (
	"sort"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type CensusResult struct {
	CurrentSlot    uint32         `json:"current_slot"`
	OldestSlot     uint32         `json:"oldest_slot_when_added"`
	Total          int            `json:"total"`           // alive objects tracked in the map
	Live           int            `json:"live"`            // strong-ref in map (active)
	DetachedInMap  int            `json:"detached_in_map"` // strong-ref nil but object alive = pinned elsewhere
	ByKind         map[string]int `json:"by_kind"`
	ByStatus       map[string]int `json:"by_status"`
	AddedHistogram map[uint32]int `json:"added_slot_histogram"` // SlotWhenAdded -> count
}

// VertexSummary is one row of a /vertices query.
type VertexSummary struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Status         string `json:"status"`
	AddedSlot      uint32 `json:"added_slot"`
	LedgerSlot     uint32 `json:"ledger_slot"`
	IsBranch       bool   `json:"is_branch"`
	IsSeq          bool   `json:"is_seq"`
	RefBySequencer bool   `json:"ref_by_sequencer"`
	DetachedInMap  bool   `json:"detached_in_map"`
	NumConsumers   int    `json:"num_consumers"`
	HasPastCone    bool   `json:"has_past_cone"`
}

// VertexFilter is the (lean) predicate set for QueryVertices. Nil pointers = unset.
type VertexFilter struct {
	AddedBefore   *uint32 // SlotWhenAdded < x
	AddedAfter    *uint32 // SlotWhenAdded > x
	AddedLagGt    *uint32 // currentSlot - SlotWhenAdded > x  (stale/pinned suspects)
	Kind          string  // "", vertex, detached, virtual
	Status        string  // "", good, bad, undefined
	IsBranch      *bool
	IsSequencer   *bool
	RefBySeq      *bool
	DetachedInMap *bool
	Sort          string // added_slot (default), ledger_slot, consumers
	Order         string // asc (default), desc
	Limit         int    // 0 = no limit
}

// Census aggregates the live set under one snapshot.
func (d *MemDAG) Census() CensusResult {
	flags := d.VerticesWithExpirationFlag() // vid -> detachedInMap
	res := CensusResult{
		CurrentSlot:    ledger.TimeNow().Slot,
		Total:          len(flags),
		ByKind:         map[string]int{},
		ByStatus:       map[string]int{},
		AddedHistogram: map[uint32]int{},
	}
	res.OldestSlot = res.CurrentSlot
	for vid, detached := range flags {
		if detached {
			res.DetachedInMap++
		} else {
			res.Live++
		}
		if vid.SlotWhenAdded < res.OldestSlot {
			res.OldestSlot = vid.SlotWhenAdded
		}
		res.AddedHistogram[vid.SlotWhenAdded]++
		res.ByKind[vid.KindString()]++
		res.ByStatus[vid.GetTxStatus().String()]++
	}
	return res
}

// QueryVertices returns summary rows matching the filter.
func (d *MemDAG) QueryVertices(f VertexFilter) []VertexSummary {
	flags := d.VerticesWithExpirationFlag()
	now := ledger.TimeNow().Slot
	ret := make([]VertexSummary, 0)
	for vid, detached := range flags {
		added := vid.SlotWhenAdded
		if f.AddedBefore != nil && !(added < *f.AddedBefore) {
			continue
		}
		if f.AddedAfter != nil && !(added > *f.AddedAfter) {
			continue
		}
		if f.AddedLagGt != nil && !(now > added && now-added > *f.AddedLagGt) {
			continue
		}
		if f.DetachedInMap != nil && *f.DetachedInMap != detached {
			continue
		}
		if f.IsBranch != nil && *f.IsBranch != vid.IsBranchTransaction() {
			continue
		}
		if f.IsSequencer != nil && *f.IsSequencer != vid.IsSequencerTransaction() {
			continue
		}
		kind := vid.KindString()
		if f.Kind != "" && f.Kind != kind {
			continue
		}
		status := vid.GetTxStatus().String()
		if f.Status != "" && f.Status != status {
			continue
		}
		refSeq := d.IsVertexReferencedBySequencer(vid)
		if f.RefBySeq != nil && *f.RefBySeq != refSeq {
			continue
		}
		nc, _ := vid.NumConsumers()
		ret = append(ret, VertexSummary{
			ID:             util.Ref(vid.ID()).StringHex(),
			Kind:           kind,
			Status:         status,
			AddedSlot:      added,
			LedgerSlot:     vid.Slot(),
			IsBranch:       vid.IsBranchTransaction(),
			IsSeq:          vid.IsSequencerTransaction(),
			RefBySequencer: refSeq,
			DetachedInMap:  detached,
			NumConsumers:   nc,
			HasPastCone:    vid.HasPastCone(),
		})
	}
	sortSummaries(ret, f.Sort, f.Order)
	if f.Limit > 0 && len(ret) > f.Limit {
		ret = ret[:f.Limit]
	}
	return ret
}

func sortSummaries(s []VertexSummary, by, order string) {
	less := func(i, j int) bool { return s[i].AddedSlot < s[j].AddedSlot }
	switch by {
	case "ledger_slot":
		less = func(i, j int) bool { return s[i].LedgerSlot < s[j].LedgerSlot }
	case "consumers":
		less = func(i, j int) bool { return s[i].NumConsumers < s[j].NumConsumers }
	}
	if order == "desc" {
		orig := less
		less = func(i, j int) bool { return orig(j, i) }
	}
	sort.SliceStable(s, less)
}

// DumpVertex returns the full debug dump of one vertex, if present in the memDAG.
func (d *MemDAG) DumpVertex(txid base.TransactionID) (vertex.VertexDump, bool) {
	vid := d.GetVertex(txid)
	if vid == nil {
		return vertex.VertexDump{}, false
	}
	return vid.DebugDump(), true
}

type HolderRef struct {
	ID  string `json:"id"`
	Via string `json:"via"` // input | consumed
}

type PinnersResult struct {
	Target           string      `json:"target"`
	Found            bool        `json:"found"`
	RefBySequencer   bool        `json:"ref_by_sequencer"`
	InBranchVertices []string    `json:"in_branch_vertices"` // branch ids whose past-cone set contains target
	InMemoryHolders  []HolderRef `json:"in_memory_holders"`  // vertices that reference target via input or consumed
}

// FindPinners reports what holds a strong reference to the target vertex:
// the sequencer, any branchVertices set, and any in-memory vertex that lists it
// as an input or in its consumed (forward) map. Naming the holder of the oldest
// pinned vertex identifies the leak's durable root.
func (d *MemDAG) FindPinners(txid base.TransactionID) PinnersResult {
	res := PinnersResult{Target: txid.StringHex(), InBranchVertices: []string{}, InMemoryHolders: []HolderRef{}}
	target := d.GetVertex(txid)
	if target == nil {
		return res
	}
	res.Found = true
	res.RefBySequencer = d.IsVertexReferencedBySequencer(target)

	// branchVertices membership (internal map, under the memDAG lock)
	d.mutex.RLock()
	for branchID, rec := range d.branchVertices {
		if rec.vertices.Contains(target) {
			res.InBranchVertices = append(res.InBranchVertices, branchID.StringHex())
		}
	}
	d.mutex.RUnlock()

	// in-memory holders: scan every live vertex for input or consumed references to target
	for _, vid := range d.Vertices() {
		if vid == target {
			continue
		}
		vid.RUnwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				for _, in := range v.Inputs {
					if in == target {
						res.InMemoryHolders = append(res.InMemoryHolders, HolderRef{ID: util.Ref(vid.ID()).StringHex(), Via: "input"})
						return
					}
				}
			},
		})
		// vid holds target in its consumed (forward) set
		if vid.HasConsumer(target) {
			res.InMemoryHolders = append(res.InMemoryHolders, HolderRef{ID: util.Ref(vid.ID()).StringHex(), Via: "consumed"})
		}
	}
	return res
}
