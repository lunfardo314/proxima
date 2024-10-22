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
		vertices map[*WrappedTx]FlagsPastCone // byte is used by attacher for flags
	}
)

const (
	FlagAttachedVertexKnown             = FlagsPastCone(0b00000001) // each vertex of consideration has this flag on
	FlagAttachedVertexDefined           = FlagsPastCone(0b00000010) // means vertex is 'defined', i.e. its validity is checked
	FlagAttachedVertexCheckedInTheState = FlagsPastCone(0b00000100) // means vertex has been checked if it is in the state (it may or may not be there)
	FlagAttachedVertexInTheState        = FlagsPastCone(0b00001000) // means vertex is definitely in the state (must be checked before)
	FlagAttachedVertexEndorsementsSolid = FlagsPastCone(0b00010000) // means all endorsements were validated
	FlagAttachedVertexInputsSolid       = FlagsPastCone(0b00100000) // means all consumed inputs are checked and valid
	FlagAttachedVertexAskedForPoke      = FlagsPastCone(0b01000000) //
)

func (f FlagsPastCone) FlagsUp(fl FlagsPastCone) bool {
	return f&fl == fl
}

func (f FlagsPastCone) String() string {
	return fmt.Sprintf("%08b known: %v, defined: %v, rooted: (%v,%v), endorsementsOk: %v, inputsOk: %v, poke: %v",
		f,
		f.FlagsUp(FlagAttachedVertexKnown),
		f.FlagsUp(FlagAttachedVertexDefined),
		f.FlagsUp(FlagAttachedVertexCheckedInTheState),
		f.FlagsUp(FlagAttachedVertexInTheState),
		f.FlagsUp(FlagAttachedVertexEndorsementsSolid),
		f.FlagsUp(FlagAttachedVertexInputsSolid),
		f.FlagsUp(FlagAttachedVertexAskedForPoke),
	)
}

func newPastConeBase(baseline *WrappedTx) *PastConeBase {
	return &PastConeBase{
		vertices: make(map[*WrappedTx]FlagsPastCone),
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
	util.Assertf(pb.baseline == nil, "referenceBaseline: pb.baseline == nil")
	if !vid.Reference() {
		return false
	}
	pb.baseline = vid
	return true
}

func (pc *PastCone) Assertf(cond bool, format string, args ...any) {
	if cond {
		return
	}
	pcStr := pc.Lines("      ").Join("\n")
	argsExt := append(slices.Clone(args), pcStr)
	pc.Logging.Assertf(cond, format+"\n---- past cone ----\n%s", argsExt...)
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
	pc.delta = newPastConeBase(pc.baseline)
}

func (pc *PastCone) CommitDelta() {
	util.Assertf(pc.delta != nil, "CommitDelta: pc.delta != nil")
	util.Assertf(pc.baseline == nil || pc.baseline == pc.delta.baseline, "pc.baseline==nil || pc.baseline == pc.delta.baseline")

	pc.baseline = pc.delta.baseline
	for vid, flags := range pc.delta.vertices {
		pc.vertices[vid] = flags
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
	if pc.baseline != nil {
		expected++
	}
	pc.Assertf(pc.refCounter == expected, "RollbackDelta: pc.refCounter(%d) not equal to expected(%d)", pc.refCounter, expected)
	pc.delta = nil
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

func (pc *PastCone) isVertexInTheState(vid *WrappedTx) (rooted bool) {
	if rooted = pc.Flags(vid).FlagsUp(FlagAttachedVertexInTheState); rooted {
		pc.Assertf(pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedInTheState), "pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedInTheState)")
	}
	return
}

// IsNotInTheState is definitely known it is not in the state
func (pc *PastCone) IsNotInTheState(vid *WrappedTx) bool {
	return pc.IsKnown(vid) &&
		pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedInTheState) &&
		!pc.Flags(vid).FlagsUp(FlagAttachedVertexInTheState)
}

func (pc *PastCone) IsKnownInTheState(vid *WrappedTx) (rooted bool) {
	return pc.IsKnown(vid) && pc.isVertexInTheState(vid)
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
	pc.Assertf(pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedInTheState), "flags.FlagsUp(FlagAttachedVertexCheckedInTheState): %s", vid.IDShortString)
	pc.MarkVertexDefinedDoNotEnforceRootedCheck(vid)
}

