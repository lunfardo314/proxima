package vertex

import (
	"fmt"
	"slices"
	"sort"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/maps"
)

// Attacher keeps list of past cone vertices.
// The vertices of consideration are all vertices in the past cone back to the 'rooted' ones
// 'rooted' are the ones which belong to the baseline state.
// each vertex in the attacher has local flags, which defines its status in the scope of the attacher
// The goal of the attacher is to make all vertices marked as 'defined', i.e. either 'rooted' or with its past cone checked
// and valid
// Flags (except 'asked for poke') become final and immutable after they are set 'ON'

type (
	FlagsPastCone byte

	PastCone struct {
		global.Logging
		tip                *WrappedTx
		targetTs           ledger.Time
		name               string
		coverageDelta      uint64
		savedCoverageDelta uint64

		*PastConeBase
		delta      *PastConeBase
		refCounter int
		traceLines *lines.Lines
	}

	PastConeBase struct {
		baseline          *WrappedTx
		vertices          map[*WrappedTx]FlagsPastCone // byte is used by attacher for flags
		virtuallyConsumed map[*WrappedTx]set.Set[byte]
	}
)

const (
	FlagPastConeVertexKnown             = FlagsPastCone(0b00000001) // each vertex of consideration has this flag on
	FlagPastConeVertexDefined           = FlagsPastCone(0b00000010) // means vertex is 'defined', i.e. its validity is checked
	FlagPastConeVertexCheckedInTheState = FlagsPastCone(0b00000100) // means vertex has been checked if it is in the state (it may or may not be there)
	FlagPastConeVertexInTheState        = FlagsPastCone(0b00001000) // means vertex is definitely in the state (must be checked before)
	FlagPastConeVertexEndorsementsSolid = FlagsPastCone(0b00010000) // means all endorsements were validated
	FlagPastConeVertexInputsSolid       = FlagsPastCone(0b00100000) // means all consumed inputs are checked and valid
	FlagPastConeVertexAskedForPoke      = FlagsPastCone(0b01000000) //
)

func (f FlagsPastCone) FlagsUp(fl FlagsPastCone) bool {
	return f&fl == fl
}

func (f FlagsPastCone) String() string {
	return fmt.Sprintf("%08b known: %v, defined: %v, inTheState: (%v,%v), endorsementsOk: %v, inputsOk: %v, poke: %v",
		f,
		f.FlagsUp(FlagPastConeVertexKnown),
		f.FlagsUp(FlagPastConeVertexDefined),
		f.FlagsUp(FlagPastConeVertexCheckedInTheState),
		f.FlagsUp(FlagPastConeVertexInTheState),
		f.FlagsUp(FlagPastConeVertexEndorsementsSolid),
		f.FlagsUp(FlagPastConeVertexInputsSolid),
		f.FlagsUp(FlagPastConeVertexAskedForPoke),
	)
}

func NewPastConeBase(baseline *WrappedTx) *PastConeBase {
	return &PastConeBase{
		vertices: make(map[*WrappedTx]FlagsPastCone),
		baseline: baseline,
	}
}

func NewPastCone(env global.Logging, tip *WrappedTx, targetTs ledger.Time, name string) *PastCone {
	return newPastConeFromBase(env, tip, targetTs, name, NewPastConeBase(nil))
}

func newPastConeFromBase(env global.Logging, tip *WrappedTx, targetTs ledger.Time, name string, pb *PastConeBase) *PastCone {
	return &PastCone{
		Logging:      env,
		tip:          tip,
		targetTs:     targetTs,
		name:         name,
		PastConeBase: pb,
		traceLines:   lines.NewDummy(),
		//traceLines: lines.New("     "),
	}
}

func (pc *PastCone) Tip() *WrappedTx {
	return pc.tip
}

func (pb *PastConeBase) CloneImmutable() *PastConeBase {
	util.Assertf(len(pb.virtuallyConsumed) == 0, "len(pb.virtuallyConsumed)==0")

	ret := &PastConeBase{
		baseline: pb.baseline,
		vertices: make(map[*WrappedTx]FlagsPastCone, len(pb.vertices)),
	}
	for vid, flags := range pb.vertices {
		ret.vertices[vid] = flags
	}
	return ret
}

