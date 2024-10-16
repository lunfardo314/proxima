package vertex

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

// Attacher keeps list of past cone vertices.
// The vertices of consideration are all Vertices in the past cone back to the 'Rooted' ones, i.e. those which belong
// to the baseline state.
// each vertex in the attacher has local flags, which defines its status in the scope of the attacher
// The goal of the attacher is to make all vertices marked as 'defined', i.e. either 'Rooted' or with its past cone checked
// and valid
// Flags (except 'asked for poke') become final and immutable after they are set 'ON'

// MarkVertexDefinedDoNotEnforceRootedCheck marks 'defined' without enforcing rooting has been checked

type (
	FlagsPastCone byte

	PastCone struct {
		global.Logging
		name string

		*PastConeBase
		delta *PastConeBase
	}

	PastConeBase struct {
		baseline *WrappedTx
		Vertices map[*WrappedTx]FlagsPastCone // byte is used by attacher for flags
		Rooted   map[*WrappedTx]set.Set[byte]
	}
)

const (
	FlagAttachedVertexKnown             = FlagsPastCone(0b00000001) // each vertex of consideration has this flag on
	FlagAttachedVertexDefined           = FlagsPastCone(0b00000010) // means vertex is 'defined', i.e. its validity is checked
	FlagAttachedVertexCheckedIfRooted   = FlagsPastCone(0b00000100) // means vertex has been checked if it is Rooted (it may or may not be Rooted)
	FlagAttachedVertexEndorsementsSolid = FlagsPastCone(0b00001000) // means all endorsements were validated
	FlagAttachedVertexInputsSolid       = FlagsPastCone(0b00010000) // means all consumed inputs are checked and valid
	FlagAttachedVertexAskedForPoke      = FlagsPastCone(0b00100000) //
)

func newPastConeBase(baseline *WrappedTx) *PastConeBase {
	return &PastConeBase{
		Vertices: make(map[*WrappedTx]FlagsPastCone),
		Rooted:   make(map[*WrappedTx]set.Set[byte]),
		baseline: baseline,
	}
}

func NewPastCone(env global.Logging, name string) *PastCone {
	return &PastCone{
		Logging:      env,
		name:         name,
		PastConeBase: newPastConeBase(nil),
	}
}

func (pb *PastConeBase) referenceBaseline(vid *WrappedTx) bool {
	util.Assertf(pb.baseline == nil, "pc.baseline == nil")
	if !vid.Reference() {
		return false
	}
	pb.baseline = vid
	return true
}

func (pc *PastCone) ReferenceBaseline(vid *WrappedTx) bool {
	if pc.delta == nil {
		return pc.referenceBaseline(vid)
	}
	return pc.delta.referenceBaseline(vid)
}

func (pc *PastCone) UnReferenceAll() {
	pc.RollbackDelta()
	if pc.baseline != nil {
		pc.baseline.UnReference()
	}
	for vid := range pc.Vertices {
		vid.UnReference()
	}
}

func (pc *PastCone) BeginDelta() {
	util.Assertf(pc.delta == nil, "pc.delta == nil")
	pc.delta = newPastConeBase(pc.baseline)
}

func (pc *PastCone) CommitDelta() {
	util.Assertf(pc.delta != nil, "pc.delta != nil")
	util.Assertf(pc.baseline == nil || pc.baseline == pc.delta.baseline, "pc.baseline==nil || pc.baseline == pc.delta.baseline")

	pc.baseline = pc.delta.baseline
	for vid, flags := range pc.delta.Vertices {
		pc.Vertices[vid] = flags
	}
	for vid, consumed := range pc.delta.Rooted {
		pc.Rooted[vid] = consumed
	}
	pc.delta = nil
}

func (pc *PastCone) RollbackDelta() {
	util.Assertf(pc.delta != nil, "pc.delta != nil")
	for vid := range pc.delta.Vertices {
		vid.UnReference()
	}
	if pc.delta.baseline != nil && pc.baseline == nil {
		pc.delta.baseline.UnReference()
	}
	pc.delta = nil
}

func (f FlagsPastCone) FlagsUp(fl FlagsPastCone) bool {
	return f&fl == fl
}

func (f FlagsPastCone) String() string {
	return fmt.Sprintf("%08b known: %v, defined: %v, checkedRooted: %v, endorsementsOk: %v, inputsOk: %v, asked for poke: %v",
		f,
		f.FlagsUp(FlagAttachedVertexKnown),
		f.FlagsUp(FlagAttachedVertexDefined),
		f.FlagsUp(FlagAttachedVertexCheckedIfRooted),
		f.FlagsUp(FlagAttachedVertexEndorsementsSolid),
		f.FlagsUp(FlagAttachedVertexInputsSolid),
		f.FlagsUp(FlagAttachedVertexAskedForPoke),
	)
}

func (pc *PastCone) Flags(vid *WrappedTx) FlagsPastCone {
	if pc.delta == nil {
		return pc.Vertices[vid]
	}
	if f, ok := pc.delta.Vertices[vid]; ok {
		return f
	}
	return pc.Vertices[vid]
}