func (pc *PastCone) MustMarkVertexWithFlags(vid *WrappedTx, flags FlagsPastCone) {
	flags &= ^FlagAttachedVertexAskedForPoke
	if !pc.IsKnown(vid) {
		pc.mustReference(vid)
	}
	pc.SetFlagsUp(vid, flags)
}

func (pc *PastCone) MarkVertexDefinedDoNotEnforceRootedCheck(vid *WrappedTx) {
	flags := pc.Flags(vid)
	if pc.IsKnownInTheState(vid) {
		pc.Assertf(!flags.FlagsUp(FlagAttachedVertexInputsSolid), "MarkVertexDefinedDoNotEnforceRootedCheck: !flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s",
			vid.IDShortString, flags.String)
		pc.Assertf(!flags.FlagsUp(FlagAttachedVertexEndorsementsSolid), "MarkVertexDefinedDoNotEnforceRootedCheck: !flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s",
			vid.IDShortString, flags.String)
	}
	if !vid.IsSequencerMilestone() {
		if pc.IsNotInTheState(vid) {
			pc.Assertf(flags.FlagsUp(FlagAttachedVertexInputsSolid), "MarkVertexDefinedDoNotEnforceRootedCheck: flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s",
				vid.IDShortString, flags.String)
			pc.Assertf(flags.FlagsUp(FlagAttachedVertexEndorsementsSolid), "MarkVertexDefinedDoNotEnforceRootedCheck: flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s",
				vid.IDShortString, flags.String)
		}
	}
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown|FlagAttachedVertexDefined)
}

// MustMarkVertexInTheState vertex becomes 'known' and marked Rooted and 'defined'
func (pc *PastCone) MustMarkVertexInTheState(vid *WrappedTx) {
	if !pc.IsKnown(vid) {
		pc.mustReference(vid)
	}
	util.Assertf(!pc.IsNotInTheState(vid), "!pc.IsNotInTheState(vid)")
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown|FlagAttachedVertexCheckedInTheState|FlagAttachedVertexDefined|FlagAttachedVertexInTheState)
	pc.Assertf(pc.IsKnownInTheState(vid), "pc.IsNotInTheState(vid)")
}

// MustMarkVertexNotInTheState is marked definitely not rooted
func (pc *PastCone) MustMarkVertexNotInTheState(vid *WrappedTx) {
	pc.Assertf(!pc.IsKnownInTheState(vid), "!pc.IsKnownInTheState(vid)")
	if !pc.IsKnown(vid) {
		pc.mustReference(vid)
	}
	pc.SetFlagsUp(vid, FlagAttachedVertexKnown|FlagAttachedVertexCheckedInTheState)
	pc.Assertf(pc.IsNotInTheState(vid), "pc.IsNotInTheState(vid)")
}

func (pc *PastCone) ContainsUndefinedExcept(except *WrappedTx) bool {
	util.Assertf(pc.delta == nil, "pc.delta==nil")
	for vid, flags := range pc.vertices {
		if !flags.FlagsUp(FlagAttachedVertexDefined) && vid != except {
			return true
		}
	}
	return false
}

func (pc *PastCone) CalculateSlotInflation() (ret uint64) {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	for vid := range pc.vertices {
		if pc.IsKnownInTheState(vid) && vid.IsSequencerMilestone() {
			ret += vid.InflationAmountOfSequencerMilestone()
		}
	}
	return
}

func (pc *PastCone) Lines(prefix ...string) *lines.Lines {
	pc.Assertf(pc.delta == nil, "pc.delta==nil")
	return pc.PastConeBase.Lines(prefix...)
}

func (pb *PastConeBase) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("------ baseline: %s", util.Cond(pb.baseline == nil, "<nil>", pb.baseline.IDShortString()))
	sorted := util.KeysSorted(pb.vertices, func(vid1, vid2 *WrappedTx) bool {
		return vid1.Before(vid2)
	})
	for i, vid := range sorted {
		ret.Add("#%d %s : %s", i, vid.IDShortString(), pb.vertices[vid].String())
	}
	return ret
}