func (pb *PastConeBase) setBaseline(vid *WrappedTx) {
	util.Assertf(pb.baseline == nil, "setBaseline: pb.baseline == nil")
	pb.baseline = vid
}

func (pb *PastConeBase) addVirtuallyConsumedOutput(wOut WrappedOutput) {
	if pb.virtuallyConsumed == nil {
		pb.virtuallyConsumed = map[*WrappedTx]set.Set[byte]{}
	}
	if consumedIndices := pb.virtuallyConsumed[wOut.VID]; len(consumedIndices) == 0 {
		pb.virtuallyConsumed[wOut.VID] = set.New[byte](wOut.Index)
	} else {
		consumedIndices.Insert(wOut.Index)
	}
}

func (pc *PastCone) AddVirtuallyConsumedOutput(wOut WrappedOutput, stateReader global.IndexedStateReader) *WrappedOutput {
	if pc.delta == nil {
		pc.addVirtuallyConsumedOutput(wOut)
		return pc.Check(stateReader)
	}
	if pc.isVirtuallyConsumed(wOut) {
		return nil
	}
	pc.delta.addVirtuallyConsumedOutput(wOut)
	return pc.Check(stateReader)
}

func (pc *PastCone) isVirtuallyConsumed(wOut WrappedOutput) bool {
	if pc.PastConeBase._isVirtuallyConsumed(wOut) {
		return true
	}
	if pc.delta != nil {
		return pc.delta._isVirtuallyConsumed(wOut)
	}
	return false
}

func (pb *PastConeBase) _isVirtuallyConsumed(wOut WrappedOutput) bool {
	if len(pb.virtuallyConsumed) == 0 {
		return false
	}
	if consumedIndices := pb.virtuallyConsumed[wOut.VID]; len(consumedIndices) > 0 {
		return consumedIndices.Contains(wOut.Index)
	}
	return false
}

func (pc *PastCone) Assertf(cond bool, format string, args ...any) {
	if cond {
		return
	}
	pcStr := pc.LinesShort("      ").Join("\n")
	argsExt := append(slices.Clone(args), pcStr)
	pc.Logging.Assertf(cond, format+"\n---- past cone ----\n%s", argsExt...)
}

func (pc *PastCone) SetBaseline(vid *WrappedTx) bool {
	pc.Assertf(pc.baseline == nil, "SetBaseline: pc.baseline == nil")
	pc.Assertf(vid.IsBranchTransaction(), "vid.IsBranchTransaction(): %s", vid.IDShortString)

	if pc.delta == nil {
		pc.setBaseline(vid)
	} else {
		pc.delta.setBaseline(vid)
	}
	return pc.markVertexWithFlags(vid, FlagPastConeVertexKnown|FlagPastConeVertexDefined|FlagPastConeVertexCheckedInTheState|FlagPastConeVertexInTheState)
}

func (pc *PastCone) UnReferenceAll() {
	pc.Assertf(pc.delta == nil, "UnReferenceAll: pc.delta == nil")
	unrefCounter := 0
	//if pc.baseline != nil {
	//	pc.baseline.UnReference()
	//	unrefCounter++
	//	pc.traceLines.Trace("UnReferenceAll: unref baseline: %s", pc.baseline.IDShortString)
	//}
	for vid := range pc.vertices {
		vid.UnReference()
		unrefCounter++
		pc.traceLines.Trace("UnReferenceAll: unref tx %s", vid.IDShortString)
	}
	pc.Assertf(unrefCounter == pc.refCounter, "UnReferenceAll: unrefCounter(%d) not equal to pc.refCounter(%d) in %s\n%s",
		unrefCounter, pc.refCounter, pc.name, pc.traceLines.String)
}

func (pc *PastCone) BeginDelta() {
	util.Assertf(pc.delta == nil, "BeginDelta: pc.delta == nil")
	pc.delta = NewPastConeBase(pc.baseline)
	pc.savedCoverageDelta = pc.coverageDelta
}

