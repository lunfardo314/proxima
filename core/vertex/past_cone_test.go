// Tests for the PastCone and PastConeBase types in the core/vertex package.
// PastCone tracks the past cone of a transaction - all ancestor transactions
// back to ones that are "rooted" in the baseline state.
//
// Key concepts tested:
// - FlagsPastCone: bit flags tracking vertex state within a past cone
// - PastConeBase: base structure with vertices map and baseline branch ID
// - PastCone: full past cone with delta transaction support for atomic updates
// - Virtually consumed outputs: tracking outputs consumed within the past cone
// - Delta operations: BeginDelta/CommitDelta/RollbackDelta for atomic changes

package vertex

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestFlagsPastCone tests the FlagsPastCone type used to track vertex state.
// Each flag represents a different aspect of vertex processing:
// - Known: vertex is part of consideration
// - Defined: vertex validity has been checked
// - CheckedInTheState/InTheState: vertex presence in baseline state
// - EndorsementsSolid/InputsSolid: dependencies validated
// - AskedForPoke: notification requested
func TestFlagsPastCone(t *testing.T) {
	t.Run("flags up check", func(t *testing.T) {
		var f FlagsPastCone

		require.False(t, f.FlagsUp(FlagPastConeVertexKnown))
		require.False(t, f.FlagsUp(FlagPastConeVertexDefined))

		f = FlagPastConeVertexKnown
		require.True(t, f.FlagsUp(FlagPastConeVertexKnown))
		require.False(t, f.FlagsUp(FlagPastConeVertexDefined))

		f = FlagPastConeVertexKnown | FlagPastConeVertexDefined
		require.True(t, f.FlagsUp(FlagPastConeVertexKnown))
		require.True(t, f.FlagsUp(FlagPastConeVertexDefined))
		require.True(t, f.FlagsUp(FlagPastConeVertexKnown|FlagPastConeVertexDefined))
	})

	t.Run("all flags", func(t *testing.T) {
		f := FlagPastConeVertexKnown |
			FlagPastConeVertexDefined |
			FlagPastConeVertexCheckedInTheState |
			FlagPastConeVertexInTheState |
			FlagPastConeVertexEndorsementsSolid |
			FlagPastConeVertexInputsSolid |
			FlagPastConeVertexAskedForPoke

		require.True(t, f.FlagsUp(FlagPastConeVertexKnown))
		require.True(t, f.FlagsUp(FlagPastConeVertexDefined))
		require.True(t, f.FlagsUp(FlagPastConeVertexCheckedInTheState))
		require.True(t, f.FlagsUp(FlagPastConeVertexInTheState))
		require.True(t, f.FlagsUp(FlagPastConeVertexEndorsementsSolid))
		require.True(t, f.FlagsUp(FlagPastConeVertexInputsSolid))
		require.True(t, f.FlagsUp(FlagPastConeVertexAskedForPoke))
	})

	t.Run("string representation", func(t *testing.T) {
		var f FlagsPastCone
		str := f.String()
		require.Contains(t, str, "known: false")
		require.Contains(t, str, "defined: false")

		f = FlagPastConeVertexKnown | FlagPastConeVertexDefined
		str = f.String()
		require.Contains(t, str, "known: true")
		require.Contains(t, str, "defined: true")
	})

	t.Run("string with state flags", func(t *testing.T) {
		f := FlagPastConeVertexCheckedInTheState | FlagPastConeVertexInTheState
		str := f.String()
		require.Contains(t, str, "inTheState: (true,true)")
	})
}

// TestNewPastConeBase tests the PastConeBase constructor.
// PastConeBase holds the vertices map and baseline branch ID.
func TestNewPastConeBase(t *testing.T) {
	t.Run("with nil baseline", func(t *testing.T) {
		pb := NewPastConeBase(nil)

		require.NotNil(t, pb)
		require.NotNil(t, pb.vertices)
		require.Equal(t, 0, len(pb.vertices))
		require.Nil(t, pb.baselineBranchID)
	})

	t.Run("with baseline", func(t *testing.T) {
		branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
		pb := NewPastConeBase(&branchID)

		require.NotNil(t, pb)
		require.NotNil(t, pb.baselineBranchID)
		require.Equal(t, branchID, *pb.baselineBranchID)
	})
}

