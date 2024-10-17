package vertex

import (
	"fmt"
	"sort"
	"strconv"

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
		delta      *PastConeBase
		refCounter int
		traceLines *lines.Lines
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
		traceLines:   lines.NewDummy(),
		//traceLines: lines.New("     "),
	}
}

func (pb *PastConeBase) referenceBaseline(vid *WrappedTx) bool {
	util.Assertf(pb.baseline == nil, "referenceBaseline: pc.baseline == nil")
	if !vid.Reference() {
		return false
	}
	pb.baseline = vid
	return true
}

func (pc *PastCone) ReferenceBaseline(vid *WrappedTx) (ret bool) {
	util.Assertf(pc.baseline == nil, "ReferenceBaseline: pc.baseline == nil")
	if pc.delta == nil {
		ret = pc.referenceBaseline(vid)
	} else {
		ret = pc.delta.referenceBaseline(vid)
	}
	if ret {
		pc.refCounter++
		pc.traceLines.Trace("ref baseline: %s", vid.IDShortString)
	}
	return
}

func (pc *PastCone) UnReferenceAll() {
	pc.Assertf(pc.delta == nil, "UnReferenceAll: pc.delta == nil")
	unrefCounter := 0
	if pc.baseline != nil {
		pc.baseline.UnReference()
		unrefCounter++
		pc.traceLines.Trace("UnReferenceAll: unref baseline: %s", pc.baseline.IDShortString)
	}
	for vid := range pc.Vertices {
		vid.UnReference()
		unrefCounter++
		pc.traceLines.Trace("UnReferenceAll: unref tx %s", vid.IDShortString)
	}
	pc.Assertf(unrefCounter == pc.refCounter, "UnReferenceAll: unrefCounter(%d) not equal to pc.refCounter(%d) in %s\n%s",
		unrefCounter, pc.refCounter, pc.name, pc.traceLines.String)
}

func (pc *PastCone) BeginDelta() {
	util.Assertf(pc.delta == nil, "BeginDelta: pc.delta == nil")
	pc.delta = newPastConeBase(pc.baseline)
}

func (pc *PastCone) CommitDelta() {
	util.Assertf(pc.delta != nil, "CommitDelta: pc.delta != nil")
	util.Assertf(pc.baseline == nil || pc.baseline == pc.delta.baseline, "pc.baseline==nil || pc.baseline == pc.delta.baseline")

	pc.baseline = pc.delta.baseline
	for vid, flags := range pc.delta.Vertices {
		pc.Vertices[vid] = flags
	}
	for vid, rootedIndices := range pc.delta.Rooted {
		pc.Rooted[vid] = rootedIndices
	}
	pc.delta = nil
}

func (pc *PastCone) RollbackDelta() {
	if pc.delta == nil {
		return
	}
	unrefCounter := 0
	for vid := range pc.delta.Vertices {
		if _, ok := pc.Vertices[vid]; !ok {
			vid.UnReference()
			unrefCounter++
		}
		pc.traceLines.Add("RollbackDelta: unref %s", vid.IDShortString())
	}
	if pc.delta.baseline != nil && pc.baseline == nil {
		pc.delta.baseline.UnReference()
		pc.traceLines.Add("RollbackDelta: unref baseline %s", pc.delta.baseline.IDShortString())
	}
	pc.refCounter -= unrefCounter
	expected := len(pc.Vertices)
	if pc.baseline != nil {
		expected++
	}
	pc.Assertf(pc.refCounter == expected, "RollbackDelta: pc.refCounter(%d) not equal to expected(%d)", pc.refCounter, expected)
	pc.delta = nil
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
	if pc.delta == nil {
		pc.Vertices[vid] = pc.Flags(vid) | f
	} else {
		pc.delta.Vertices[vid] = pc.Flags(vid) | f
	}
}

func (pc *PastCone) mustReference(vid *WrappedTx) {
	util.Assertf(pc.reference(vid), "pb.reference(vid): %s", vid.IDShortString)
}

func (pc *PastCone) reference(vid *WrappedTx) bool {
	if !vid.Reference() {
		return false
	}
	pc.refCounter++
	if pc.delta == nil {
		pc.traceLines.Trace("ref %s", vid.IDShortString)
	} else {
		pc.traceLines.Trace("ref (delta) %s", vid.IDShortString)
	}
	return true
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
	rooted, _ := pc.isRootedVertex(vid)
	return pc.IsKnown(vid) && !rooted
}

func (pc *PastCone) IsKnownRooted(vid *WrappedTx) (rooted bool) {
	if !pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedIfRooted) {
		// not checked yet
		return false
	}
	rooted, _ = pc.isRootedVertex(vid)
	pc.Assertf(!rooted || pc.IsKnown(vid), "!rooted || pc.IsKnown(vid)")
	return
}