func (pc *PastCone) SetFlagsUp(vid *WrappedTx, f FlagsPastCone) {
	flags := pc.Flags(vid) | f
	if pc.delta == nil {
		pc.Vertices[vid] = flags
	} else {
		pc.delta.Vertices[vid] = flags
	}
	pc.Assertf(flags.FlagsUp(FlagAttachedVertexKnown) && !flags.FlagsUp(FlagAttachedVertexDefined), "flags.FlagsUp(FlagKnown) && !flags.FlagsUp(FlagDefined)")
}

func (pc *PastCone) IsKnown(vid *WrappedTx) bool {
	return pc.Flags(vid).FlagsUp(FlagAttachedVertexKnown)
}

func (pc *PastCone) IsKnownDefined(vid *WrappedTx) bool {
	return pc.Flags(vid).FlagsUp(FlagAttachedVertexKnown | FlagAttachedVertexDefined)
}

func (pc *PastCone) IsKnownUndefined(vid *WrappedTx) bool {
	f := pc.Flags(vid)
	if !f.FlagsUp(FlagAttachedVertexKnown) {
		return false
	}
	return !f.FlagsUp(FlagAttachedVertexDefined)
}

func (pc *PastCone) isRootedVertex(vid *WrappedTx) (rooted bool, rootedIndices set.Set[byte]) {
	if pc.delta == nil {
		rootedIndices, rooted = pc.Rooted[vid]
		return
	}
	if rootedIndices, rooted = pc.delta.Rooted[vid]; rooted {
		return true, rootedIndices
	}
	rootedIndices, rooted = pc.Rooted[vid]
	return rooted, rootedIndices
}

func (pc *PastCone) IsRootedOutput(wOut WrappedOutput) bool {
	rootedVertex, rootedIndices := pc.isRootedVertex(wOut.VID)
	if !rootedVertex || len(rootedIndices) == 0 {
		return false
	}
	pc.Assertf(!pc.IsKnownNotRooted(wOut.VID), "!a.IsKnownNotRooted(wOut.VID)")
	return rootedIndices.Contains(wOut.Index)
}

// IsKnownNotRooted is definitely known it is not Rooted
func (pc *PastCone) IsKnownNotRooted(vid *WrappedTx) bool {
	if !pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedIfRooted) {
		// not checked yet
		return false
	}
	// it was checked already
	known := pc.IsKnownDefined(vid) || pc.IsKnownUndefined(vid)
	rooted, _ := pc.isRootedVertex(vid)
	return known && !rooted
}

func (pc *PastCone) IsKnownRooted(vid *WrappedTx) (rooted bool) {
	if !pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedIfRooted) {
		// not checked yet
		return false
	}
	rooted, _ = pc.isRootedVertex(vid)
	pc.Assertf(!rooted || pc.IsKnownDefined(vid) || pc.IsKnownUndefined(vid), "!rooted || pc.IsKnownDefined(vid) || pc.IsKnownUndefined(vid)")
	return
}

// MustMarkVertexRooted vertex becomes 'known' and marked Rooted and 'defined'
func (pc *PastCone) MustMarkVertexRooted(vid *WrappedTx) {
	if !pc.IsKnown(vid) {
		vid.MustReference()
	}
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown|FlagAttachedVertexCheckedIfRooted|FlagAttachedVertexDefined)
	// creates entry, probably empty, i.e. with or without output indices
	if pc.delta == nil {
		pc.Rooted[vid] = pc.Rooted[vid]
	} else {
		if _, ok := pc.Rooted[vid]; !ok {
			pc.delta.Rooted[vid] = pc.delta.Rooted[vid]
		}
	}
	pc.Assertf(pc.IsKnownRooted(vid), "pc.IsKnownNotRooted(vid)")
}

// MustMarkVertexNotRooted is marked definitely not Rooted
func (pc *PastCone) MustMarkVertexNotRooted(vid *WrappedTx) {
	if !pc.IsKnown(vid) {
		vid.MustReference()
	}
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown|FlagAttachedVertexCheckedIfRooted)
	pc.Assertf(pc.IsKnownNotRooted(vid), "pc.IsKnownNotRooted(vid)")
}