// TestPastConeBaseLen tests the Len method that returns vertex count.
func TestPastConeBaseLen(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	pb := NewPastConeBase(&branchID)

	require.Equal(t, 0, pb.Len())

	// Add some vertices
	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	vid2 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))

	pb.vertices[vid1] = FlagPastConeVertexKnown
	require.Equal(t, 1, pb.Len())

	pb.vertices[vid2] = FlagPastConeVertexKnown
	require.Equal(t, 2, pb.Len())
}

// TestPastConeBaseCloneImmutable tests cloning a PastConeBase.
// CloneImmutable creates a deep copy of vertices but requires no virtually consumed outputs.
func TestPastConeBaseCloneImmutable(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	pb := NewPastConeBase(&branchID)

	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	vid2 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))

	pb.vertices[vid1] = FlagPastConeVertexKnown | FlagPastConeVertexDefined
	pb.vertices[vid2] = FlagPastConeVertexKnown

	clone := pb.CloneImmutable()

	require.NotNil(t, clone)
	require.Equal(t, 2, clone.Len())
	require.Equal(t, pb.baselineBranchID, clone.baselineBranchID)
	require.Equal(t, pb.vertices[vid1], clone.vertices[vid1])
	require.Equal(t, pb.vertices[vid2], clone.vertices[vid2])

	// Verify it's a deep copy - modifying clone doesn't affect original
	vid3 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1003, 50)))
	clone.vertices[vid3] = FlagPastConeVertexKnown
	require.Equal(t, 3, clone.Len())
	require.Equal(t, 2, pb.Len())
}

// TestPastConeBaseVirtuallyConsumed tests tracking of virtually consumed outputs.
// Virtually consumed outputs are those consumed within the past cone but not yet in state.
func TestPastConeBaseVirtuallyConsumed(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	pb := NewPastConeBase(&branchID)

	vid := WrapTxID(base.RandomTransactionID(false, 5, base.T(1001, 50)))

	t.Run("initially not consumed", func(t *testing.T) {
		wOut := WrappedOutput{VID: vid, Index: 0}
		require.False(t, pb._isVirtuallyConsumed(wOut))
	})

	t.Run("add virtually consumed", func(t *testing.T) {
		wOut := WrappedOutput{VID: vid, Index: 0}
		pb.addVirtuallyConsumedOutput(wOut)

		require.True(t, pb._isVirtuallyConsumed(wOut))
		require.False(t, pb._isVirtuallyConsumed(WrappedOutput{VID: vid, Index: 1}))
	})

	t.Run("add multiple indices", func(t *testing.T) {
		wOut1 := WrappedOutput{VID: vid, Index: 1}
		wOut2 := WrappedOutput{VID: vid, Index: 2}

		pb.addVirtuallyConsumedOutput(wOut1)
		pb.addVirtuallyConsumedOutput(wOut2)

		require.True(t, pb._isVirtuallyConsumed(wOut1))
		require.True(t, pb._isVirtuallyConsumed(wOut2))
		require.False(t, pb._isVirtuallyConsumed(WrappedOutput{VID: vid, Index: 3}))
	})

	t.Run("virtuallyConsumedIndexSet", func(t *testing.T) {
		indexSet := pb._virtuallyConsumedIndexSet(vid)
		require.Contains(t, indexSet, byte(0))
		require.Contains(t, indexSet, byte(1))
		require.Contains(t, indexSet, byte(2))
		require.NotContains(t, indexSet, byte(3))
	})

	t.Run("virtuallyConsumedIndexSet for unknown vid", func(t *testing.T) {
		unknownVid := WrapTxID(base.RandomTransactionID(false, 3, base.T(2000, 50)))
		indexSet := pb._virtuallyConsumedIndexSet(unknownVid)
		require.Equal(t, 0, len(indexSet))
	})
}

// TestPastConeBaseLines tests the Lines formatting method.
func TestPastConeBaseLines(t *testing.T) {
	t.Run("nil pastcone", func(t *testing.T) {
		var pb *PastConeBase
		lines := pb.Lines()
		require.Contains(t, lines.String(), "<nil pastCone>")
	})

	t.Run("with baseline and vertices", func(t *testing.T) {
		branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
		pb := NewPastConeBase(&branchID)

		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
		pb.vertices[vid] = FlagPastConeVertexKnown

		lines := pb.Lines()
		str := lines.String()
		require.Contains(t, str, "baseline:")
		require.Contains(t, str, "dept")
	})

	t.Run("with virtually consumed", func(t *testing.T) {
		branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
		pb := NewPastConeBase(&branchID)

		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
		pb.addVirtuallyConsumedOutput(WrappedOutput{VID: vid, Index: 0})

		lines := pb.Lines()
		str := lines.String()
		require.Contains(t, str, "virt")
	})
}

