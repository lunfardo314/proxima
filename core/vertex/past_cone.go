package vertex

import (
	"fmt"
	"slices"
	"sort"

	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/set256"
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
		global.Logging // TODO not very necessary
		tip            *WrappedTx
		txTs           base.LedgerTime
		name           string

		*PastConeBase
		delta *PastConeBase
	}

	PastConeBase struct {
		baselineBranchID  *base.TransactionID
		vertices          map[*WrappedTx]FlagsPastCone // byte is used by attacher for flags
		virtuallyConsumed map[*WrappedTx]set.Set[byte]
		attachmentCost    int
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
	FlagPastConeDirectCost              = FlagsPastCone(0b10000000) // vertex contributes to direct attachment cost (not merged from other past cones)
)

func (f FlagsPastCone) FlagsUp(fl FlagsPastCone) bool {
	return f&fl == fl
}

func (f FlagsPastCone) String() string {
	return fmt.Sprintf("%08b known: %v, defined: %v, inTheState: (%v,%v), endorsementsOk: %v, inputsOk: %v, poke: %v, directCost: %v",
		f,
		f.FlagsUp(FlagPastConeVertexKnown),
		f.FlagsUp(FlagPastConeVertexDefined),
		f.FlagsUp(FlagPastConeVertexCheckedInTheState),
		f.FlagsUp(FlagPastConeVertexInTheState),
		f.FlagsUp(FlagPastConeVertexEndorsementsSolid),
		f.FlagsUp(FlagPastConeVertexInputsSolid),
		f.FlagsUp(FlagPastConeVertexAskedForPoke),
		f.FlagsUp(FlagPastConeDirectCost),
	)
}

// we are using sync.Pool for heap optimization

func NewPastConeBase(baselineID *base.TransactionID) *PastConeBase {
	ret := &PastConeBase{
		vertices:         make(map[*WrappedTx]FlagsPastCone),
		baselineBranchID: baselineID,
	}
	return ret
}

func NewPastCone(env global.Logging, tip *WrappedTx, txTs base.LedgerTime, name string) *PastCone {
	return newPastConeFromBase(env, tip, txTs, name, NewPastConeBase(nil))
}

func newPastConeFromBase(env global.Logging, tip *WrappedTx, targetTs base.LedgerTime, name string, pb *PastConeBase) *PastCone {
	return &PastCone{
		Logging:      env,
		tip:          tip,
		txTs:         targetTs,
		name:         name,
		PastConeBase: pb,
	}
}

func (pb *PastConeBase) CloneImmutable() *PastConeBase {
	util.Assertf(len(pb.virtuallyConsumed) == 0, "len(pb.virtuallyConsumed)==0")

	ret := &PastConeBase{
		baselineBranchID: pb.baselineBranchID,
		vertices:         make(map[*WrappedTx]FlagsPastCone, len(pb.vertices)),
	}
	for vid, flags := range pb.vertices {
		ret.vertices[vid] = flags
	}
	return ret
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

func (pb *PastConeBase) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	if pb == nil {
		ret.Add("<nil pastCone>")
		return ret
	}
	ret.Add("baseline: %s", pb.baselineBranchID.String())
	for vid := range pb.vertices {
		ret.Add("  dept %s", vid.IDShortString())
	}
	for vid := range pb.virtuallyConsumed {
		ret.Add("  virt %s", vid.IDShortString())
	}
	return ret
}