// MarkVertexUndefined vertex becomes 'known' but undefined and 'referenced'
func (pc *PastCone) MarkVertexUndefined(vid *WrappedTx) bool {
	pc.Assertf(!pc.IsKnownDefined(vid), "!pc.IsKnownDefined(vid)")
	// prevent repeated referencing
	if !pc.IsKnown(vid) {
		if !pc.reference(vid) {
			return false
		}
	}
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown)
	return true
}

// MarkVertexDefined marks 'defined' and enforces rooting has been checked
func (pc *PastCone) MarkVertexDefined(vid *WrappedTx) {
	pc.Assertf(pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedIfRooted), "flags.FlagsUp(FlagAttachedVertexCheckedIfRooted): %s", vid.IDShortString)
	pc.MarkVertexDefinedDoNotEnforceRootedCheck(vid)
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

// MustMarkVertexRooted vertex becomes 'known' and marked Rooted and 'defined'
func (pc *PastCone) MustMarkVertexRooted(vid *WrappedTx) {
	if !pc.IsKnown(vid) {
		pc.mustReference(vid)
	}
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown|FlagAttachedVertexCheckedIfRooted|FlagAttachedVertexDefined)
	// creates rooted entry if it does not exist yet, probably empty, i.e. with or without output indices
	if pc.delta == nil {
		pc.Rooted[vid] = pc.Rooted[vid]
	} else {
		if _, ok := pc.delta.Rooted[vid]; !ok {
			if rootedIndices, ok1 := pc.Rooted[vid]; ok1 {
				pc.delta.Rooted[vid] = rootedIndices.Clone()
			} else {
				pc.delta.Rooted[vid] = nil
			}
		}
	}
	pc.Assertf(pc.IsKnownRooted(vid), "pc.IsKnownNotRooted(vid)")
}

// MustMarkVertexNotRooted is marked definitely not rooted
func (pc *PastCone) MustMarkVertexNotRooted(vid *WrappedTx) {
	pc.Assertf(!pc.IsKnownRooted(vid), "!pc.IsKnownRooted(vid)")
	if !pc.IsKnown(vid) {
		pc.mustReference(vid)
	}
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown|FlagAttachedVertexCheckedIfRooted)
	pc.Assertf(pc.IsKnownNotRooted(vid), "pc.IsKnownNotRooted(vid)")
}

func (pc *PastCone) MustMarkOutputRooted(wOut WrappedOutput) {
	pc.MustMarkVertexRooted(wOut.VID)

	if pc.delta == nil {
		rootedIndices := pc.Rooted[wOut.VID]
		if len(rootedIndices) > 0 {
			rootedIndices.Insert(wOut.Index)
		} else {
			pc.Rooted[wOut.VID] = set.New[byte](wOut.Index)
		}
		return
	}
	// delta
	rootedIndices := pc.delta.Rooted[wOut.VID]
	if len(rootedIndices) > 0 {
		rootedIndices.Insert(wOut.Index)
		return
	}
	// element in delta does not exist. Copy it from committed part
	rootedIndices = pc.Rooted[wOut.VID]
	if len(rootedIndices) > 0 {
		rootedIndices = rootedIndices.Clone()
	} else {
		rootedIndices = set.New[byte]()
		pc.delta.Rooted[wOut.VID] = rootedIndices
	}
	rootedIndices.Insert(wOut.Index)
	// now delta contains copy of the committed part
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

func (pc *PastCone) Lines(prefix ...string) *lines.Lines {
	pc.Assertf(pc.delta == nil, "pc.delta==nil")
	ret := lines.New(prefix...)
	ret.Add("------ baseline: %s", util.Cond(pc.baseline == nil, "<nil>", pc.baseline.IDShortString()))
	sorted := util.KeysSorted(pc.Vertices, func(vid1, vid2 *WrappedTx) bool {
		return vid1.Before(vid2)
	})
	for i, vid := range sorted {
		ret.Add("#%d %s : %s", i, vid.IDShortString(), pc.Vertices[vid].String())
	}
	ret.Add("------ rooted:")
	sorted = util.KeysSorted(pc.Rooted, func(vid1, vid2 *WrappedTx) bool {
		return vid1.Before(vid2)
	})
	for i, vid := range sorted {
		ln := pc.Rooted[vid].Lines(func(key byte) string {
			return strconv.Itoa(int(key))
		})
		ret.Add("#%d %s : %s", i, vid.IDShortString(), ln.Join(","))
	}
	return ret
}

func (pc *PastCone) UndefinedList() []*WrappedTx {
	pc.Assertf(pc.delta == nil, "pc.delta==nil")

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