// TestPastConeBaseDispose tests resource cleanup.
func TestPastConeBaseDispose(t *testing.T) {
	t.Run("dispose nil", func(t *testing.T) {
		var pb *PastConeBase
		require.NotPanics(t, func() {
			pb.Dispose()
		})
	})

	t.Run("dispose with data", func(t *testing.T) {
		branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
		pb := NewPastConeBase(&branchID)

		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
		pb.vertices[vid] = FlagPastConeVertexKnown
		pb.addVirtuallyConsumedOutput(WrappedOutput{VID: vid, Index: 0})

		pb.Dispose()

		require.Nil(t, pb.baselineBranchID)
		require.Nil(t, pb.vertices)
		require.Nil(t, pb.virtuallyConsumed)
	})
}

// TestPastConeBaseline tests baseline management in PastCone.
// The baseline branch is the reference point for determining which vertices are "rooted".
func TestPastConeBaseline(t *testing.T) {
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	t.Run("get baseline initially nil", func(t *testing.T) {
		pc := NewPastCone(nil, tip, ts, "test")

		require.Nil(t, pc.GetBaseline())
	})

	t.Run("set and get baseline", func(t *testing.T) {
		pc := NewPastCone(nil, tip, ts, "test")

		branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
		pc.baselineBranchID = &branchID

		require.NotNil(t, pc.GetBaseline())
		require.Equal(t, branchID, *pc.GetBaseline())
	})
}

// TestPastConeFlagOperations tests flag manipulation on vertices within a past cone.
// Flags track the processing state of each vertex in the past cone.
func TestPastConeFlagOperations(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))

	t.Run("initial flags are zero", func(t *testing.T) {
		flags := pc.Flags(vid)
		require.Equal(t, FlagsPastCone(0), flags)
	})

	t.Run("set flags up", func(t *testing.T) {
		pc.SetFlagsUp(vid, FlagPastConeVertexKnown)
		require.True(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexKnown))

		pc.SetFlagsUp(vid, FlagPastConeVertexDefined)
		require.True(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexKnown))
		require.True(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexDefined))
	})

	t.Run("set flags down", func(t *testing.T) {
		pc.SetFlagsDown(vid, FlagPastConeVertexDefined)
		require.True(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexKnown))
		require.False(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexDefined))
	})
}

// TestPastConeKnownStatus tests vertex "known" status queries.
// A vertex is "known" if it's part of the past cone being processed.
func TestPastConeKnownStatus(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))

	t.Run("initially not known", func(t *testing.T) {
		require.False(t, pc.IsKnown(vid))
		require.False(t, pc.IsKnownDefined(vid))
	})

	t.Run("mark known", func(t *testing.T) {
		pc.MarkVertexKnown(vid)
		require.True(t, pc.IsKnown(vid))
		require.False(t, pc.IsKnownDefined(vid))
	})

	t.Run("mark defined", func(t *testing.T) {
		pc.SetFlagsUp(vid, FlagPastConeVertexDefined)
		require.True(t, pc.IsKnown(vid))
		require.True(t, pc.IsKnownDefined(vid))
	})
}

// TestPastConeStateStatus tests vertex "in the state" queries.
// Vertices can be checked against the baseline state to determine if they're rooted.
func TestPastConeStateStatus(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))

	t.Run("initially not in state", func(t *testing.T) {
		require.False(t, pc.IsInTheState(vid))
	})

	t.Run("mark not in state", func(t *testing.T) {
		pc.MarkVertexKnown(vid)
		pc.MustMarkVertexNotInTheState(vid)

		// Should be known and checked, but not in state
		require.True(t, pc.IsKnown(vid))
		require.True(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexCheckedInTheState))
		require.False(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexInTheState))
		require.False(t, pc.IsInTheState(vid))
	})

	t.Run("mark in state", func(t *testing.T) {
		vid2 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))
		pc.SetFlagsUp(vid2, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState|FlagPastConeVertexInTheState)

		require.True(t, pc.IsInTheState(vid2))
	})
}