func (pb *PastConeBase) Dispose() {
	if pb == nil {
		return
	}
	pb.baselineBranchID = nil
	clear(pb.vertices)
	pb.vertices = nil
	clear(pb.virtuallyConsumed)
	pb.virtuallyConsumed = nil
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

func (pb *PastConeBase) Len() int {
	return len(pb.vertices)
}

// AttachmentCost is sum of attachment costs of all non-sequencer vertices that ar definitely not in the state
func (pc *PastCone) AttachmentCost() (ret int) {
	if pc.delta == nil {
		return pc.attachmentCost
	}
	return pc.attachmentCost + pc.delta.attachmentCost
}

func (pc *PastCone) addToAttachmentCost(delta int) {
	if pc.delta != nil {
		pc.delta.attachmentCost += delta
	} else {
		pc.attachmentCost += delta
	}
}

// AttachmentCostDirect calculates attachment cost by iterating vertices with FlagPastConeDirectCost.
// Only vertices that were directly added (not merged from other past cones) contribute to the cost.
func (pc *PastCone) AttachmentCostDirect() (ret int) {
	pc.forAllVertices(func(vid *WrappedTx) bool {
		if pc.Flags(vid).FlagsUp(FlagPastConeDirectCost) {
			ret += vid.AttachmentCost()
		}
		return true
	})
	return
}

func (pc *PastCone) AddVirtuallyConsumedOutput(wOut WrappedOutput, getStateReader func(branchID base.TransactionID) multistate.StateReader) *WrappedOutput {
	if pc.delta == nil {
		pc.addVirtuallyConsumedOutput(wOut)
		return pc.CheckConflicts(getStateReader)
	}
	if pc.isVirtuallyConsumed(wOut) {
		return nil
	}
	pc.delta.addVirtuallyConsumedOutput(wOut)
	return pc.CheckConflicts(getStateReader)
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

func (pc *PastCone) Assertf(cond bool, format string, args ...any) {
	if cond {
		return
	}
	pcStr := pc.LinesShort("      ").Join("\n")
	argsExt := append(slices.Clone(args), pcStr)
	pc.Logging.Assertf(cond, format+"\n---- past cone ----\n%s", argsExt...)
}

func (pc *PastCone) SetBaseline(baselineID *base.TransactionID) {
	pc.Assertf(baselineID.IsBranchTransaction(), "branch tx expected in past cone %s, got %s", pc.name, baselineID.StringShort)

	if pc.delta == nil {
		pc.baselineBranchID = baselineID
	} else {
		pc.delta.baselineBranchID = baselineID
	}
}

func (pc *PastCone) GetBaseline() *base.TransactionID {
	if pc.baselineBranchID != nil {
		return pc.baselineBranchID
	}
	if pc.delta != nil {
		return pc.delta.baselineBranchID
	}
	return nil
}

func (pc *PastCone) BeginDelta() {
	util.Assertf(pc.delta == nil, "BeginDelta: pc.delta == nil")
	pc.delta = NewPastConeBase(pc.baselineBranchID)
}

func (pc *PastCone) CommitDelta() {
	util.Assertf(pc.delta != nil, "CommitDelta: pc.delta != nil")

	pc.baselineBranchID = pc.delta.baselineBranchID
	for vid, flags := range pc.delta.vertices {
		pc.vertices[vid] = flags
	}
	for vid, consumedIndices := range pc.delta.virtuallyConsumed {
		for idx := range consumedIndices {
			pc.addVirtuallyConsumedOutput(WrappedOutput{VID: vid, Index: idx})
		}
	}
	pc.attachmentCost += pc.delta.attachmentCost
	pc.delta = nil
}

func (pc *PastCone) RollbackDelta() {
	if pc.delta == nil {
		return
	}
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

func (pc *PastCone) MarkVertexKnown(vid *WrappedTx) {
	pc.SetFlagsUp(vid, FlagPastConeVertexKnown)
}

func (pc *PastCone) markVertexWithFlags(vid *WrappedTx, flags FlagsPastCone) {
	pc.SetFlagsUp(vid, flags)
}

// MustMarkVertexNotInTheState is marked definitely not rooted
// attachmentCost increased for non-sequencer transactions
// FlagPastConeDirectCost is set for non-sequencer transactions to mark them as directly contributing to cost
func (pc *PastCone) MustMarkVertexNotInTheState(vid *WrappedTx) {
	pc.Assertf(!pc.IsInTheState(vid), "!pc.IsInTheState(vid)")
	pc.SetFlagsUp(vid, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState)
	pc.Assertf(pc.isNotInTheState(vid), "pc.isNotInTheState(vid)")
	if !vid.IsSequencerTransaction() {
		pc.addToAttachmentCost(vid.AttachmentCost())
		pc.SetFlagsUp(vid, FlagPastConeDirectCost)
	}
}

func (pc *PastCone) ContainsUndefined() bool {
	util.Assertf(pc.delta == nil, "pc.delta==nil")
	util.Assertf(pc.baselineBranchID != nil, "pc.baselineBranchID != nil")
	for vid, flags := range pc.vertices {
		if vid == pc.tip {
			continue
		}
		if *pc.baselineBranchID == vid.ID() {
			continue
		}
		if !flags.FlagsUp(FlagPastConeVertexDefined) {
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
		Add("------ baseline: %s", pc.baselineBranchID.StringShort()).
		Add("------ tip: %s", pc.tip.IDShortString())

	counter := 0
	pc.forAllVertices(func(vid *WrappedTx) bool {
		ret.Add("#%d %s", counter, pc.VertexLine(vid))
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

func (pc *PastCone) VertexLine(vid *WrappedTx) string {
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
	return fmt.Sprintf("S%s %s consumers: {%s} flags: %s", stateStr, vid.IDShortString(), lnOut.Join(", "), pc.Flags(vid).String())
}

func (pc *PastCone) LinesShort(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("------ past cone: '%s'", pc.name).
		Add("------ baseline: %s", pc.baselineBranchID.StringShort())
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

func (pc *PastCone) consumedUTXOIndices(vid *WrappedTx) []byte {
	return maps.Keys(pc.consumersByOutputIndex(vid))
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

func (pc *PastCone) Mutations() (muts *multistate.Mutations, stats MutationStats, txs []base.TransactionID) {
	muts = multistate.NewMutations()
	txs = make([]base.TransactionID, 0)

	// need to handle discontinued chains
	deletedChainIDs := set.New[base.ChainID]()
	producedChainIDs := set.New[base.ChainID]()

	// generate ADD TX and ADD OUTPUT mutations
	for vid := range pc.vertices {
		if pc.IsInTheState(vid) {
			// generate DEL mutations
			for idx, consumersOfRooted := range pc.consumersByOutputIndex(vid) {
				pc.Assertf(len(consumersOfRooted) == 1, "Mutations: len(consumersOfRooted)==1")

				if pc.isNotInTheState(consumersOfRooted[0]) {
					oid := vid.OutputID(idx)
					o := vid.MustOutputAt(idx)
					if cc := o.ChainConstraint(); cc != nil {
						chainID := cc.ChainID
						if cc.IsOrigin() {
							chainID = base.MakeOriginChainID(oid)
						}
						// chain output deleted
						deletedChainIDs.Insert(chainID)
					}
					muts.InsertDelOutputMutation(oid)
					stats.NumDeleted++
				}
			}
		} else {
			produced := pc.producedIndices(vid)
			var unspent set256.Set256
			unspent.InsertAll(produced...)
			muts.InsertAddTxMutation(vid.id, unspent)
			stats.NumTransactions++
			txs = append(txs, vid.id)

			// ADD OUTPUT mutations only for not consumed outputs
			for _, idx := range produced {
				o := vid.MustOutputAt(idx)
				oid := vid.OutputID(idx)
				muts.InsertAddOutputMutation(oid, o)
				stats.NumCreated++

				if cc := o.ChainConstraint(); cc != nil {
					chainID := cc.ChainID
					if cc.IsOrigin() {
						chainID = base.MakeOriginChainID(oid)
					}
					producedChainIDs.Insert(chainID)
				}
			}
		}
	}
	// add delchain mutations for those chain IDs which were deleted and wasn't produced
	for chainID := range producedChainIDs {
		deletedChainIDs.Remove(chainID)
	}
	for chainID := range deletedChainIDs {
		muts.InsertDelChainMutation(chainID)
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

// MergePastCone checks the compatibility of baselines and swaps them if necessary.
// Does not check for double-spends
func (pc *PastCone) MergePastCone(pcb *PastConeBase, br *branches.Branches) bool {
	if len(pcb.vertices) == 0 {
		return true
	}
	currentBaseline := pc.GetBaseline()
	pc.Assertf(currentBaseline != nil && pcb.baselineBranchID != nil, "pc.GetBaseline() != nil && pcb.baselineBranchID != nil")

	compatible, needsBaselineSwap := br.IsDescendantBranch(*pcb.baselineBranchID, *currentBaseline)
	if !compatible {
		return false
	}
	if needsBaselineSwap {
		pc.SetBaseline(pcb.baselineBranchID)
		old := *currentBaseline
		// must set the old baseline is in checked and in the state and defined
		pc.forAllVertices(func(vid *WrappedTx) bool {
			if vid.ID() == old {
				pc.SetFlagsUp(vid, FlagPastConeVertexCheckedInTheState|FlagPastConeVertexInTheState|FlagPastConeVertexDefined)
				return false
			}
			return true
		})
	}
	for vid, flags := range pcb.vertices {
		if vid.ID() != *pcb.baselineBranchID {
			pc.Assertf(flags.FlagsUp(FlagPastConeVertexKnown|FlagPastConeVertexDefined), "inconsistent flag in merged past cone: %s\n%s\n%s",
				flags.String, vid.IDShortString, func() string { return pcb.Lines("    ").String() })
		}
		if !flags.FlagsUp(FlagPastConeVertexInTheState) {
			// if vertex is in the state of the appended past cone, it will be in the state of the new baseline
			// When vertex not in appended baseline, check if it didn't become known in the new one
			if br.BranchKnowsTransaction(*pc.baselineBranchID, vid.id) {
				flags |= FlagPastConeVertexCheckedInTheState | FlagPastConeVertexInTheState
			}
		}
		// it will also create a new entry in the target past cone if necessary
		// FlagPastConeDirectCost is masked out: merged transactions don't contribute to direct attachment cost
		// (they were already accounted for in the source attacher's cost)
		pc.markVertexWithFlags(vid, flags & ^FlagPastConeVertexAskedForPoke & ^FlagPastConeDirectCost)
	}
	return true
}

// CheckFinalPastCone check determinism consistency of the past cone
// If rootVid == nil, past cone must be fully deterministic
func (pc *PastCone) CheckFinalPastCone(getStateReader func(branchID base.TransactionID) multistate.StateReader) (err error) {
	if pc.delta != nil {
		return fmt.Errorf("CheckFinalPastCone: past cone has uncommitted delta")
	}
	if pc.ContainsUndefined() {
		return fmt.Errorf("CheckFinalPastCone: still contains undefined Vertices")
	}

	// should be at least one 'rooted' output (ledger baselineCoverage must be > 0)
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
			// do not check dependencies if the transaction is rooted
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
	if conflict := pc.CheckConflicts(getStateReader); conflict != nil {
		return fmt.Errorf("past cone %s contains double-spent output %s", pc.name, conflict.IDStringShort())
	}
	return nil
}

func (pc *PastCone) checkFinalFlags(vid *WrappedTx) error {
	util.Assertf(pc.baselineBranchID != nil, "checkFinalFlags: pc.baseline != nil")
	if vid.ID() == *pc.baselineBranchID {
		return nil
	}

	flags := pc.Flags(vid)
	wrongFlag := ""

	pc.Assertf(pc.baselineBranchID != nil, "checkFinalFlags: pc.baseline != nil")

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
		// A non-baseline branch can legitimately appear in the past cone when a transaction
		// in the cone consumes an output from a competing branch at the same slot.
		// This is normal during multi-sequencer operation with concurrent forks.
		// Only flag it as inconsistent if there's no baseline at all and the branch isn't the tip.
		if pc.baselineBranchID == nil {
			if vid.ID() != pc.tip.ID() {
				return fmt.Errorf("checkFinalFlags: inconsistent baseline 1 %s", vid.IDShortString())
			}
		}
	default:
		switch {
		case !flags.FlagsUp(FlagPastConeVertexInputsSolid):
			wrongFlag = "FlagPastConeVertexInputsSolid"
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
	ret := NewPastCone(env, pc.tip, pc.txTs, name+"_debug_clone")
	ret.baselineBranchID = pc.baselineBranchID
	ret.vertices = maps.Clone(pc.vertices)
	ret.virtuallyConsumed = make(map[*WrappedTx]set.Set[byte])
	for vid, consumedIndices := range pc.virtuallyConsumed {
		ret.virtuallyConsumed[vid] = consumedIndices.Clone()
	}
	return ret
}

// CheckConflicts returns double-spent output (conflict) or nil if the past cone is consistent
// The complexity is O(NxM) where N is number of vertices and M is an average number of conflicts in the UTXO tangle
// Practically, it is linear wrt the number of vertices because M is 1 or close to 1.
func (pc *PastCone) CheckConflicts(getStateReader func(branchID base.TransactionID) multistate.StateReader) (conflict *WrappedOutput) {
	rdr := getStateReader(*pc.GetBaseline())
	pc.forAllVertices(func(vid *WrappedTx) bool {
		conflict, _ = pc._checkVertex(vid, rdr)
		return conflict == nil
	})
	return
}

// CheckAndClean iterates past cone, checks for conflicts and removes those vertices
// that have consumers and all consumers are already in the state
func (pc *PastCone) CheckAndClean(getStateReader func(branchID base.TransactionID) multistate.StateReader) (conflict *WrappedOutput) {
	pc.Assertf(pc.baselineBranchID != nil, "pc.baseline!=nil")
	pc.Assertf(len(pc.virtuallyConsumed) == 0, "len(pb.virtuallyConsumed)==0")
	pc.Assertf(pc.delta == nil, "pc.delta == nil")

	var canBeRemoved bool

	rdr := getStateReader(*pc.GetBaseline())
	for vid, flags := range pc.vertices {
		if vid != pc.tip && vid.ID() != *pc.baselineBranchID {
			pc.Assertf(flags.FlagsUp(FlagPastConeVertexKnown|FlagPastConeVertexDefined|FlagPastConeVertexCheckedInTheState), "wrong flag in %s", vid.IDShortString)
		}
		conflict, canBeRemoved = pc._checkVertex(vid, rdr)
		if conflict != nil {
			return
		}
		if canBeRemoved {
			delete(pc.vertices, vid)
		}
	}
	return
}

func (pc *PastCone) _checkVertex(vid *WrappedTx, stateReader multistate.StateReader) (doubleSpend *WrappedOutput, canBeRemoved bool) {
	allConsumersAreInTheState := true
	inTheState := pc.IsInTheState(vid)
	byIdx := pc.consumersByOutputIndex(vid)
	for idx, consumers := range byIdx {
		wOut := WrappedOutput{VID: vid, Index: idx}
		pc.Assertf(len(consumers) > 0, "len(consumers) > 0")
		if len(consumers) != 1 {
			return &wOut, false
		}
		if pc.IsInTheState(consumers[0]) {
			continue
		}
		// virtual consumer nil is never in the state
		allConsumersAreInTheState = false
		if inTheState && !stateReader.HasUTXO(wOut.DecodeID()) {
			return &wOut, false
		}
	}
	canBeRemoved = len(byIdx) > 0 && allConsumersAreInTheState
	return
}

func (pc *PastCone) SlotInflation() (ret uint64) {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	for vid := range pc.vertices {
		if pc.isNotInTheState(vid) {
			ret += vid.InflationAmount()
		}
	}
	return
}

// CoverageDeltaRaw is not adjusted for sequencer output. Function does not check the consistency of the past cone.
// Calculates coverage by checking them right in the state. For chained outputs adds non-frozen coverage .
// Accounts for the frozen coverage in sequencer outputs.
// Returns:
// - total coverage delta
// - frozen coverage (included in the delta)
func (pc *PastCone) CoverageDeltaRaw(getStateReader func(branchID base.TransactionID) multistate.StateReader) (delta, frozen uint64) {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	pc.Assertf(pc.baselineBranchID != nil, "pc.baseline != nil")

	rdr := getStateReader(*pc.GetBaseline())
	for vid := range pc.vertices {
		for _, idx := range pc.consumedUTXOIndices(vid) {
			oid := vid.OutputID(idx)
			if o := multistate.GetOutputFromStateReader(rdr, oid); o != nil {
				cov, fr := ledger.Coverage(o, oid, pc.txTs)
				delta += cov
				frozen += fr
			}
		}
	}
	return
}

func (pc *PastCone) IsConsumed(wOut WrappedOutput) bool {
	return len(pc.findConsumersOf(wOut)) > 0
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

func (pc *PastCone) Dispose() {
	if pc == nil {
		return
	}
	pc.tip = nil
	pc.PastConeBase.Dispose()
	pc.PastConeBase = nil
	if pc.delta != nil {
		pc.delta.Dispose()
	}
	pc.delta = nil
}