func (pc *PastCone) UndefinedList() []*WrappedTx {
	pc.Assertf(pc.delta == nil, "pc.delta==nil")

	ret := make([]*WrappedTx, 0)
	for vid, flags := range pc.vertices {
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

func (pc *PastCone) NumVertices() int {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	return len(pc.vertices)
}

func (pc *PastCone) findConsumerOf(wOut WrappedOutput) (ret *WrappedTx) {
	if !pc.IsKnown(wOut.VID) {
		return nil
	}

	wOut.VID.mutexDescendants.RLock()
	defer wOut.VID.mutexDescendants.RUnlock()

	consumers, found := wOut.VID.consumed[wOut.Index]
	if !found {
		return nil
	}

	return pc._findConsumer(consumers)
}

// _findConsumer selects 0 or 1 consumer from the set which is known in the past cone
// It panics if there's more than one consumer (double spends are not allowed in the past cone),
func (pc *PastCone) _findConsumer(consumers set.Set[*WrappedTx]) (ret *WrappedTx) {
	for vid := range consumers {
		if pc.IsKnown(vid) {
			pc.Assertf(ret == nil, "inconsistency: double-spend in the past cone %s", pc.name)
			ret = vid
		}
	}
	return
}

func (pc *PastCone) outputIndices(vid *WrappedTx) (consumedIndices, notConsumedIndices []byte) {
	numProduced := vid.NumProducedOutputs()
	pc.Assertf(numProduced > 0, "numProduced > 0")
	consumedIndices, notConsumedIndices = make([]byte, 0, numProduced), make([]byte, 0, numProduced)

	vid.mutexDescendants.RLock()
	defer vid.mutexDescendants.RUnlock()

	for idx := 0; idx < numProduced; idx++ {
		if consumers, isConsumed := vid.consumed[byte(idx)]; isConsumed {
			if pc._findConsumer(consumers) != nil {
				consumedIndices = append(consumedIndices, byte(idx))
			} else {
				notConsumedIndices = append(notConsumedIndices, byte(idx))
			}
		} else {
			notConsumedIndices = append(notConsumedIndices, byte(idx))
		}
	}
	if len(consumedIndices) == 0 {
		consumedIndices = nil
	}
	if len(notConsumedIndices) == 0 {
		notConsumedIndices = nil
	}
	pc.Assertf(len(notConsumedIndices)+len(consumedIndices) == numProduced, "len(notConsumedIndices)+len(consumedIndices) == numProduced")
	return
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
		consumedIndices, notConsumedIndices := pc.outputIndices(vid)
		if pc.IsKnownInTheState(vid) {
			// generate DEL mutations
			for _, idx := range consumedIndices {
				if pc.IsRootedOutput(WrappedOutput{VID: vid, Index: idx}) {
					muts.InsertDelOutputMutation(vid.OutputID(idx))
					stats.NumDeleted++
				}
			}
		} else {
			muts.InsertAddTxMutation(vid.ID, slot, byte(len(consumedIndices)+len(notConsumedIndices)-1))
			stats.NumTransactions++

			// ADD OUTPUT mutations only for not consumed outputs
			for _, idx := range notConsumedIndices {
				muts.InsertAddOutputMutation(vid.OutputID(idx), vid.MustOutputAt(idx))
				stats.NumCreated++
			}
		}
	}
	return
}

func (pc *PastCone) IsRootedOutput(wOut WrappedOutput) bool {
	if pc.IsNotInTheState(wOut.VID) {
		return false
	}
	consumer := pc.findConsumerOf(wOut)
	return consumer != nil && pc.IsNotInTheState(consumer)
}

func (pc *PastCone) hasRooted() bool {
	for _, flags := range pc.vertices {
		if flags.FlagsUp(FlagAttachedVertexInTheState) {
			return true
		}
	}
	return false
}

func (pc *PastCone) IsComplete() bool {
	return pc.delta == nil && !pc.ContainsUndefinedExcept(nil) && pc.hasRooted()
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

// AppendPastCone appends deterministic past cone to the current one. Returns conflict info if any
func (pc *PastCone) AppendPastCone(pcb *PastConeBase, getStateReader func(branch *WrappedTx) global.IndexedStateReader) (*WrappedOutput, uint64) {
	baseline := pc.getBaseline()
	pc.Assertf(baseline != nil, "pc.hasBaseline()")
	pc.Assertf(pcb.baseline != nil, "pcb.baseline != nil")

	// we require baselines must be compatible (on the same chain) of pcb should not be younger than pc
	if !baseline.IsContainingBranchOf(pcb.baseline, getStateReader) {
		// baselines not compatible
		return &WrappedOutput{}, 0 // >>>>>>>>>>>>>>>> conflict
	}

	coverageDelta := uint64(0)
	baselineStateReader := getStateReader(baseline)

	// pcb must be deterministic, i.e. immutable and all vertices in it must be 'known defined'
	// it does not need locking anymore
	for vid, flags := range pcb.vertices {
		if pc.IsKnown(vid) {
			// it already exists in the target past cone. Check for conflicts
			if conflict := pc.checkConflicts(vid, pcb); conflict != nil {
				// past cones are conflicting
				return conflict, 0 //>>>>>>>>>>>>>>>>>>>>> conflict/double spend
			}
		}
		pc.MustMarkVertexWithFlags(vid, flags)
		if !pc.IsKnownInTheState(vid) && baselineStateReader.KnowsCommittedTransaction(&vid.ID) {
			pc.SetFlagsUp(vid, FlagAttachedVertexCheckedInTheState|FlagAttachedVertexInTheState)
		}
	}
	return nil, coverageDelta
}

func (pc *PastCone) checkConflicts(vid *WrappedTx, pcb *PastConeBase) *WrappedOutput {
	vid.mutexDescendants.RLock()
	defer vid.mutexDescendants.RUnlock()

	for outputIndex, consumers := range vid.consumed {
		var consumerInPastCone *WrappedTx
		for consumer := range consumers {
			_, consumed := pcb.vertices[consumer]
			if consumed || pc.IsKnown(consumer) {
				if consumerInPastCone == nil {
					consumerInPastCone = consumer
				} else {
					if consumerInPastCone != consumer {
						// it is consumed in both past cones by different transactions -> conflict/double spend
						return &WrappedOutput{VID: vid, Index: outputIndex}
					}
				}
			}
		}
	}
	return nil
}

// CheckFinalPastCone check determinism consistency of the past cone
// If rootVid == nil, past cone must be fully deterministic
func (pc *PastCone) CheckFinalPastCone() (err error) {
	if pc.delta != nil {
		return fmt.Errorf("CheckFinalPastCone: past cone has uncommitted delta")
	}
	if pc.ContainsUndefinedExcept(nil) {
		return fmt.Errorf("CheckFinalPastCone: still contains undefined Vertices")
	}

	// should be at least one Rooted output ( ledger baselineCoverage must be > 0)
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
		if pc.IsKnownInTheState(vid) {
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

func (pc *PastCone) checkFinalFlags(vid *WrappedTx) error {
	flags := pc.Flags(vid)
	wrongFlag := ""

	pc.Assertf(pc.baseline != nil, "checkFinalFlags: pc.baseline != nil")

	switch {
	case !flags.FlagsUp(FlagAttachedVertexKnown):
		wrongFlag = "FlagAttachedVertexKnown"
	case !flags.FlagsUp(FlagAttachedVertexDefined):
		wrongFlag = "FlagAttachedVertexDefined"
	case flags.FlagsUp(FlagAttachedVertexInTheState):
		if !flags.FlagsUp(FlagAttachedVertexCheckedInTheState) {
			wrongFlag = "FlagAttachedVertexCheckedInTheState"
		}
	case vid.IsBranchTransaction():
		switch {
		case pc.baseline == vid:
			return fmt.Errorf("checkFinalFlags: must be baseline")
		case flags.FlagsUp(FlagAttachedVertexCheckedInTheState):
			wrongFlag = "FlagAttachedVertexCheckedInTheState"
		}
	default:
		switch {
		case !flags.FlagsUp(FlagAttachedVertexInputsSolid):
			wrongFlag = "FlagAttachedVertexEndorsementsSolid"
		case !flags.FlagsUp(FlagAttachedVertexEndorsementsSolid):
			wrongFlag = "FlagAttachedVertexEndorsementsSolid"
		}
	}
	if wrongFlag != "" {
		return fmt.Errorf("checkFinalFlags: wrong %s flag  %08b in %s", wrongFlag, flags, vid.IDShortString())
	}
	return nil
}