// TestPastConeDeltaOperations tests the delta transaction mechanism.
// BeginDelta/CommitDelta/RollbackDelta allow atomic updates to the past cone.
func TestPastConeDeltaOperations(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	t.Run("begin and commit delta", func(t *testing.T) {
		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))

		pc.BeginDelta()
		require.NotNil(t, pc.delta)

		// Changes in delta
		pc.SetFlagsUp(vid, FlagPastConeVertexKnown|FlagPastConeVertexDefined)
		require.True(t, pc.IsKnownDefined(vid))

		// Before commit, main vertices is empty
		require.Equal(t, 0, len(pc.vertices))

		pc.CommitDelta()
		require.Nil(t, pc.delta)

		// After commit, changes are in main vertices
		require.Equal(t, 1, len(pc.vertices))
		require.True(t, pc.IsKnownDefined(vid))
	})

	t.Run("begin and rollback delta", func(t *testing.T) {
		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))

		pc.BeginDelta()

		// Changes in delta
		pc.SetFlagsUp(vid, FlagPastConeVertexKnown|FlagPastConeVertexDefined)
		require.True(t, pc.IsKnownDefined(vid))

		pc.RollbackDelta()
		require.Nil(t, pc.delta)

		// After rollback, changes are discarded
		require.Equal(t, 0, len(pc.vertices))
		require.False(t, pc.IsKnownDefined(vid))
	})

	t.Run("rollback nil delta is safe", func(t *testing.T) {
		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

		require.NotPanics(t, func() {
			pc.RollbackDelta()
		})
	})

	t.Run("delta baseline", func(t *testing.T) {
		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

		newBranchID := base.RandomTransactionID(true, 2, base.T(999, 0))

		pc.BeginDelta()
		pc.delta.baselineBranchID = &newBranchID

		// GetBaseline should check base first, then delta
		require.Equal(t, branchID, *pc.GetBaseline())

		pc.CommitDelta()

		require.Equal(t, newBranchID, *pc.GetBaseline())
	})

	t.Run("delta virtually consumed", func(t *testing.T) {
		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

		vid := WrapTxID(base.RandomTransactionID(false, 5, base.T(1001, 50)))
		wOut := WrappedOutput{VID: vid, Index: 0}

		pc.BeginDelta()
		pc.delta.addVirtuallyConsumedOutput(wOut)

		// Should be visible through isVirtuallyConsumed
		require.True(t, pc.isVirtuallyConsumed(wOut))

		pc.CommitDelta()

		// Still visible after commit
		require.True(t, pc.isVirtuallyConsumed(wOut))
	})
}

// TestPastConeFlags tests reading flags with delta support.
// When a delta is active, flags are read from delta first, then base.
func TestPastConeFlagsWithDelta(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))

	// Set flags in base
	pc.vertices[vid] = FlagPastConeVertexKnown

	pc.BeginDelta()

	// Delta should see base flags
	require.True(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexKnown))

	// Add more flags in delta
	pc.SetFlagsUp(vid, FlagPastConeVertexDefined)

	// Should see combined flags
	require.True(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexKnown|FlagPastConeVertexDefined))

	pc.RollbackDelta()

	// After rollback, only base flags remain
	require.True(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexKnown))
	require.False(t, pc.Flags(vid).FlagsUp(FlagPastConeVertexDefined))
}

// TestPastConeForAllVertices tests iterating over all vertices.
// forAllVertices traverses both committed and uncommitted (delta) vertices.
func TestPastConeForAllVertices(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	vid2 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))
	vid3 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1003, 50)))

	pc.vertices[vid1] = FlagPastConeVertexKnown
	pc.vertices[vid2] = FlagPastConeVertexKnown

	t.Run("iterate without delta", func(t *testing.T) {
		count := 0
		pc.forAllVertices(func(vid *WrappedTx) bool {
			count++
			return true
		})
		require.Equal(t, 2, count)
	})

	t.Run("iterate with delta", func(t *testing.T) {
		pc.BeginDelta()
		pc.delta.vertices[vid3] = FlagPastConeVertexKnown

		count := 0
		pc.forAllVertices(func(vid *WrappedTx) bool {
			count++
			return true
		})
		require.Equal(t, 3, count)

		pc.RollbackDelta()
	})

	t.Run("iterate sorted ascending", func(t *testing.T) {
		var lastTs base.LedgerTime
		pc.forAllVertices(func(vid *WrappedTx) bool {
			require.True(t, !lastTs.After(vid.Timestamp()))
			lastTs = vid.Timestamp()
			return true
		}, true)
	})

	t.Run("iterate sorted descending", func(t *testing.T) {
		var lastTs base.LedgerTime
		first := true
		pc.forAllVertices(func(vid *WrappedTx) bool {
			if !first {
				require.True(t, !lastTs.Before(vid.Timestamp()))
			}
			first = false
			lastTs = vid.Timestamp()
			return true
		}, false)
	})

	t.Run("early termination", func(t *testing.T) {
		count := 0
		pc.forAllVertices(func(vid *WrappedTx) bool {
			count++
			return false // Stop after first
		})
		require.Equal(t, 1, count)
	})
}

