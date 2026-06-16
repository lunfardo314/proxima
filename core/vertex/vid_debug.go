package vertex

import "strconv"

// Read-only debug introspection of a WrappedTx. Used by the optional memDAG debug
// API (see node/debug_api.go). Nothing here mutates state; it only snapshots fields
// under the existing locks for diagnostics (leak/pin investigation).

// VertexDump is a serializable snapshot of a WrappedTx and its wrapped payload.
type VertexDump struct {
	ID               string              `json:"id"`
	IDShort          string              `json:"id_short"`
	Kind             string              `json:"kind"` // vertex | detached | virtual
	Status           string              `json:"status"`
	Flags            string              `json:"flags"`
	IsBranch         bool                `json:"is_branch"`
	IsSequencer      bool                `json:"is_sequencer"`
	LedgerSlot       uint32              `json:"ledger_slot"`
	SlotWhenAdded    uint32              `json:"slot_when_added"`
	AttachmentDepth  int                 `json:"attachment_depth"`
	Coverage         *uint64             `json:"coverage,omitempty"`
	Err              string              `json:"err,omitempty"`
	HasPastCone      bool                `json:"has_past_cone"`
	PastConeBaseline string              `json:"past_cone_baseline,omitempty"`
	PastConeSize     int                 `json:"past_cone_size,omitempty"`
	NumConsumers     int                 `json:"num_consumers"`
	Consumed         map[string][]string `json:"consumed,omitempty"` // output index -> consumer txids (hex)
	Inputs           []string            `json:"inputs,omitempty"`
	Endorsements     []string            `json:"endorsements,omitempty"`
	Baseline         string              `json:"baseline,omitempty"`
}

// flagsString renders the vertex flags compactly, e.g. "DV-S-".
func flagsString(f Flags) string {
	out := []byte("-----")
	if f.FlagsUp(FlagVertexDefined) {
		out[0] = 'D'
	}
	if f.FlagsUp(FlagVertexConstraintsValid) {
		out[1] = 'V'
	}
	if f.FlagsUp(FlagVertexTxAttachmentStarted) {
		out[2] = 'S'
	}
	if f.FlagsUp(FlagVertexTxAttachmentFinished) {
		out[3] = 'F'
	}
	if f.FlagsUp(FlagVertexIgnoreAbsenceOfPastCone) {
		out[4] = 'I'
	}
	return string(out)
}

// HasPastCone reports whether the vertex still retains a past cone (debug).
func (vid *WrappedTx) HasPastCone() bool {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()
	return vid.pastCone != nil
}

// HasConsumer reports whether c is in this vertex's consumed (forward) set, i.e.
// this vertex holds a strong reference to c. Used by FindPinners (debug).
func (vid *WrappedTx) HasConsumer(c *WrappedTx) bool {
	vid.mutexDescendants.RLock()
	defer vid.mutexDescendants.RUnlock()
	for _, cs := range vid.consumed {
		if cs.Contains(c) {
			return true
		}
	}
	return false
}

// KindString returns "vertex" | "detached" | "virtual".
func (vid *WrappedTx) KindString() string {
	ret := "virtual"
	vid.RUnwrap(UnwrapOptions{
		Vertex:         func(_ *Vertex) { ret = "vertex" },
		DetachedVertex: func(_ *DetachedVertex) { ret = "detached" },
	})
	return ret
}

// DebugDump snapshots the vertex for the debug API. Read-only.
func (vid *WrappedTx) DebugDump() VertexDump {
	d := VertexDump{
		ID:            vid.id.StringHex(),
		IDShort:       vid.IDShortString(),
		Status:        vid.GetTxStatus().String(),
		IsBranch:      vid.IsBranchTransaction(),
		IsSequencer:   vid.IsSequencerTransaction(),
		LedgerSlot:    vid.Slot(),
		SlotWhenAdded: vid.SlotWhenAdded,
	}
	if cp := vid.GetLedgerCoverageP(); cp != nil {
		c := *cp
		d.Coverage = &c
	}
	// payload + mutex-protected fields, snapshotted under the read lock
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			d.Kind = "vertex"
			d.Flags = flagsString(vid.flags)
			d.AttachmentDepth = vid.attachmentDepth
			if vid.err != nil {
				d.Err = vid.err.Error()
			}
			if vid.pastCone != nil {
				d.HasPastCone = true
				d.PastConeSize = len(vid.pastCone.vertices)
				if vid.pastCone.baselineBranchID != nil {
					d.PastConeBaseline = vid.pastCone.baselineBranchID.StringHex()
				}
			}
			for _, in := range v.Inputs {
				if in != nil {
					d.Inputs = append(d.Inputs, in.id.StringHex())
				}
			}
			for _, e := range v.Endorsements {
				if e != nil {
					d.Endorsements = append(d.Endorsements, e.id.StringHex())
				}
			}
			if v.BaselineBranchID != nil {
				d.Baseline = v.BaselineBranchID.StringHex()
			}
		},
		DetachedVertex: func(v *DetachedVertex) {
			d.Kind = "detached"
			d.Flags = flagsString(vid.flags)
			if vid.err != nil {
				d.Err = vid.err.Error()
			}
			if v.BranchID != nil {
				d.Baseline = v.BranchID.StringHex()
			}
		},
		VirtualTx: func(_ *VirtualTransaction) {
			d.Kind = "virtual"
			d.Flags = flagsString(vid.flags)
		},
	})
	// consumed (forward) edges, under their own lock
	consumed := map[string][]string{}
	vid.mutexDescendants.RLock()
	for idx, cs := range vid.consumed {
		ids := make([]string, 0, len(cs))
		for c := range cs {
			ids = append(ids, c.id.StringHex())
		}
		consumed[strconv.Itoa(int(idx))] = ids
	}
	vid.mutexDescendants.RUnlock()
	if len(consumed) > 0 {
		d.Consumed = consumed
	}
	nc, _ := vid.NumConsumers()
	d.NumConsumers = nc
	return d
}