func (pc *PastCone) MarkVertexDefinedDoNotEnforceRootedCheck(vid *WrappedTx) {
	flags := pc.Flags(vid)
	if pc.IsKnownRooted(vid) {
		pc.Assertf(!flags.FlagsUp(FlagAttachedVertexInputsSolid), "!flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
		pc.Assertf(!flags.FlagsUp(FlagAttachedVertexEndorsementsSolid), "!flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
	}
	if pc.IsKnownNotRooted(vid) {
		pc.Assertf(flags.FlagsUp(FlagAttachedVertexInputsSolid), "flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
		pc.Assertf(flags.FlagsUp(FlagAttachedVertexEndorsementsSolid), "flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
	}
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown|FlagAttachedVertexDefined)
}

// MarkVertexDefined marks 'defined' and enforces rooting has been checked
func (pc *PastCone) MarkVertexDefined(vid *WrappedTx) {
	pc.Assertf(pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedIfRooted), "flags.FlagsUp(FlagAttachedVertexCheckedIfRooted): %s", vid.IDShortString)
	pc.MarkVertexDefinedDoNotEnforceRootedCheck(vid)
}

// MarkVertexUndefined vertex becomes 'known' but undefined
func (pc *PastCone) MarkVertexUndefined(vid *WrappedTx) bool {
	pc.Assertf(!pc.IsKnownDefined(vid), "!pc.IsKnownDefined(vid)")
	f := pc.Flags(vid)
	pc.Assertf(!f.FlagsUp(FlagAttachedVertexDefined), "!f.FlagsUp(FlagDefined)")
	if !pc.IsKnown(vid) {
		if !vid.Reference() {
			return false
		}
	}
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown)
	return true
}

func (pc *PastCone) ContainsUndefinedExcept(except *WrappedTx) bool {
	util.Assertf(pc.delta == nil, "pc.delta==nil")
	for vid, flags := range pc.Vertices {
		if !flags.FlagsUp(FlagAttachedVertexDefined) && vid != except {
			return true
		}
	}
	return false
}

func (pc *PastCone) CheckPastCone(rootVid *WrappedTx) (err error) {
	if pc.ContainsUndefinedExcept(rootVid) {
		return fmt.Errorf("still contains undefined Vertices")
	}

	// should be at least one Rooted output ( ledger baselineCoverage must be > 0)
	if len(pc.Rooted) == 0 {
		return fmt.Errorf("at least one Rooted output is expected")
	}
	for vid := range pc.Rooted {
		if !pc.IsKnownDefined(vid) {
			return fmt.Errorf("all Rooted must be defined. This one is not: %s", vid.IDShortString())
		}
	}
	if len(pc.Vertices) == 0 {
		return fmt.Errorf("'vertices' is empty")
	}
	sumRooted := uint64(0)
	for vid, consumed := range pc.Rooted {
		var o *ledger.Output
		consumed.ForEach(func(idx byte) bool {
			o, err = vid.OutputAt(idx)
			if err != nil {
				return false
			}
			sumRooted += o.Amount()
			return true
		})
	}
	if err != nil {
		return
	}
	if sumRooted == 0 {
		err = fmt.Errorf("sum of Rooted cannot be 0")
		return
	}
	for vid, flags := range pc.Vertices {
		if !flags.FlagsUp(FlagAttachedVertexKnown) {
			return fmt.Errorf("wrong flags 1 %08b in %s", flags, vid.IDShortString())
		}
		if !flags.FlagsUp(FlagAttachedVertexDefined) && vid != rootVid {
			return fmt.Errorf("wrong flags 2 %08b in %s", flags, vid.IDShortString())
		}
		if vid == rootVid {
			continue
		}
		status := vid.GetTxStatus()
		if status == Bad {
			return fmt.Errorf("BAD vertex in the past cone: %s", vid.IDShortString())
		}
		// transaction can be undefined in the past cone (virtual, non-sequencer etc)

		if pc.IsKnownRooted(vid) {
			// do not check dependencies if transaction is Rooted
			continue
		}
		vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			missingInputs, missingEndorsements := v.NumMissingInputs()
			if missingInputs+missingEndorsements > 0 {
				err = fmt.Errorf("not all dependencies solid in %s\n      missing inputs: %d\n      missing endorsements: %d,\n      missing input txs: [%s]",
					vid.IDShortString(), missingInputs, missingEndorsements, v.MissingInputTxIDString())
			}
		}})
		if err != nil {
			return
		}
	}
	return nil
}

func (pc *PastCone) UndefinedList() []*WrappedTx {
	ret := make([]*WrappedTx, 0)
	for vid, flags := range pc.Vertices {
		if !flags.FlagsUp(FlagAttachedVertexDefined) {
			ret = append(ret, vid)
		}
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (pc *PastCone) UndefinedListLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for _, vid := range pc.UndefinedList() {
		ret.Add(vid.IDVeryShort())
	}
	return ret
}

//// Reference references transaction and ensures it is referenced once or none
//func (pc *PastCone) Reference(vid *WrappedTx) bool {
//	if pc.referenced.committed.Contains(vid) {
//		return true
//	}
//	if pc.referenced.delta != nil && pc.referenced.delta.Contains(vid) {
//		return true
//	}
//	if !vid.Reference() {
//		// failed to reference
//		return false
//	}
//	if pc.referenced.delta != nil {
//		// Delta buffer is open
//		pc.referenced.delta.Insert(vid)
//	} else {
//		pc.referenced.committed.Insert(vid)
//	}
//	return true
//}
//
//func (pc *PastCone) MustReference(vid *WrappedTx) {
//	util.Assertf(pc.Reference(vid), "pc.Reference(vid)")
//}