// TestPastConeNumVertices tests the vertex count method.
func TestPastConeNumVertices(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	require.Equal(t, 0, pc.NumVertices())

	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	vid2 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))

	pc.vertices[vid1] = FlagPastConeVertexKnown
	require.Equal(t, 1, pc.NumVertices())

	pc.vertices[vid2] = FlagPastConeVertexKnown
	require.Equal(t, 2, pc.NumVertices())
}

// TestPastConeDispose tests resource cleanup for PastCone.
func TestPastConeDispose(t *testing.T) {
	t.Run("dispose nil", func(t *testing.T) {
		var pc *PastCone
		require.NotPanics(t, func() {
			pc.Dispose()
		})
	})

	t.Run("dispose with data", func(t *testing.T) {
		branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
		tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
		tip := WrapTxID(tipTxID)
		ts := tipTxID.Timestamp()

		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
		pc.vertices[vid] = FlagPastConeVertexKnown

		pc.BeginDelta()
		pc.delta.vertices[vid] = FlagPastConeVertexDefined

		pc.Dispose()

		require.Nil(t, pc.tip)
		require.Nil(t, pc.PastConeBase)
		require.Nil(t, pc.delta)
	})
}

// TestPastConeHasRooted tests checking for rooted vertices.
// A rooted vertex is one that exists in the baseline state.
func TestPastConeHasRooted(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))

	t.Run("no rooted initially", func(t *testing.T) {
		require.False(t, pc.hasRooted())
	})

	t.Run("with non-rooted vertex", func(t *testing.T) {
		pc.vertices[vid] = FlagPastConeVertexKnown | FlagPastConeVertexDefined
		require.False(t, pc.hasRooted())
	})

	t.Run("with rooted vertex", func(t *testing.T) {
		pc.vertices[vid] = FlagPastConeVertexKnown | FlagPastConeVertexInTheState
		require.True(t, pc.hasRooted())
	})
}

// TestPastConeContainsUndefined tests checking for undefined vertices.
// Undefined vertices are those whose validity hasn't been checked yet.
func TestPastConeContainsUndefined(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	// Create baseline vid to exclude from undefined check
	baselineVid := WrapTxID(branchID)

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))

	t.Run("empty is not undefined", func(t *testing.T) {
		require.False(t, pc.ContainsUndefined())
	})

	t.Run("tip is excluded", func(t *testing.T) {
		pc.vertices[tip] = FlagPastConeVertexKnown
		require.False(t, pc.ContainsUndefined())
	})

	t.Run("baseline is excluded", func(t *testing.T) {
		pc.vertices[baselineVid] = FlagPastConeVertexKnown
		require.False(t, pc.ContainsUndefined())
	})

	t.Run("undefined vertex detected", func(t *testing.T) {
		pc.vertices[vid] = FlagPastConeVertexKnown // known but not defined
		require.True(t, pc.ContainsUndefined())
	})

	t.Run("all defined", func(t *testing.T) {
		pc.vertices[vid] = FlagPastConeVertexKnown | FlagPastConeVertexDefined
		require.False(t, pc.ContainsUndefined())
	})
}

// TestPastConeUndefinedList tests getting a list of undefined vertices.
func TestPastConeUndefinedList(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	vid2 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))
	vid3 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1003, 50)))

	pc.vertices[vid1] = FlagPastConeVertexKnown // undefined
	pc.vertices[vid2] = FlagPastConeVertexKnown | FlagPastConeVertexDefined
	pc.vertices[vid3] = FlagPastConeVertexKnown // undefined

	undefinedList := pc.UndefinedList()
	require.Equal(t, 2, len(undefinedList))

	// Should be sorted by timestamp
	require.True(t, !undefinedList[0].Timestamp().After(undefinedList[1].Timestamp()))
}

// TestPastConeUndefinedListLines tests formatting undefined vertices.
func TestPastConeUndefinedListLines(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.vertices[vid] = FlagPastConeVertexKnown // undefined

	lines := pc.UndefinedListLines("prefix")
	require.NotNil(t, lines)
	str := lines.String()
	require.NotEmpty(t, str)
}