func (pc *PastCone) CommitDelta() {
	util.Assertf(pc.delta != nil, "CommitDelta: pc.delta != nil")
	util.Assertf(pc.baseline == nil || pc.baseline == pc.delta.baseline, "pc.baseline==nil || pc.baseline == pc.delta.baseline")

	pc.baseline = pc.delta.baseline
	for vid, flags := range pc.delta.vertices {
		pc.vertices[vid] = flags
	}
	for vid, consumedIndices := range pc.delta.virtuallyConsumed {
		for idx := range consumedIndices {
			pc.addVirtuallyConsumedOutput(WrappedOutput{VID: vid, Index: idx})
		}
	}
	pc.delta = nil
}

func (pc *PastCone) RollbackDelta() {
	if pc.delta == nil {
		return
	}
	unrefCounter := 0
	for vid := range pc.delta.vertices {
		if _, ok := pc.vertices[vid]; !ok {
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
	expected := len(pc.vertices)
	pc.Assertf(pc.refCounter == expected, "RollbackDelta: pc.refCounter(%d) not equal to expected(%d)", pc.refCounter, expected)
	pc.delta = nil
	pc.coverageDelta = pc.savedCoverageDelta
}

func (pc *PastCone) Flags(vid *WrappedTx) FlagsPastCone {
	if pc.delta == nil {
		return pc.vertices[vid]
	}
	if f, ok := pc.delta.vertices[vid]; ok {
		return f
	}
	return pc.vertices[vid]
}

func (pc *PastCone) SetFlagsUp(vid *WrappedTx, f FlagsPastCone) {
	if vid.IDHasFragment("007d") {
		pc.Log().Infof(">>#### %s SetFlagsUp: %s", pc.name, f.String())
	}
	if pc.delta == nil {
		pc.vertices[vid] = pc.Flags(vid) | f
	} else {
		pc.delta.vertices[vid] = pc.Flags(vid) | f
	}
}

func (pc *PastCone) SetFlagsDown(vid *WrappedTx, f FlagsPastCone) {
	if pc.delta == nil {
		pc.vertices[vid] = pc.Flags(vid) & ^f
	} else {
		pc.delta.vertices[vid] = pc.Flags(vid) & ^f
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
	return pc.Flags(vid).FlagsUp(FlagPastConeVertexKnown)
}

func (pc *PastCone) IsKnownDefined(vid *WrappedTx) bool {
	return pc.Flags(vid).FlagsUp(FlagPastConeVertexKnown | FlagPastConeVertexDefined)
}

func (pc *PastCone) isVertexInTheState(vid *WrappedTx) (inTheState bool) {
	if inTheState = pc.Flags(vid).FlagsUp(FlagPastConeVertexInTheState); inTheState {
		pc.Assertf(pc.Flags(vid).FlagsUp(FlagPastConeVertexCheckedInTheState), "pc.Flags(vid).FlagsUp(FlagPastConeVertexCheckedInTheState)")
	}
	return
}

// isNotInTheState is definitely known it is not in the state
func (pc *PastCone) isNotInTheState(vid *WrappedTx) bool {
	return pc.IsKnown(vid) &&
		pc.Flags(vid).FlagsUp(FlagPastConeVertexCheckedInTheState) &&
		!pc.Flags(vid).FlagsUp(FlagPastConeVertexInTheState)
}

// IsInTheState is definitely known it is in the state
func (pc *PastCone) IsInTheState(vid *WrappedTx) (rooted bool) {
	return pc.IsKnown(vid) && pc.isVertexInTheState(vid)
}

func (pc *PastCone) MarkVertexKnown(vid *WrappedTx) bool {
	// prevent repeated referencing
	if !pc.IsKnown(vid) {
		if !pc.reference(vid) {
			return false
		}
	}
	pc.SetFlagsUp(vid, FlagPastConeVertexKnown)
	return true
}

func (pc *PastCone) markVertexWithFlags(vid *WrappedTx, flags FlagsPastCone) bool {
	if !pc.IsKnown(vid) {
		if !pc.reference(vid) {
			return false
		}
	}
	pc.SetFlagsUp(vid, flags)
	return true
}

// MustMarkVertexNotInTheState is marked definitely not rooted
func (pc *PastCone) MustMarkVertexNotInTheState(vid *WrappedTx) {
	pc.Assertf(!pc.IsInTheState(vid), "!pc.IsInTheState(vid)")
	if !pc.IsKnown(vid) {
		pc.mustReference(vid)
	}
	pc.SetFlagsUp(vid, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState)
	pc.Assertf(pc.isNotInTheState(vid), "pc.isNotInTheState(vid)")
}

func (pc *PastCone) ContainsUndefined() bool {
	util.Assertf(pc.delta == nil, "pc.delta==nil")
	for vid, flags := range pc.vertices {
		if !flags.FlagsUp(FlagPastConeVertexDefined) && vid != pc.tip {
			return true
		}
	}
	return false
}

// forAllVertices traverses all vertices, both committed and uncommitted
func (pc *PastCone) forAllVertices(fun func(vid *WrappedTx) bool, sortAsc ...bool) {
	all := set.New[*WrappedTx]()
	for vid := range pc.vertices {
		all.Insert(vid)
	}
	if pc.delta != nil {
		for vid := range pc.delta.vertices {
			all.Insert(vid)
		}
	}
	if len(sortAsc) == 0 {
		// no sorting
		for vid := range all {
			if !fun(vid) {
				return
			}
		}
		return
	}
	// requires sorting
	allSlice := maps.Keys(all)
	sort.Slice(allSlice, func(i, j int) bool {
		if !sortAsc[0] {
			i, j = j, i
		}
		return allSlice[i].Before(allSlice[j])
	})
	for _, vid := range allSlice {
		if !fun(vid) {
			return
		}
	}
}

func (pc *PastCone) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("------ past cone: '%s'", pc.name).
		Add("------ baseline: %s, coverage: %s",
			util.Cond(pc.baseline == nil, "<nil>", pc.baseline.IDShortString()),
			pc.baseline.GetLedgerCoverageString(),
		)

	//rooted := make([]WrappedOutput, 0)
	counter := 0
	pc.forAllVertices(func(vid *WrappedTx) bool {
		pc._addVertexLine(counter, vid, ret)
		counter++
		return true
	}, true)

	if len(pc.virtuallyConsumed) > 0 {
		ret.Add("----- virtually consumed ----")
		for vid, consumedIndices := range pc.virtuallyConsumed {
			ret.Add("   %s: %+v", vid.IDShortString(), maps.Keys(consumedIndices))
		}
	}
	//ret.Add("----- rooted ----")
	//for _, wOut := range rooted {
	//	covStr := "n/a"
	//	o, err := wOut.VID.OutputAt(wOut.Index)
	//	if err == nil && o != nil {
	//		covStr = util.Th(o.Amount())
	//	}
	//	ret.Add("   %s: amount: %s", wOut.IDShortString(), covStr)
	//}
	coverage, delta := pc.CoverageAndDelta()
	ret.Add("ledger coverage: %s, delta: %s", util.Th(coverage), util.Th(delta))
	return ret
}

func (pc *PastCone) _addVertexLine(n int, vid *WrappedTx, ln *lines.Lines) {
	stateStr := "?"
	if pc.IsInTheState(vid) {
		stateStr = "+"
	} else {
		if pc.isNotInTheState(vid) {
			stateStr = "-"
		}
	}

	lnOut := lines.New()
	for idx, consumers := range pc.consumersByOutputIndex(vid) {
		lnCons := lines.New()
		for _, consumer := range consumers {
			if consumer != nil {
				lnCons.Add("%s", consumer.IDShortString())
			} else {
				lnCons.Add("<nil>")
			}
		}
		lnOut.Add("%d: {%s}", idx, lnCons.Join(", "))
	}
	ln.Add("#%d S%s %s consumers: {%s} flags: %s", n, stateStr, vid.IDShortString(), lnOut.Join(", "), pc.Flags(vid).String())
}

func (pc *PastCone) LinesShort(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	blStr := "<nil>"
	if pc.baseline != nil {
		blStr = pc.baseline.IDShortString()
	}
	ret.Add("------ past cone: '%s'", pc.name).
		Add("------ baseline: %s, coverage: %s", blStr, pc.baseline.GetLedgerCoverageString())
	counter := 0
	pc.forAllVertices(func(vid *WrappedTx) bool {
		ret.Add("#%d %s : %s", counter, vid.IDShortString(), pc.vertices[vid].String())
		counter++
		return true
	}, true)
	if len(pc.virtuallyConsumed) > 0 {
		ret.Add("----- virtually consumed ----")
		for vid, consumedIndices := range pc.virtuallyConsumed {
			ret.Add("   %s: %+v", vid.IDShortString(), maps.Keys(consumedIndices))
		}
	}
	return ret
}

func (pc *PastCone) findConsumersOf(wOut WrappedOutput) []*WrappedTx {
	wOut.VID.mutexDescendants.RLock()
	defer wOut.VID.mutexDescendants.RUnlock()

	return pc.findConsumersNoLock(wOut)
}

func (pc *PastCone) findConsumersNoLock(wOut WrappedOutput) []*WrappedTx {
	ret := make([]*WrappedTx, 0)
	if virtuallyConsumed := pc.isVirtuallyConsumed(wOut); virtuallyConsumed {
		ret = append(ret, nil)
	}
	return append(ret, pc._filterConsumingVertices(wOut.VID.consumed[wOut.Index])...)
}

func (pc *PastCone) _filterConsumingVertices(consumers set.Set[*WrappedTx]) []*WrappedTx {
	ret := make([]*WrappedTx, 0, 2)
	for vid := range consumers {
		if pc.IsKnown(vid) {
			ret = append(ret, vid)
		}
	}
	if len(ret) == 0 {
		return nil
	}
	return ret
}

func (pb *PastConeBase) _virtuallyConsumedIndexSet(vid *WrappedTx) set.Set[byte] {
	if len(pb.virtuallyConsumed) == 0 {
		return set.New[byte]()
	}
	ret := pb.virtuallyConsumed[vid]
	if len(ret) == 0 {
		return set.New[byte]()
	}
	return ret.Clone()
}

func (pc *PastCone) consumersByOutputIndex(vid *WrappedTx) map[byte][]*WrappedTx {
	ret := make(map[byte][]*WrappedTx)

	virtuallyConsumedIndices := pc._virtuallyConsumedIndexSet(vid)
	if pc.delta != nil {
		virtuallyConsumedIndices.AddAll(pc.delta._virtuallyConsumedIndexSet(vid))
	}

	vid.mutexDescendants.RLock()
	defer vid.mutexDescendants.RUnlock()

	for idx, allConsumers := range vid.consumed {
		consumers := pc._filterConsumingVertices(allConsumers)
		if len(consumers) > 0 {
			ret[idx] = consumers
		}
	}

	for idx := range virtuallyConsumedIndices {
		lst := ret[idx]
		if len(lst) > 0 {
			lst = append(lst, nil)
		} else {
			lst = []*WrappedTx{nil}
		}
		ret[idx] = lst
	}

	if len(ret) > 0 {
		return ret
	}
	return nil
}

// mustNotConsumedIndices returns indices of the transaction which are definitely not consumed
// panics in case of conflicting past cone
func (pc *PastCone) producedIndices(vid *WrappedTx) []byte {
	numProduced := vid.NumProducedOutputs()
	pc.Assertf(numProduced > 0, "numProduced>0")

	if pc.IsInTheState(vid) {
		return nil
	}
	byIdx := pc.consumersByOutputIndex(vid)

	ret := make([]byte, 0, numProduced-len(byIdx))
	for i := 0; i < numProduced; i++ {
		if _, found := byIdx[byte(i)]; !found {
			ret = append(ret, byte(i))
		}
	}
	if len(ret) > 0 {
		return ret
	}
	return nil
}

type MutationStats struct {
	NumTransactions int
	NumDeleted      int
	NumCreated      int
}

func (pc *PastCone) Mutations(slot ledger.Slot) (muts *multistate.Mutations, stats MutationStats) {
	muts = multistate.NewMutations()

	// generate ADD TX and ADD OUTPUT mutations
	for vid := range pc.vertices {
		if pc.IsInTheState(vid) {
			// generate DEL mutations
			for idx, consumersOfRooted := range pc.consumersByOutputIndex(vid) {
				pc.Assertf(len(consumersOfRooted) == 1, "Mutations: len(consumersOfRooted)==1")

				if pc.isNotInTheState(consumersOfRooted[0]) {
					muts.InsertDelOutputMutation(vid.OutputID(idx))
					stats.NumDeleted++
				}
			}
		} else {
			// TODO no need to store number of outputs: now all is contained in the ID
			muts.InsertAddTxMutation(vid.ID, slot, byte(vid.ID.NumProducedOutputs()-1))
			stats.NumTransactions++

			// ADD OUTPUT mutations only for not consumed outputs
			for _, idx := range pc.producedIndices(vid) {
				muts.InsertAddOutputMutation(vid.OutputID(idx), vid.MustOutputAt(idx))
				stats.NumCreated++
			}
		}
	}
	return
}

func (pc *PastCone) hasRooted() bool {
	for _, flags := range pc.vertices {
		if flags.FlagsUp(FlagPastConeVertexInTheState) {
			return true
		}
	}
	return false
}

func (pc *PastCone) IsComplete() bool {
	switch {
	case pc.delta != nil:
		return false
	case pc.ContainsUndefined():
		return false
	case !pc.hasRooted():
		return false
	}
	return true
}

func (pc *PastCone) getBaseline() *WrappedTx {
	if pc.baseline != nil {
		return pc.baseline
	}
	if pc.delta != nil {
		return pc.delta.baseline
	}
	return nil
}

// AppendPastCone appends deterministic past cone to the current one. Does not check for conflicts
func (pc *PastCone) AppendPastCone(pcb *PastConeBase, getStateReader func() global.IndexedStateReader) {
	baseline := pc.getBaseline()
	pc.Assertf(baseline != nil, "pc.hasBaseline()")
	pc.Assertf(pcb.baseline != nil, "pcb.baseline != nil")
	pc.Assertf(baseline.IsContainingBranchOf(pcb.baseline, getStateReader), "baseline.IsContainingBranchOf(pcb.baseline, getStateReader)")
	// we require baselines must be compatible (on the same chain) of pcb should not be younger than pc
	if len(pcb.vertices) == 0 {
		return
	}
	baselineStateReader := getStateReader()

	for vid, flags := range pcb.vertices {
		pc.Assertf(flags.FlagsUp(FlagPastConeVertexKnown|FlagPastConeVertexDefined), "inconsistent flag in appended past cone: %s", flags.String())

		if !flags.FlagsUp(FlagPastConeVertexInTheState) {
			// if vertex is in the state of the appended past cone, it will be in the state of the new baseline
			// When vertex not in appended baseline, check if it didn't become known in the new one
			if baselineStateReader.KnowsCommittedTransaction(&vid.ID) {
				flags |= FlagPastConeVertexCheckedInTheState | FlagPastConeVertexInTheState
			}
		}
		// it will also create a new entry in the target past cone if necessary
		pc.markVertexWithFlags(vid, flags & ^FlagPastConeVertexAskedForPoke)
	}
}

// CheckFinalPastCone check determinism consistency of the past cone
// If rootVid == nil, past cone must be fully deterministic
func (pc *PastCone) CheckFinalPastCone(getStateReader func() global.IndexedStateReader) (err error) {
	if pc.delta != nil {
		return fmt.Errorf("CheckFinalPastCone: past cone has uncommitted delta")
	}
	if pc.ContainsUndefined() {
		return fmt.Errorf("CheckFinalPastCone: still contains undefined Vertices")
	}

	// should be at least one 'rooted' output ( ledger baselineCoverage must be > 0)
	if !pc.hasRooted() {
		return fmt.Errorf("CheckFinalPastCone: at least one rooted output is expected")
	}
	if len(pc.vertices) == 0 {
		return fmt.Errorf("CheckFinalPastCone: 'vertices' is empty")
	}
	for vid := range pc.vertices {
		if err = pc.checkFinalFlags(vid); err != nil {
			return
		}
		status := vid.GetTxStatus()
		if status == Bad {
			return fmt.Errorf("BAD vertex in the past cone: %s", vid.IDShortString())
		}
		if pc.IsInTheState(vid) {
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
	if conflict := pc.Check(getStateReader()); conflict != nil {
		return fmt.Errorf("past cone %s contains double-spent output %s", pc.name, conflict.IDShortString())
	}
	return nil
}

func (pc *PastCone) checkFinalFlags(vid *WrappedTx) error {
	flags := pc.Flags(vid)
	wrongFlag := ""

	pc.Assertf(pc.baseline != nil, "checkFinalFlags: pc.baseline != nil")

	switch {
	case !flags.FlagsUp(FlagPastConeVertexKnown):
		wrongFlag = "FlagPastConeVertexKnown"
	case !flags.FlagsUp(FlagPastConeVertexDefined):
		wrongFlag = "FlagPastConeVertexDefined"
	case flags.FlagsUp(FlagPastConeVertexInTheState):
		if !flags.FlagsUp(FlagPastConeVertexCheckedInTheState) {
			wrongFlag = "FlagPastConeVertexCheckedInTheState"
		}
	case vid.IsBranchTransaction():
		switch {
		case pc.baseline == vid:
			return fmt.Errorf("checkFinalFlags: must be baseline")
		}
	default:
		switch {
		case !flags.FlagsUp(FlagPastConeVertexInputsSolid):
			wrongFlag = "FlagPastConeVertexEndorsementsSolid"
		case !flags.FlagsUp(FlagPastConeVertexEndorsementsSolid):
			wrongFlag = "FlagPastConeVertexEndorsementsSolid"
		}
	}
	if wrongFlag != "" {
		return fmt.Errorf("checkFinalFlags: wrong %s flag  %08b in %s", wrongFlag, flags, vid.IDShortString())
	}
	return nil
}

func (pc *PastCone) CloneForDebugOnly(env global.Logging, name string) *PastCone {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	ret := NewPastCone(env, pc.tip, pc.targetTs, name+"_debug_clone")
	ret.baseline = pc.baseline
	ret.coverageDelta = pc.coverageDelta
	ret.vertices = maps.Clone(pc.vertices)
	ret.virtuallyConsumed = make(map[*WrappedTx]set.Set[byte])
	for vid, consumedIndices := range pc.virtuallyConsumed {
		ret.virtuallyConsumed[vid] = consumedIndices.Clone()
	}
	return ret
}

func (pb *PastConeBase) Len() int {
	return len(pb.vertices)
}

// Check returns double-spent output (conflict), or nil if past cone is consistent
// The complexity is O(NxM) where N is number of vertices and M is average number of conflicts in the UTXO tangle
// Practically, it is linear wrt number of vertices because M is 1 or close to 1.
// for optimization, latest time value can be specified
func (pc *PastCone) Check(stateReader global.IndexedStateReader) (conflict *WrappedOutput) {
	var coverageDelta uint64
	pc.coverageDelta = 0
	pc.forAllVertices(func(vid *WrappedTx) bool {
		conflict, _, coverageDelta = pc._checkVertex(vid, stateReader)
		pc.coverageDelta += coverageDelta
		return conflict == nil
	})
	if conflict != nil {
		pc.coverageDelta = 0
	}
	return
}

// CheckAndClean iterates past cone, checks for conflicts and removes those vertices
// which has consumers and all consumers are already in the state
func (pc *PastCone) CheckAndClean(stateReader global.IndexedStateReader) (conflict *WrappedOutput) {
	pc.Assertf(pc.baseline != nil, "pc.baseline!=nil")
	pc.Assertf(len(pc.virtuallyConsumed) == 0, "len(pb.virtuallyConsumed)==0")
	pc.Assertf(pc.delta == nil, "pc.delta == nil")

	var canBeRemoved bool
	var coverageDelta uint64
	toDelete := make([]*WrappedTx, 0)
	for vid, flags := range pc.vertices {
		if vid == pc.tip {
			continue
		}
		pc.Assertf(flags.FlagsUp(FlagPastConeVertexKnown|FlagPastConeVertexDefined|FlagPastConeVertexCheckedInTheState), "wrong flag in %s", vid.IDShortString)
		conflict, canBeRemoved, coverageDelta = pc._checkVertex(vid, stateReader)
		if conflict != nil {
			pc.coverageDelta = 0
			return
		}
		if canBeRemoved {
			toDelete = append(toDelete, vid)
		} else {
			pc.coverageDelta += coverageDelta
		}
	}
	for _, vid := range toDelete {
		delete(pc.vertices, vid)
		vid.UnReference()
		pc.refCounter--
	}
	return
}

func (pc *PastCone) _checkVertex(vid *WrappedTx, stateReader global.IndexedStateReader) (doubleSpend *WrappedOutput, canBeRemoved bool, coverageDelta uint64) {
	allConsumersAreInTheState := true
	inTheState := pc.IsInTheState(vid)
	byIdx := pc.consumersByOutputIndex(vid)
	for idx, consumers := range byIdx {
		wOut := WrappedOutput{VID: vid, Index: idx}
		pc.Assertf(len(consumers) > 0, "len(consumers) > 0")
		if len(consumers) != 1 {
			return &wOut, false, 0
		}
		pc.Assertf(len(consumers) == 1, "len(consumers) == 1")
		//if consumers[0] == nil {
		//	// virtual consumer
		//	allConsumersAreInTheState = false
		//} else {
		//	// real consumer
		//	pc.Assertf(pc.Flags(consumers[0]).FlagsUp(FlagPastConeVertexCheckedInTheState), "pc.Flags(consumers[0]).FlagsUp(FlagPastConeVertexCheckedInTheState)")
		//	if !pc.IsInTheState(consumers[0]) {
		//		allConsumersAreInTheState = false
		//		// must be in the state
		//		if inTheState && !stateReader.HasUTXO(wOut.DecodeID()) {
		//			return &wOut, false, 0
		//		}
		//		coverageDelta += vid.MustOutputAt(idx).Amount()
		//	}
		//}

		// virtual consumer nil is never in the state
		if !pc.IsInTheState(consumers[0]) {
			allConsumersAreInTheState = false
			if inTheState {
				if !stateReader.HasUTXO(wOut.DecodeID()) {
					return &wOut, false, 0
				}
				coverageDelta += vid.MustOutputAt(idx).Amount()
			}
		}
	}
	canBeRemoved = len(byIdx) > 0 && allConsumersAreInTheState
	pc.Assertf(!canBeRemoved || coverageDelta == 0, "!canBeRemoved || coverageDelta == 0")
	return
}

func (pc *PastCone) CalculateSlotInflation() (ret uint64) {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	for vid := range pc.vertices {
		if pc.isNotInTheState(vid) && vid.IsSequencerMilestone() {
			ret += vid.InflationAmountOfSequencerMilestone()
		}
	}
	return
}

func (pc *PastCone) CoverageAndDelta() (coverage, delta uint64) {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	pc.Assertf(pc.baseline != nil, "pc.baseline != nil")

	delta = pc.coverageDelta
	ts := pc.targetTs
	if pc.tip != nil {
		ts = pc.tip.Timestamp()
	}
	diffSlots := ts.Slot() - pc.baseline.Slot()
	if ts.IsSlotBoundary() {
		coverage = (pc.baseline.GetLedgerCoverage() >> diffSlots) + delta
	} else {
		coverage = (pc.baseline.GetLedgerCoverage() >> (diffSlots + 1)) + delta
	}
	coverage += pc.ledgerCoverageAdjustment()
	return
}

// ledgerCoverageAdjustment if sequencer output of the baseline in not consumed,
// ledger coverage must be adjusted by the branch inflation
func (pc *PastCone) ledgerCoverageAdjustment() uint64 {
	wOut := pc.baseline.SequencerWrappedOutput()
	if len(pc.findConsumersOf(wOut)) == 0 {
		o, err := pc.baseline.OutputAt(wOut.Index)
		pc.AssertNoError(err)
		return o.Inflation(true)
	}
	return 0
}

func (pc *PastCone) LedgerCoverage() uint64 {
	ret, _ := pc.CoverageAndDelta()
	return ret
}

func (pc *PastCone) UndefinedList() []*WrappedTx {
	pc.Assertf(pc.delta == nil, "pc.delta==nil")

	ret := make([]*WrappedTx, 0)
	for vid, flags := range pc.vertices {
		if !flags.FlagsUp(FlagPastConeVertexDefined) {
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

func (pc *PastCone) NumVertices() int {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	return len(pc.vertices)
}
