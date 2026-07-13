package vertex

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
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

// TraceTagPastConeDiag toggles diagnostic cross-checks at conflict-detection and merge
// boundaries. When enabled, the past-cone subsystem emits structured logs distinguishing
// stale-S+ flags, TTL-blessed txs, and GC-stranded consumers. See claude/pastcone_consistency.md.
const TraceTagPastConeDiag = "past_cone_diag"

type (
	FlagsPastCone byte

	PastCone struct {
		global.Logging // TODO not very necessary
		tip            *WrappedTx
		txTs           base.LedgerTime
		name           string

		// diagBranches, when non-nil, enables runtime consistency cross-checks for this
		// past cone. Set via SetDiagBranches. Diagnostic output is gated by trace tag
		// TraceTagPastConeDiag so the hot paths stay free when the tag is off.
		diagBranches *branches.Branches

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
	FlagPastConeVertexAskedForPoke      = FlagsPastCone(0b01000000) //
	FlagPastConeDirectCost              = FlagsPastCone(0b10000000) // vertex contributes to direct attachment cost (not merged from other past cones)
)

func (f FlagsPastCone) FlagsUp(fl FlagsPastCone) bool {
	return f&fl == fl
}

func (f FlagsPastCone) String() string {
	return fmt.Sprintf("%08b known: %v, defined: %v, inTheState: (%v,%v), poke: %v, directCost: %v",
		f,
		f.FlagsUp(FlagPastConeVertexKnown),
		f.FlagsUp(FlagPastConeVertexDefined),
		f.FlagsUp(FlagPastConeVertexCheckedInTheState),
		f.FlagsUp(FlagPastConeVertexInTheState),
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

// Clone creates a deep copy of PastConeBase including virtuallyConsumed state.
func (pb *PastConeBase) Clone() *PastConeBase {
	ret := &PastConeBase{
		baselineBranchID: pb.baselineBranchID,
		vertices:         make(map[*WrappedTx]FlagsPastCone, len(pb.vertices)),
		attachmentCost:   pb.attachmentCost,
	}
	for vid, flags := range pb.vertices {
		ret.vertices[vid] = flags
	}
	if len(pb.virtuallyConsumed) > 0 {
		ret.virtuallyConsumed = make(map[*WrappedTx]set.Set[byte], len(pb.virtuallyConsumed))
		for vid, indices := range pb.virtuallyConsumed {
			ret.virtuallyConsumed[vid] = indices.Clone()
		}
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

// VertexSet returns the set of all vertices in the past cone.
// Used to track which vertices are confirmed in a branch for fine-grained pruning.
func (pb *PastConeBase) VertexSet() set.Set[*WrappedTx] {
	return set.NewFromKeys(pb.vertices)
}

// CommittedVertexSet returns only the not-rooted (newly-committed) vertices of the past
// cone — the delta this branch adds to the state — excluding the inherited rooted boundary.
// These match Mutations()' committedTxs (the not-in-state branch of that iteration).
//
// Used for branchVertices pruning (RegisterBranchVertices) so each vertex is tracked under
// the branch that first commits it and is pruned when that branch ages out. Registering the
// full VertexSet instead re-registered the inherited rooted boundary under every successor
// branch, so old vertices were perpetually refreshed into recent branches, never became
// branchPruneDepth-deep, and were never reclaimed — the unbounded memDAG leak (oldestSlot
// frozen at the restart slot). The inherited boundary is pruned by wall-clock TTL instead.
func (pb *PastConeBase) CommittedVertexSet() set.Set[*WrappedTx] {
	ret := set.New[*WrappedTx]()
	for vid, flags := range pb.vertices {
		if !flags.FlagsUp(FlagPastConeVertexInTheState) {
			ret.Insert(vid)
		}
	}
	return ret
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

func (pc *PastCone) AddVirtuallyConsumedOutput(ctx context.Context, wOut WrappedOutput, getStateReader func(branchID base.TransactionID) multistate.StateReader) (*WrappedOutput, error) {
	if pc.delta == nil {
		pc.addVirtuallyConsumedOutput(wOut)
		return pc.CheckConflicts(ctx, getStateReader)
	}
	if pc.isVirtuallyConsumed(wOut) {
		return nil, nil
	}
	pc.delta.addVirtuallyConsumedOutput(wOut)
	return pc.CheckConflicts(ctx, getStateReader)
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

// SetDiagBranches opts this PastCone into runtime diagnostic cross-checks. When set, conflict
// detection, merge boundaries and TTL-bless paths cross-reference flags against the live
// Branches index and emit structured Tracef logs under TraceTagPastConeDiag. Safe to pass
// nil to disable. Called once by the attacher at construction.
func (pc *PastCone) SetDiagBranches(br *branches.Branches) {
	pc.diagBranches = br
}

// baselineKnowsTx delegates to the live Branches index to answer "is txid in the
// committed state of pc.baselineBranchID?". Returns false if the past cone was
// constructed without a Branches reference (primarily test scaffolding) or has no
// baseline set. Used both by diagnostics and by the stale-S- safety net in _checkVertex.
func (pc *PastCone) baselineKnowsTx(txid base.TransactionID) bool {
	if pc.diagBranches == nil || pc.baselineBranchID == nil {
		return false
	}
	return pc.diagBranches.BranchKnowsTransaction(*pc.baselineBranchID, txid)
}

// diagLogSuspectConflict is called from _checkVertex just before returning BAD for
// the "inTheState && !HasUTXO" case. With the safety net in _checkVertex consuming
// the stale-S--on-consumer class upstream, this hook now classifies the remaining
// cases: stale-S+ on vid, real fork (state has a different consumer), and the
// inconsistent combination. See claude/pastcone_consistency.md §6.1.
func (pc *PastCone) diagLogSuspectConflict(vid *WrappedTx, wOut WrappedOutput, pcConsumer *WrappedTx) {
	if pc.diagBranches == nil || pc.baselineBranchID == nil {
		return
	}
	baseline := *pc.baselineBranchID
	vidKnown := pc.diagBranches.BranchKnowsTransaction(baseline, vid.ID())
	consumerStr := "<virtual>"
	var consumerKnown bool
	if pcConsumer != nil {
		consumerStr = pcConsumer.IDShortString()
		consumerKnown = pc.diagBranches.BranchKnowsTransaction(baseline, pcConsumer.ID())
	}
	switch {
	case !vidKnown:
		pc.Tracef(TraceTagPastConeDiag, "STALE S+ on vid: pc=%s baseline=%s vid=%s flag=S+ branchKnowsTx=false pcConsumer=%s output=%d",
			pc.name, baseline.StringShort(), vid.IDShortString(), consumerStr, wOut.Index)
	case consumerKnown:
		// Should have been caught by the _checkVertex safety net; reaching here means
		// the safety net is not firing (e.g., Branches wiring missing). Log loudly.
		pc.Tracef(TraceTagPastConeDiag, "STALE S- on consumer (unexpected — safety net should have upgraded): pc=%s baseline=%s vid=%s consumer=%s output=%d",
			pc.name, baseline.StringShort(), vid.IDShortString(), consumerStr, wOut.Index)
	default:
		pc.Tracef(TraceTagPastConeDiag, "REAL conflict: pc=%s baseline=%s vid=%s output=%d pcConsumer=%s (state holds a different consumer or consumer is GC-stranded)",
			pc.name, baseline.StringShort(), vid.IDShortString(), wOut.Index, consumerStr)
	}
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

// Clone creates an independent deep copy of the PastCone.
// Must be called with no pending delta (asserted).
// Vertex pointers (WrappedTx) are shared — only the mutable tracking state is copied.
func (pc *PastCone) Clone(name string) *PastCone {
	util.Assertf(pc.delta == nil, "PastCone.Clone: no pending delta allowed")

	return &PastCone{
		Logging:      pc.Logging,
		tip:          pc.tip,
		txTs:         pc.txTs,
		name:         name,
		PastConeBase: pc.PastConeBase.Clone(),
	}
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

// MarkVertexNotInTheState marks the vertex as checked and not in the baseline state.
// The result is provisional: it may be upgraded to in-the-state later if a re-check
// against a newer baseline finds the tx (see UpgradeToInTheState).
// Cost tracking is idempotent — guarded by FlagPastConeDirectCost to prevent double-counting.
func (pc *PastCone) MarkVertexNotInTheState(vid *WrappedTx) {
	pc.Assertf(!pc.IsInTheState(vid), "!pc.IsInTheState(vid)")
	pc.SetFlagsUp(vid, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState)
	if !vid.IsSequencerTransaction() && !pc.Flags(vid).FlagsUp(FlagPastConeDirectCost) {
		pc.addToAttachmentCost(vid.AttachmentCost())
		pc.SetFlagsUp(vid, FlagPastConeDirectCost)
	}
}

// UpgradeToInTheState upgrades a vertex to in-the-state. This happens when a
// PastConeBase merge or the _checkVertex safety net finds that the vertex is in
// fact in the current baseline's state, overriding an earlier (or default) view.
// Reverses the attachment cost added by MarkVertexNotInTheState, if any.
//
// Sets CheckedInTheState alongside InTheState — the invariant isVertexInTheState
// asserts is "InTheState ⇒ CheckedInTheState". Callers that use this on a vertex
// that does not already carry CheckedInTheState (e.g. a baseline branch added
// synthetically via _filterConsumingVertices) would otherwise trip that assert
// on the next read.
func (pc *PastCone) UpgradeToInTheState(vid *WrappedTx) {
	pc.SetFlagsUp(vid, FlagPastConeVertexCheckedInTheState|FlagPastConeVertexInTheState|FlagPastConeVertexDefined)
	if pc.Flags(vid).FlagsUp(FlagPastConeDirectCost) {
		pc.addToAttachmentCost(-vid.AttachmentCost())
		pc.SetFlagsDown(vid, FlagPastConeDirectCost)
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
			continue
		}
		// The baseline branch is not in pc.vertices but IS a legitimate consumer
		// of outputs in the past cone (e.g. it consumes the predecessor's stem).
		// Without this, CheckAndClean removes in-state branches whose stem consumer
		// (the baseline) is invisible, stripping conflict evidence from the PastConeBase.
		if pc.baselineBranchID != nil && vid.ID() == *pc.baselineBranchID {
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
	NumConfirmedTransactions int
	NumDeleted      int
	NumCreated      int
	// AmountDeleted/AmountCreated are the token-balance sums of the DEL/ADD output
	// mutations — the same delAmount/addAmount updateTrie checks at commit. Summed here
	// while the outputs are already in hand so the branch aggregate conservation invariant
	// (created == deleted + slotInflation) can be enforced at wrap-up, before the deferred
	// commit, instead of only detonating there.
	AmountDeleted uint64
	AmountCreated uint64
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

				// A nil consumer is a virtual consumption: the sequencer's IncrementalAttacher
				// reserved this rooted output for its milestone-in-construction (see
				// addVirtuallyConsumedOutput). That reservation is by definition a delta consumer
				// not in the state, so the rooted output leaves the UTXO set and MUST emit a DEL —
				// the mirror of producedIndices() excluding a virtually-consumed output from the ADD
				// set. Omitting it left `deleted` short by the output's amount and tripped the
				// wrap-up conservation guard (created != deleted + slotInflation).
				if consumersOfRooted[0] == nil || pc.isNotInTheState(consumersOfRooted[0]) {
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
					stats.AmountDeleted += o.TokenBalance()
				}
			}
		} else {
			// DEBUG: detect orphaned branch in mutations (skip tip)
			if vid.IsBranchTransaction() && vid != pc.tip {
				util.Assertf(pc.baselineBranchID != nil && vid.ID() == *pc.baselineBranchID,
					"ORPHANED BRANCH %s in past cone %s (baseline %s, tip %s)",
					vid.IDShortString(), pc.name, pc.baselineBranchID.StringShort(), pc.tip.IDShortString())
			}
			produced := pc.producedIndices(vid)
			var unspent set256.Set256
			unspent.InsertAll(produced...)
			muts.InsertAddTxMutation(vid.id, unspent)
			stats.NumConfirmedTransactions++
			txs = append(txs, vid.id)

			// ADD OUTPUT mutations only for not consumed outputs
			for _, idx := range produced {
				o := vid.MustOutputAt(idx)
				oid := vid.OutputID(idx)
				muts.InsertAddOutputMutation(oid, o)
				stats.NumCreated++
				stats.AmountCreated += o.TokenBalance()

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

// ConsumerEdgesInWindow returns the first-time consumer edges (from the process-wide instrument)
// recorded in generation window (fromGen, toGen] whose producer or consumer is a member of THIS
// past cone. On the branch conservation failure path it names the edges that a concurrent attacher
// first-registered between CheckAndClean and Mutations — the suspected cause of the transient
// mutation-set non-conservation. Failure-path only.
func (pc *PastCone) ConsumerEdgesInWindow(fromGen, toGen uint64) *lines.Lines {
	coneIDs := set.New[base.TransactionID]()
	for vid := range pc.vertices {
		coneIDs.Insert(vid.ID())
	}
	return dumpConsumerEdgeRing(fromGen, toGen, coneIDs.Contains)
}

// DiagnoseMutationImbalance localizes a branch-delta token-conservation violation
// (created != deleted + slotInflation, see wrapUpAttacher). It reconstructs the DEL set that
// Mutations() generates and compares it against the GROUND TRUTH of what the not-rooted delta
// transactions actually consume: for every input of every delta tx, it resolves the producer and
// classifies it. The conservation invariant breaks by exactly the amount of consumed outputs whose
// rooted producer failed to emit a DEL, so this pinpoints the specific output(s) and reports WHY —
// producer absent from the cone, producer flag not rooted, the consumed-link not visible to
// consumersByOutputIndex, or the input pointer referencing a stale/duplicate WrappedTx that is not
// the cone's instance for that txid. Only called on the failure path, so O(n^2) scans are fine.
//
// resolveCurrent (may be nil) resolves a txid to the CURRENT memDAG registry instance. When given,
// each consumed-link anomaly additionally reports the instance-generation forensic: the pointers of
// the cone's producer instance vs the input's producer ref vs the live registry instance, and
// whether each holds the consumer edge. A cone instance that differs from the registry instance and
// lacks an edge the registry instance now has is the generation-gap fingerprint — a producer that
// was reclaimed and re-minted (empty `consumed`) while its Defined flag survived by merge, so the
// edge was only re-registered later, on the fresh instance, after the branch froze the stale one.
func (pc *PastCone) DiagnoseMutationImbalance(resolveCurrent func(base.TransactionID) *WrappedTx) *lines.Lines {
	ret := lines.New()

	// index the cone by txid so an input's producer can be resolved to the cone's own instance,
	// independent of the (possibly stale) input pointer on the consuming vertex
	byTxID := make(map[base.TransactionID]*WrappedTx, len(pc.vertices))
	for vid := range pc.vertices {
		byTxID[vid.ID()] = vid
	}

	// the DEL set Mutations() actually generates: a rooted output whose single in-cone consumer is
	// definitely-not-in-state. Reconstructed with the exact predicates Mutations() uses.
	actualDel := set.New[base.OutputID]()
	for vid := range pc.vertices {
		if !pc.IsInTheState(vid) {
			continue
		}
		for idx, consumers := range pc.consumersByOutputIndex(vid) {
			if len(consumers) == 1 && pc.isNotInTheState(consumers[0]) {
				actualDel.Insert(vid.OutputID(idx))
			}
		}
	}

	// ground truth: walk every not-rooted (delta) transaction's inputs and classify the producer
	unmatched := uint64(0)
	numAnomalies := 0
	for vid := range pc.vertices {
		if pc.IsInTheState(vid) {
			continue // rooted txs do not consume within the delta
		}
		var tx *transaction.Transaction
		var inputPtrs []*WrappedTx
		vid.RUnwrap(UnwrapOptions{
			Vertex:         func(v *Vertex) { tx = v.Transaction; inputPtrs = v.Inputs },
			DetachedVertex: func(v *DetachedVertex) { tx = v.Transaction },
		})
		if tx == nil {
			continue // virtualTx carries no inputs to check
		}
		for i := 0; i < tx.NumInputs(); i++ {
			oid := tx.MustInputAt(byte(i))
			producerTxID := oid.TransactionID()
			coneProducer := byTxID[producerTxID]
			var refProducer *WrappedTx
			if inputPtrs != nil && i < len(inputPtrs) {
				refProducer = inputPtrs[i]
			}

			report := func(reason string, amount uint64) {
				numAnomalies++
				unmatched += amount
				ret.Add("ANOMALY: consumed %s by %s -> %s (amount %s)",
					oid.StringShort(), vid.IDShortString(), reason, util.Th(amount))
			}
			amountOf := func(p *WrappedTx) uint64 {
				if p == nil {
					return 0
				}
				if o, err := p.OutputAt(oid.Index()); err == nil && o != nil {
					return o.TokenBalance()
				}
				return 0
			}
			// genForensic reports the instance-generation evidence for a consumed-link anomaly:
			// whether the cone's producer instance is the same object the registry now holds, and
			// which of them actually carries the consumer edge. Registry-having-it while cone-lacking-it
			// (and different pointers) is the reclaim-and-re-mint generation gap.
			genForensic := func(tag string, coneProducer, refProducer *WrappedTx) {
				hasByPtr := func(p *WrappedTx) bool { return p != nil && p.ConsumersOf(oid.Index()).Contains(vid) }
				hasByID := func(p *WrappedTx) bool {
					if p == nil {
						return false
					}
					found := false
					p.ConsumersOf(oid.Index()).ForEach(func(c *WrappedTx) bool {
						if c != nil && c.ID() == vid.ID() {
							found = true
							return false
						}
						return true
					})
					return found
				}
				var registry *WrappedTx
				if resolveCurrent != nil {
					registry = resolveCurrent(producerTxID)
				}
				ret.Add("    GEN-FORENSIC [%s] producer %s idx %d consumer %s:", tag, producerTxID.StringShort(), oid.Index(), vid.IDShortString())
				ret.Add("      instances: cone=%p inputRef=%p registry=%p  (cone==registry:%v, inputRef==cone:%v)",
					coneProducer, refProducer, registry, registry == coneProducer, refProducer == coneProducer)
				ret.Add("      edge present in .consumed: cone{byPtr:%v byID:%v} registry{byPtr:%v byID:%v}  curGen=%d",
					hasByPtr(coneProducer), hasByID(coneProducer), hasByPtr(registry), hasByID(registry), ConsumerEdgeGen())
			}

			switch {
			case coneProducer == nil:
				// producer not in the cone at all. If it is rooted in the baseline its DEL is lost;
				// if it is a not-rooted delta tx that was trimmed, the intermediate output was ADDed
				// with no producer to cancel it. Either way the delta is unbalanced by this output.
				report("PRODUCER ABSENT FROM CONE", amountOf(refProducer))
			case pc.IsInTheState(coneProducer):
				// rooted producer: Mutations() must DEL this consumed output
				if !actualDel.Contains(oid) {
					reasonWhy := "unknown"
					if refProducer != nil && refProducer != coneProducer {
						reasonWhy = "input ref is a DUPLICATE WrappedTx, not the cone instance"
					} else if !pc.consumerReported(coneProducer, oid.Index(), vid) {
						reasonWhy = "consumed-link not visible in producer.consumed"
					}
					report("ROOTED PRODUCER, DEL MISSING ("+reasonWhy+")", amountOf(coneProducer))
					genForensic("rooted-DEL-missing", coneProducer, refProducer)
				}
			default:
				// not-rooted producer in the cone: the consumed output is intermediate and must be
				// excluded from the ADD set by producedIndices(). That exclusion relies on the same
				// consumed-link; if it is not visible (or the input points at a duplicate WrappedTx),
				// Mutations() ADDs the intermediate output with nothing to cancel it -> over-count on
				// the created side, the mirror image of a rooted producer's missing DEL.
				switch {
				case refProducer != nil && refProducer != coneProducer:
					report("DUPLICATE WrappedTx for not-rooted producer", amountOf(coneProducer))
				case !pc.consumerReported(coneProducer, oid.Index(), vid):
					report("NOT-ROOTED PRODUCER, intermediate output wrongly ADDed (consumed-link not visible)", amountOf(coneProducer))
					genForensic("not-rooted-intermediate", coneProducer, refProducer)
				}
			}
		}
	}
	ret.Add("---- imbalance diagnostic: %d anomaly(ies), unmatched amount ~%s ----", numAnomalies, util.Th(unmatched))
	return ret
}

// consumerReported reports whether consumersByOutputIndex sees `consumer` among the consumers of
// producer's output at idx — i.e. whether Mutations() would act on the consumed link.
func (pc *PastCone) consumerReported(producer *WrappedTx, idx byte, consumer *WrappedTx) bool {
	for _, c := range pc.consumersByOutputIndex(producer)[idx] {
		if c == consumer {
			return true
		}
	}
	return false
}

func (pc *PastCone) hasRooted() bool {
	for vid, flags := range pc.vertices {
		if flags.FlagsUp(FlagPastConeVertexInTheState) {
			return true
		}
		// baseline defines the state — it is implicitly rooted even when not marked InTheState
		// (detached branches have defined=false, so defineInTheStateStatus is never called for them)
		if pc.baselineBranchID != nil && vid.ID() == *pc.baselineBranchID {
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

// reconcilableUnder reports whether pcb — anchored to a branch on a lineage incompatible with the merge
// target's current baseline — can still be merged under that baseline. It can iff every delta vertex of pcb
// (all but pcb's own foreign baseline branch) is already committed in the current baseline's state, per the
// knowsInCurrentBaseline oracle. Then pcb contributes only pre-fork, lineage-neutral history and the foreign
// baseline label is irrelevant; otherwise pcb carries genuinely foreign content and the merge is a conflict.
func (pcb *PastConeBase) reconcilableUnder(knowsInCurrentBaseline func(txid base.TransactionID) bool) bool {
	for vid := range pcb.vertices {
		if vid.ID() == *pcb.baselineBranchID {
			continue
		}
		if !knowsInCurrentBaseline(vid.id) {
			return false
		}
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
		// pcb is anchored to a branch on a DIFFERENT lineage than the current baseline. This is the
		// multi-branch-per-slot cross-pin an out-of-sync node hits when it forward-syncs through steady-state
		// traffic (several sequencer branches per slot): a Good sequencer vertex can carry a PastConeBase whose
		// baseline was floor-labeled to one lineage while a merging attacher sits on another. Reconcile instead
		// of wedging the LRB when every delta vertex of pcb is already committed in the current baseline's
		// state — then pcb adds only pre-fork, lineage-neutral history that merges soundly under the current
		// baseline (the foreign baseline frontier is dropped, the current baseline replaces it). If any vertex
		// is genuinely foreign-lineage content the current baseline does not know, it is a real conflict.
		if !pcb.reconcilableUnder(func(txid base.TransactionID) bool {
			return br.BranchKnowsTransaction(*currentBaseline, txid)
		}) {
			return false
		}
		for vid := range pcb.vertices {
			if vid.ID() == *pcb.baselineBranchID {
				continue
			}
			pc.SetFlagsUp(vid, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState|FlagPastConeVertexInTheState|FlagPastConeVertexDefined)
		}
		return true
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
			// closure defers expensive pcb.Lines() — only evaluated if assertion fails (via lazyargs.Eval)
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
		// We used to also Unwrap the Vertex here and call v.NumMissingInputs() as a
		// belt-and-suspenders check that v.Inputs was populated. That read races with
		// GC's ConvertToDetached → UnReferenceDependencies (clears v.Inputs) and
		// ReattachVertexNoLock (installs a fresh Vertex with nil-init Inputs). Under
		// load this fired false-positive even though the dependency vid was still in
		// the past cone with FlagPastConeVertexDefined set. The past cone's own
		// bookkeeping (checkFinalFlags above) is the source of truth for "all
		// dependencies present"; the Vertex-state read added nothing but a race.
	}
	if conflict, ctxErr := pc.CheckConflicts(context.Background(), getStateReader); ctxErr != nil {
		return ctxErr
	} else if conflict != nil {
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

// CheckConflicts returns double-spent output (conflict) or nil if the past cone is consistent.
// Returns context error if the context is cancelled or its deadline exceeded during iteration.
// The complexity is O(NxM) where N is number of vertices and M is an average number of conflicts in the UTXO tangle
// Practically, it is linear wrt the number of vertices because M is 1 or close to 1.
func (pc *PastCone) CheckConflicts(ctx context.Context, getStateReader func(branchID base.TransactionID) multistate.StateReader) (conflict *WrappedOutput, err error) {
	// detect orphaned branches before checking individual vertices —
	// the per-vertex check misses conflicts when the stem producer is not in pc.vertices
	if orphanConflict := pc._detectOrphanedBranch(); orphanConflict != nil {
		conflict = orphanConflict
		return
	}

	rdr := getStateReader(*pc.GetBaseline())
	pc.forAllVertices(func(vid *WrappedTx) bool {
		if e := ctx.Err(); e != nil {
			err = e
			return false
		}
		conflict, _ = pc._checkVertex(vid, rdr)
		return conflict == nil
	})
	return
}

// CheckAndClean iterates past cone, checks for conflicts and removes those vertices
// that have consumers and all consumers are already in the state.
// Returns context error if the context is cancelled or its deadline exceeded during iteration.
// pending (when non-nil) is a not-in-state branch ON the tip's own lineage that the tip/baseline
// depends on but that has not been committed yet — a catch-up ordering artifact, not a fork (see
// _removeOrphanedBranchSubtrees). The caller waits for it to commit and retries, rather than failing.
func (pc *PastCone) CheckAndClean(ctx context.Context, getStateReader func(branchID base.TransactionID) multistate.StateReader) (conflict *WrappedOutput, pending *WrappedTx, err error) {
	pc.Assertf(pc.baselineBranchID != nil, "pc.baseline!=nil")
	pc.Assertf(len(pc.virtuallyConsumed) == 0, "len(pb.virtuallyConsumed)==0")
	pc.Assertf(pc.delta == nil, "pc.delta == nil")

	var canBeRemoved bool

	// Phase 1: detect and remove orphaned branch subtrees.
	// An orphaned branch is a not-in-state branch vertex that is neither the baseline nor the tip.
	// It indicates a competing branch chain that leaked into the past cone through transitive
	// PastConeBase merges or the Good+InTheState+nil-PastConeBase code path.
	// Removing the orphaned branch alone is not enough — all not-in-state vertices that
	// transitively consume its outputs must also be removed, otherwise Mutations() would
	// generate ADD mutations without corresponding DELs (conservation invariant violation).
	if n, orphanConflict, pendingBranch := pc._removeOrphanedBranchSubtrees(); orphanConflict != nil {
		// tip or baseline depends on a genuinely dead (Bad) orphaned branch — the past cone is invalid
		conflict = orphanConflict
		return
	} else if pendingBranch != nil {
		// tip or baseline depends on a not-yet-committed canonical branch on its own lineage —
		// signal the caller to wait for its commit instead of failing
		pending = pendingBranch
		return
	} else if n > 0 {
		pc.Log().Warnf("CheckAndClean %s: removed %d orphaned vertices from past cone", pc.name, n)
	}

	// Phase 2: check for conflicts and remove rooted vertices that contribute nothing to the
	// delta (see _checkVertex). Branches are NOT exempt: ancestor branches are exactly the
	// dead-weight rooted vertices that accumulate per-slot via MergePastCone and leaked the
	// memDAG. The old per-branch exemption (f860eac7) was a stopgap superseded the same day by
	// Phase 1 (_removeOrphanedBranchSubtrees, 027da26a), which catches competing same-slot
	// branches structurally regardless of whether rooted branches are present. The baseline
	// branch and the tip are always kept (the inTheState guard already excludes the not-rooted
	// tip, but we keep the explicit guard for safety).
	rdr := getStateReader(*pc.GetBaseline())
	for vid, flags := range pc.vertices {
		if e := ctx.Err(); e != nil {
			err = e
			return
		}
		if vid != pc.tip && vid.ID() != *pc.baselineBranchID {
			pc.Assertf(flags.FlagsUp(FlagPastConeVertexKnown|FlagPastConeVertexDefined|FlagPastConeVertexCheckedInTheState), "wrong flag in %s", vid.IDShortString)
		}
		conflict, canBeRemoved = pc._checkVertex(vid, rdr)
		if conflict != nil {
			return
		}
		if canBeRemoved && vid != pc.tip && vid.ID() != *pc.baselineBranchID {
			delete(pc.vertices, vid)
		}
	}
	return
}

// _removeOrphanedBranchSubtrees detects competing branches in the past cone and removes
// them together with all not-in-state vertices that transitively depend on their outputs.
// Returns the number of removed vertices, a conflict output if the tip depends on a genuinely
// dead (Bad) orphan, and a pending branch if the tip depends on a not-yet-committed canonical one.
//
// A branch vertex is orphaned if it is not-in-state, not the baseline, and not the tip.
// In a valid past cone only two branches can be not-in-state: the baseline (state boundary)
// and the tip (being committed). Any other not-in-state branch is either from a competing fork
// that leaked in through PastConeBase merges, or — during catch-up — a canonical branch on the
// tip's own lineage that the in-order commit has not reached yet (gossip attached this tx ahead
// of its baseline branch). These two cases are structurally identical, so they are told apart by
// status: a Bad branch is the dead fork; any other (Good/solidifying) branch is the pending one.
//
// If the tip or baseline transitively consumes from an orphaned branch, the past cone cannot be
// committed as-is: a Bad orphan makes it invalid (conflict); a not-Bad one only means "not yet"
// (pending — the caller waits for that branch to commit, then retries).
func (pc *PastCone) _removeOrphanedBranchSubtrees() (int, *WrappedOutput, *WrappedTx) {
	// Step 1: seed the orphan set with competing branches
	orphans := set.New[*WrappedTx]()
	for vid := range pc.vertices {
		if !vid.IsBranchTransaction() {
			continue
		}
		if pc.IsInTheState(vid) {
			continue
		}
		if vid == pc.tip {
			continue
		}
		if pc.baselineBranchID != nil && vid.ID() == *pc.baselineBranchID {
			continue
		}
		orphans.Insert(vid)
	}
	if len(orphans) == 0 {
		return 0, nil, nil
	}

	// Step 2: propagate forward — any not-in-state vertex that consumes an output
	// produced by an orphan is itself orphaned.
	// If the tip or baseline consumes from an orphan, the past cone is invalid.
	changed := true
	for changed {
		changed = false
		for orphan := range orphans {
			byIdx := pc.consumersByOutputIndex(orphan)
			for _, consumers := range byIdx {
				for _, consumer := range consumers {
					if consumer == nil || pc.IsInTheState(consumer) || orphans.Contains(consumer) {
						continue
					}
					if consumer == pc.tip || (pc.baselineBranchID != nil && consumer.ID() == *pc.baselineBranchID) {
						// the tip or baseline depends on this orphaned branch. A Bad branch is a dead
						// fork -> the past cone is invalid (conflict). Any other status means it is the
						// canonical branch on the tip's lineage, committed momentarily after the tip was
						// attached (catch-up ordering) -> pending, the caller waits for its commit.
						if orphan.IsBad() {
							conflictOut := WrappedOutput{VID: orphan, Index: 0}
							return 0, &conflictOut, nil
						}
						return 0, nil, orphan
					}
					orphans.Insert(consumer)
					changed = true
				}
			}
		}
	}

	// Step 3: remove all orphaned vertices from the past cone
	for vid := range orphans {
		delete(pc.vertices, vid)
	}
	return len(orphans), nil, nil
}

// _detectOrphanedBranch returns a conflict if any orphaned branch exists in the past cone.
// Read-only: does not modify pc.vertices. Safe to call during an active delta.
func (pc *PastCone) _detectOrphanedBranch() *WrappedOutput {
	var found *WrappedOutput
	pc.forAllVertices(func(vid *WrappedTx) bool {
		if !vid.IsBranchTransaction() || pc.IsInTheState(vid) {
			return true
		}
		if vid == pc.tip {
			return true
		}
		if pc.baselineBranchID != nil && vid.ID() == *pc.baselineBranchID {
			return true
		}
		found = &WrappedOutput{VID: vid, Index: 0}
		return false
	})
	return found
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
		// Safety net for claude/pastcone_consistency.md §4.1.a: the consumer flag may be
		// stale S- (CheckedInTheState=true, InTheState=false) against an older baseline,
		// even though this past cone's baseline has the consumer committed. Verify
		// against the live Branches index and upgrade the flag in place so the
		// conflict-check path matches the state-trie ground truth. Only fires on the
		// rare fall-through from the flag cache, so the hot loop stays flag-cached.
		if consumers[0] != nil && pc.baselineKnowsTx(consumers[0].ID()) {
			pc.Tracef(TraceTagPastConeDiag, "STALE S- upgraded in _checkVertex: pc=%s baseline=%s vid=%s consumer=%s",
				pc.name, pc.baselineBranchID.StringShort(), vid.IDShortString(), consumers[0].IDShortString())
			pc.UpgradeToInTheState(consumers[0])
			continue
		}
		// virtual consumer nil is never in the state
		allConsumersAreInTheState = false
		if inTheState && !stateReader.HasUTXO(wOut.DecodeID()) {
			pc.diagLogSuspectConflict(vid, wOut, consumers[0])
			return &wOut, false
		}
	}
	// Removable iff the vertex is ROOTED (already in the baseline state) and no not-rooted
	// transaction consumes any of its outputs — then it contributes nothing to the past-cone
	// delta (coverage/mutations). This covers the byIdx==0 case (allConsumersAreInTheState
	// stays true when there are no in-cone consumers): a rooted vertex with no in-cone consumer
	// is dead weight. The old `len(byIdx) > 0` guard wrongly kept exactly those, which let
	// ancestor branches (stem consumed by the next branch, not in pc.vertices) accumulate.
	// The inTheState guard is essential: without it the tip and not-rooted delta leaves (which
	// Mutations() must commit) would be removed, violating conservation.
	canBeRemoved = inTheState && allConsumersAreInTheState
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
// Returns the total coverage delta.
func (pc *PastCone) CoverageDeltaRaw(ctx context.Context, getStateReader func(branchID base.TransactionID) multistate.StateReader) (delta uint64, err error) {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	pc.Assertf(pc.baselineBranchID != nil, "pc.baseline != nil")

	rdr := getStateReader(*pc.GetBaseline())
	for vid := range pc.vertices {
		if e := ctx.Err(); e != nil {
			err = e
			return
		}
		for _, idx := range pc.consumedUTXOIndices(vid) {
			oid := vid.OutputID(idx)
			if o := multistate.GetOutputFromStateReader(rdr, oid); o != nil {
				delta += ledger.Coverage(o, oid, pc.txTs)
			}
		}
	}
	return
}

// SequencerFrozenCoverageDelta returns the signed change, over this branch's
// past-cone delta, in the total tokens frozen by delegations across all
// sequencer chains. It is the mutation-set form (same iteration as Mutations)
// of Σ over delta sequencer transitions of (succ.frozenCoverage[0] -
// pred.frozenCoverage[0]):
//
//	+ frozenCoverage[0] of produced-and-unspent sequencer tips (ADD mutations)
//	- frozenCoverage[0] of spent baseline sequencer tips        (DEL mutations)
//
// Only sequencer outputs are counted: the sequencer aggregates the frozen
// coverage of the delegations targeting it (a delegation output mirrors the
// same value on itself, so counting both would double-count). Regular chains
// and foundries carry an all-zero frozen vector, so they contribute 0.
//
// Accumulated onto the baseline branch's FrozenCoverage it yields the total
// frozen tokens at this branch (telescoping; see claude/frozen_coverage.md).
func (pc *PastCone) SequencerFrozenCoverageDelta() (delta int64) {
	for vid := range pc.vertices {
		if pc.IsInTheState(vid) {
			// DEL: baseline tips spent by a not-in-the-state consumer
			for idx, consumers := range pc.consumersByOutputIndex(vid) {
				pc.Assertf(len(consumers) == 1, "SequencerFrozenCoverageDelta: len(consumers)==1")
				if pc.isNotInTheState(consumers[0]) {
					if o := vid.MustOutputAt(idx); o.IsSequencerOutput() {
						delta -= o.FrozenCoverage(0)
					}
				}
			}
		} else {
			// ADD: produced-and-unspent tips
			for _, idx := range pc.producedIndices(vid) {
				if o := vid.MustOutputAt(idx); o.IsSequencerOutput() {
					delta += o.FrozenCoverage(0)
				}
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

// NumNewTransactions counts vertices in the past cone that are NOT in the
// baseline state — i.e. transactions that THIS branch is committing for the
// first time. Matches `MutationStats.NumConfirmedTransactions` from Mutations(), but
// without building the full mutation set.
func (pc *PastCone) NumNewTransactions() int {
	numTx, _, _ := pc.NumNewTransactionStats()
	return numTx
}

// NumNewTransactionStats counts, in a single pass over the past cone, the new
// (non-rooted) transactions (numTx), how many of them are sequencer
// transactions (numSeqTx), and the number of distinct sequencers among those
// (numSeq) — the OracleData numTransactions / numSeqTransactions / numSeq
// aggregates.
//
// includeSeq pre-seeds the distinct-sequencer set. The branch builder passes
// its own sequencer ID so the predicted numSeq matches the verifying attacher,
// whose past cone already contains the branch transaction itself (and thus its
// sequencer). It affects numSeq only, not numTx / numSeqTx.
func (pc *PastCone) NumNewTransactionStats(includeSeq ...base.ChainID) (numTx, numSeqTx, numSeq int) {
	pc.Assertf(pc.delta == nil, "pc.delta == nil")
	seen := set.New[base.ChainID](includeSeq...)
	for vid := range pc.vertices {
		if !pc.isNotInTheState(vid) {
			continue
		}
		numTx++
		if vid.IsSequencerTransaction() {
			numSeqTx++
			if p := vid.SequencerID.Load(); p != nil {
				seen.Insert(*p)
			}
		}
	}
	return numTx, numSeqTx, len(seen)
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