// TestPastConeLines tests the Lines formatting method.
func TestPastConeLines(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test_cone", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.vertices[vid] = FlagPastConeVertexKnown | FlagPastConeVertexDefined

	lines := pc.Lines()
	str := lines.String()

	require.Contains(t, str, "past cone:")
	require.Contains(t, str, "test_cone")
	require.Contains(t, str, "baseline:")
	require.Contains(t, str, "tip:")
}

// TestPastConeLinesShort tests the LinesShort formatting method.
func TestPastConeLinesShort(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test_cone", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.vertices[vid] = FlagPastConeVertexKnown

	lines := pc.LinesShort()
	str := lines.String()

	require.Contains(t, str, "past cone:")
	require.Contains(t, str, "test_cone")
	require.Contains(t, str, "baseline:")
}

// TestPastConeIsComplete tests the completeness check.
// A past cone is complete when: no delta, no undefined vertices, has rooted vertices.
func TestPastConeIsComplete(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	t.Run("incomplete with delta", func(t *testing.T) {
		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))
		pc.BeginDelta()
		require.False(t, pc.IsComplete())
		pc.RollbackDelta()
	})

	t.Run("incomplete without rooted", func(t *testing.T) {
		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
		pc.vertices[vid] = FlagPastConeVertexKnown | FlagPastConeVertexDefined

		require.False(t, pc.IsComplete())
	})

	t.Run("incomplete with undefined", func(t *testing.T) {
		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
		pc.vertices[vid] = FlagPastConeVertexKnown | FlagPastConeVertexInTheState // rooted but not defined
		require.True(t, pc.ContainsUndefined())
		require.False(t, pc.IsComplete())
	})

	t.Run("complete", func(t *testing.T) {
		pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
		pc.vertices[vid] = FlagPastConeVertexKnown | FlagPastConeVertexDefined | FlagPastConeVertexInTheState

		require.True(t, pc.IsComplete())
	})
}

// TestPastConeSlotInflation tests calculating slot inflation.
// Inflation is the sum of inflation amounts from vertices not in state.
func TestPastConeSlotInflation(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Virtual transactions have 0 inflation, so result should be 0
	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.vertices[vid] = FlagPastConeVertexKnown | FlagPastConeVertexDefined | FlagPastConeVertexCheckedInTheState

	inflation := pc.SlotInflation()
	require.Equal(t, uint64(0), inflation)
}

// TestPastConeIsConsumed tests checking if an output is consumed.
func TestPastConeIsConsumed(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 5, base.T(1001, 50)))
	wOut := WrappedOutput{VID: vid, Index: 0}

	t.Run("not consumed initially", func(t *testing.T) {
		require.False(t, pc.IsConsumed(wOut))
	})

	t.Run("virtually consumed", func(t *testing.T) {
		pc.addVirtuallyConsumedOutput(wOut)
		require.True(t, pc.IsConsumed(wOut))
	})
}

// TestPastConeCloneForDebugOnly tests debug cloning.
func TestPastConeCloneForDebugOnly(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "original", NewPastConeBase(&branchID))

	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.vertices[vid] = FlagPastConeVertexKnown | FlagPastConeVertexDefined
	pc.addVirtuallyConsumedOutput(WrappedOutput{VID: vid, Index: 0})

	clone := pc.CloneForDebugOnly(nil, "cloned")

	require.NotNil(t, clone)
	require.Contains(t, clone.name, "debug_clone")
	require.Equal(t, pc.tip, clone.tip)
	require.Equal(t, *pc.baselineBranchID, *clone.baselineBranchID)
	require.Equal(t, len(pc.vertices), len(clone.vertices))
	require.Equal(t, len(pc.virtuallyConsumed), len(clone.virtuallyConsumed))

	// Verify it's a deep copy
	vid2 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))
	clone.vertices[vid2] = FlagPastConeVertexKnown
	require.Equal(t, 1, len(pc.vertices))
	require.Equal(t, 2, len(clone.vertices))
}

// TestMutationStats tests the MutationStats structure.
func TestMutationStats(t *testing.T) {
	stats := MutationStats{
		NumTransactions: 10,
		NumDeleted:      3,
		NumCreated:      7,
	}

	require.Equal(t, 10, stats.NumTransactions)
	require.Equal(t, 3, stats.NumDeleted)
	require.Equal(t, 7, stats.NumCreated)
}
