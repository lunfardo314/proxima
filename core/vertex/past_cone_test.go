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
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// Initialize ledger for tests that need real transactions.
// This is a package-level variable that ensures ledger is initialized once.
var pastConeTestGenesisKey ed25519.PrivateKey

func init() {
	pastConeTestGenesisKey = ledger.InitWithTestingLedgerData(
		ledger.WithBranchCoverageBounds(0, 2*ledger.DefaultInitialSupply),
	)
}

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
		pc.MarkVertexNotInTheState(vid)

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

// =============================================================================
// Attachment Cost Tests
// =============================================================================
//
// These tests verify that incremental attachment cost calculation (maintained via
// MarkVertexNotInTheState) always equals the direct calculation (AttachmentCostDirect).
// AttachmentCost is the sum of (NumInputs + NumProducedOutputs) for all non-sequencer
// transactions that are definitely NOT in the baseline state.
//
// Key scenarios tested:
// - Basic attachment cost with virtual transactions (cost = 0 since VirtualTx has no AttachmentCost)
// - Delta commit preserves equality between incremental and direct
// - Delta rollback preserves equality
// - Sequencer transactions are excluded from cost
// - Multiple vertices accumulate correctly

// TestAttachmentCostBasic tests basic attachment cost invariant with virtual transactions.
// VirtualTx has AttachmentCost() = 0, so cost should be 0.
func TestAttachmentCostBasic(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	t.Run("empty past cone", func(t *testing.T) {
		require.Equal(t, 0, pc.AttachmentCost())
		require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
	})

	t.Run("vertex not marked not-in-state", func(t *testing.T) {
		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
		pc.MarkVertexKnown(vid)

		// Just known, not marked not-in-state, so no contribution to cost
		require.Equal(t, 0, pc.AttachmentCost())
		require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
	})

	t.Run("virtual tx marked not-in-state has zero cost", func(t *testing.T) {
		vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))
		pc.MarkVertexKnown(vid)
		pc.MarkVertexNotInTheState(vid)

		// VirtualTx has AttachmentCost() = 0
		require.Equal(t, 0, pc.AttachmentCost())
		require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
	})
}

// TestAttachmentCostSequencerExcluded tests that sequencer transactions are excluded from cost.
func TestAttachmentCostSequencerExcluded(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Create a sequencer transaction (isSequencer=true in the txid)
	seqTxID := base.RandomTransactionID(true, 3, base.T(1001, 50))
	seqVid := WrapTxID(seqTxID)

	require.True(t, seqVid.IsSequencerTransaction())

	pc.MarkVertexKnown(seqVid)
	pc.MarkVertexNotInTheState(seqVid)

	// Sequencer transactions don't contribute to attachment cost
	require.Equal(t, 0, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
}

// TestAttachmentCostDeltaCommit tests that delta commit preserves the invariant.
func TestAttachmentCostDeltaCommit(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Add some vertices to base
	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.MarkVertexKnown(vid1)
	pc.MarkVertexNotInTheState(vid1)

	initialCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), initialCost)

	// Begin delta
	pc.BeginDelta()

	// Add vertex in delta
	vid2 := WrapTxID(base.RandomTransactionID(false, 4, base.T(1002, 50)))
	pc.MarkVertexKnown(vid2)
	pc.MarkVertexNotInTheState(vid2)

	// During delta, cost should include both base and delta contributions
	deltaCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), deltaCost)

	// Commit delta
	pc.CommitDelta()

	// After commit, invariant should still hold
	commitedCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), commitedCost)
	require.Equal(t, deltaCost, commitedCost)
}

// TestAttachmentCostDeltaRollback tests that delta rollback preserves the invariant.
func TestAttachmentCostDeltaRollback(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Add some vertices to base
	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.MarkVertexKnown(vid1)
	pc.MarkVertexNotInTheState(vid1)

	initialCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), initialCost)

	// Begin delta
	pc.BeginDelta()

	// Add vertex in delta
	vid2 := WrapTxID(base.RandomTransactionID(false, 4, base.T(1002, 50)))
	pc.MarkVertexKnown(vid2)
	pc.MarkVertexNotInTheState(vid2)

	// During delta, cost may differ
	deltaCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), deltaCost)

	// Rollback delta
	pc.RollbackDelta()

	// After rollback, cost should return to initial value
	rollbackCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), rollbackCost)
	require.Equal(t, initialCost, rollbackCost)
}

// TestAttachmentCostMultipleDeltaCycles tests multiple begin/commit/rollback cycles.
func TestAttachmentCostMultipleDeltaCycles(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Cycle 1: commit
	pc.BeginDelta()
	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.MarkVertexKnown(vid1)
	pc.MarkVertexNotInTheState(vid1)
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
	pc.CommitDelta()
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	// Cycle 2: rollback
	pc.BeginDelta()
	vid2 := WrapTxID(base.RandomTransactionID(false, 4, base.T(1002, 50)))
	pc.MarkVertexKnown(vid2)
	pc.MarkVertexNotInTheState(vid2)
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
	pc.RollbackDelta()
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	// Cycle 3: commit
	pc.BeginDelta()
	vid3 := WrapTxID(base.RandomTransactionID(false, 5, base.T(1003, 50)))
	pc.MarkVertexKnown(vid3)
	pc.MarkVertexNotInTheState(vid3)
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
	pc.CommitDelta()
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
}

// TestAttachmentCostMixedVertexTypes tests mixed sequencer and non-sequencer vertices.
func TestAttachmentCostMixedVertexTypes(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Add non-sequencer vertex
	nonSeqVid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.MarkVertexKnown(nonSeqVid)
	pc.MarkVertexNotInTheState(nonSeqVid)
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	// Add sequencer vertex - should not change cost (sequencers excluded)
	seqVid := WrapTxID(base.RandomTransactionID(true, 4, base.T(1002, 50)))
	pc.MarkVertexKnown(seqVid)
	pc.MarkVertexNotInTheState(seqVid)

	// Cost should still match direct calculation
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	// Begin delta and add more
	pc.BeginDelta()

	nonSeqVid2 := WrapTxID(base.RandomTransactionID(false, 2, base.T(1003, 50)))
	pc.MarkVertexKnown(nonSeqVid2)
	pc.MarkVertexNotInTheState(nonSeqVid2)
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	seqVid2 := WrapTxID(base.RandomTransactionID(true, 5, base.T(1004, 50)))
	pc.MarkVertexKnown(seqVid2)
	pc.MarkVertexNotInTheState(seqVid2)
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	pc.CommitDelta()
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
}

// TestAttachmentCostVertexInState tests that vertices marked "in state" don't contribute.
func TestAttachmentCostVertexInState(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Add vertex and mark as in-state (rooted)
	vid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	pc.SetFlagsUp(vid, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState|FlagPastConeVertexInTheState)

	// Vertex in state doesn't contribute to attachment cost
	require.Equal(t, 0, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
}

// TestAttachmentCostNestedDeltaNotAllowed verifies BeginDelta panics if delta already active.
func TestAttachmentCostNestedDeltaNotAllowed(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	pc.BeginDelta()

	// Nested BeginDelta should panic
	require.Panics(t, func() {
		pc.BeginDelta()
	})

	pc.RollbackDelta()
}

// TestAttachmentCostCommitWithoutDeltaPanics verifies CommitDelta panics if no delta active.
func TestAttachmentCostCommitWithoutDeltaPanics(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// CommitDelta without BeginDelta should panic
	require.Panics(t, func() {
		pc.CommitDelta()
	})
}

// TestAttachmentCostRollbackWithoutDeltaSafe verifies RollbackDelta is safe without delta.
func TestAttachmentCostRollbackWithoutDeltaSafe(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// RollbackDelta without BeginDelta should be safe (no-op)
	require.NotPanics(t, func() {
		pc.RollbackDelta()
	})

	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
}

// =============================================================================
// Attachment Cost Tests with Real Transactions
// =============================================================================
//
// These tests use real transactions created via utxodb to verify attachment cost
// calculations with non-zero costs. AttachmentCost = NumInputs + NumProducedOutputs
// for each non-sequencer transaction that is definitely NOT in the baseline state.

// createTestTransaction creates a real transaction using utxodb for testing.
// Returns a WrappedTx containing a real Vertex with non-zero AttachmentCost.
func createTestTransaction(t *testing.T, u *utxodb.UTXODB, addrIdx int) *WrappedTx {
	privKey, _, addr := u.GenerateAddress(addrIdx)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.Equal(t, 1, len(outs))

	ts := outs[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(privKey, nil, ts)
	require.NoError(t, err)

	_, _, addr2 := u.GenerateAddress(addrIdx + 1000)
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
	require.NoError(t, err)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	v := NewVertex(tx)
	vid := v.Wrap()

	return vid
}

// TestAttachmentCostWithRealTransaction tests attachment cost with a real transaction.
// Real transactions have AttachmentCost = NumInputs + NumProducedOutputs > 0.
func TestAttachmentCostWithRealTransaction(t *testing.T) {
	u := utxodb.NewUTXODB(pastConeTestGenesisKey, true)

	vid := createTestTransaction(t, u, 200)

	// Verify that the real transaction has non-zero attachment cost
	cost := vid.AttachmentCost()
	require.Greater(t, cost, 0, "Real transaction should have non-zero attachment cost")

	// AttachmentCost = NumInputs + NumProducedOutputs
	var numInputs, numOutputs int
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			numInputs = v.NumInputs()
			numOutputs = v.NumProducedOutputs()
		},
	})
	require.Equal(t, numInputs+numOutputs, cost)

	// Now test in a PastCone
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	pc.MarkVertexKnown(vid)
	pc.MarkVertexNotInTheState(vid)

	// Incremental and direct calculations should match
	require.Equal(t, cost, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
}

// TestAttachmentCostWithRealTransactionDeltaCommit tests delta commit with real transactions.
func TestAttachmentCostWithRealTransactionDeltaCommit(t *testing.T) {
	u := utxodb.NewUTXODB(pastConeTestGenesisKey, true)

	vid1 := createTestTransaction(t, u, 300)
	vid2 := createTestTransaction(t, u, 302)

	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Add first transaction to base
	pc.MarkVertexKnown(vid1)
	pc.MarkVertexNotInTheState(vid1)

	baseCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), baseCost)
	require.Greater(t, baseCost, 0)

	// Begin delta and add second transaction
	pc.BeginDelta()

	pc.MarkVertexKnown(vid2)
	pc.MarkVertexNotInTheState(vid2)

	deltaCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), deltaCost)
	require.Greater(t, deltaCost, baseCost)

	// Commit delta
	pc.CommitDelta()

	commitCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), commitCost)
	require.Equal(t, deltaCost, commitCost)
}

// TestAttachmentCostWithRealTransactionDeltaRollback tests delta rollback with real transactions.
func TestAttachmentCostWithRealTransactionDeltaRollback(t *testing.T) {
	u := utxodb.NewUTXODB(pastConeTestGenesisKey, true)

	vid1 := createTestTransaction(t, u, 400)
	vid2 := createTestTransaction(t, u, 402)

	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Add first transaction to base
	pc.MarkVertexKnown(vid1)
	pc.MarkVertexNotInTheState(vid1)

	baseCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), baseCost)
	require.Greater(t, baseCost, 0)

	// Begin delta and add second transaction
	pc.BeginDelta()

	pc.MarkVertexKnown(vid2)
	pc.MarkVertexNotInTheState(vid2)

	deltaCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), deltaCost)
	require.Greater(t, deltaCost, baseCost)

	// Rollback delta
	pc.RollbackDelta()

	rollbackCost := pc.AttachmentCost()
	require.Equal(t, pc.AttachmentCostDirect(), rollbackCost)
	require.Equal(t, baseCost, rollbackCost)
}

// TestAttachmentCostMultipleRealTransactions tests attachment cost with multiple real transactions.
func TestAttachmentCostMultipleRealTransactions(t *testing.T) {
	u := utxodb.NewUTXODB(pastConeTestGenesisKey, true)

	// Create multiple transactions
	vids := make([]*WrappedTx, 5)
	for i := 0; i < 5; i++ {
		vids[i] = createTestTransaction(t, u, 500+i*2)
	}

	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Add transactions one by one, checking invariant each time
	expectedCost := 0
	for i, vid := range vids {
		pc.MarkVertexKnown(vid)
		pc.MarkVertexNotInTheState(vid)

		expectedCost += vid.AttachmentCost()

		require.Equal(t, expectedCost, pc.AttachmentCost(), "After adding transaction %d", i)
		require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost(), "After adding transaction %d", i)
	}
}

// TestAttachmentCostMixedRealAndVirtual tests attachment cost with mixed real and virtual transactions.
func TestAttachmentCostMixedRealAndVirtual(t *testing.T) {
	u := utxodb.NewUTXODB(pastConeTestGenesisKey, true)

	realVid := createTestTransaction(t, u, 600)
	virtualVid := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))

	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Add real transaction
	pc.MarkVertexKnown(realVid)
	pc.MarkVertexNotInTheState(realVid)

	realCost := realVid.AttachmentCost()
	require.Equal(t, realCost, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	// Add virtual transaction (cost = 0)
	pc.MarkVertexKnown(virtualVid)
	pc.MarkVertexNotInTheState(virtualVid)

	// Cost should remain the same (virtual has 0 cost)
	require.Equal(t, realCost, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
}

// TestAttachmentCostAccumulationInDelta tests that attachment cost accumulates correctly in delta.
func TestAttachmentCostAccumulationInDelta(t *testing.T) {
	u := utxodb.NewUTXODB(pastConeTestGenesisKey, true)

	// Create transactions: 2 for base, 3 for delta
	baseVids := make([]*WrappedTx, 2)
	for i := 0; i < 2; i++ {
		baseVids[i] = createTestTransaction(t, u, 700+i*2)
	}

	deltaVids := make([]*WrappedTx, 3)
	for i := 0; i < 3; i++ {
		deltaVids[i] = createTestTransaction(t, u, 710+i*2)
	}

	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	// Add base transactions
	baseCost := 0
	for _, vid := range baseVids {
		pc.MarkVertexKnown(vid)
		pc.MarkVertexNotInTheState(vid)
		baseCost += vid.AttachmentCost()
	}
	require.Equal(t, baseCost, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	// Begin delta
	pc.BeginDelta()

	// Add delta transactions
	deltaCost := baseCost
	for _, vid := range deltaVids {
		pc.MarkVertexKnown(vid)
		pc.MarkVertexNotInTheState(vid)
		deltaCost += vid.AttachmentCost()

		require.Equal(t, deltaCost, pc.AttachmentCost())
		require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
	}

	// Commit delta
	pc.CommitDelta()

	require.Equal(t, deltaCost, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
}

// TestAttachmentCostComplexScenario tests a complex scenario with multiple deltas.
func TestAttachmentCostComplexScenario(t *testing.T) {
	u := utxodb.NewUTXODB(pastConeTestGenesisKey, true)

	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))

	addrIdx := 800

	// Step 1: Add transaction to base
	vid1 := createTestTransaction(t, u, addrIdx)
	addrIdx += 2
	pc.MarkVertexKnown(vid1)
	pc.MarkVertexNotInTheState(vid1)
	cost1 := vid1.AttachmentCost()
	require.Equal(t, cost1, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	// Step 2: Delta with transaction, then commit
	pc.BeginDelta()
	vid2 := createTestTransaction(t, u, addrIdx)
	addrIdx += 2
	pc.MarkVertexKnown(vid2)
	pc.MarkVertexNotInTheState(vid2)
	cost2 := cost1 + vid2.AttachmentCost()
	require.Equal(t, cost2, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
	pc.CommitDelta()
	require.Equal(t, cost2, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	// Step 3: Delta with transaction, then rollback
	pc.BeginDelta()
	vid3 := createTestTransaction(t, u, addrIdx)
	addrIdx += 2
	pc.MarkVertexKnown(vid3)
	pc.MarkVertexNotInTheState(vid3)
	cost3 := cost2 + vid3.AttachmentCost()
	require.Equal(t, cost3, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
	pc.RollbackDelta()
	require.Equal(t, cost2, pc.AttachmentCost()) // Back to cost2
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	// Step 4: Another delta with multiple transactions, then commit
	pc.BeginDelta()
	vid4 := createTestTransaction(t, u, addrIdx)
	addrIdx += 2
	pc.MarkVertexKnown(vid4)
	pc.MarkVertexNotInTheState(vid4)
	cost4a := cost2 + vid4.AttachmentCost()
	require.Equal(t, cost4a, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	vid5 := createTestTransaction(t, u, addrIdx)
	pc.MarkVertexKnown(vid5)
	pc.MarkVertexNotInTheState(vid5)
	cost4b := cost4a + vid5.AttachmentCost()
	require.Equal(t, cost4b, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())

	pc.CommitDelta()
	require.Equal(t, cost4b, pc.AttachmentCost())
	require.Equal(t, pc.AttachmentCostDirect(), pc.AttachmentCost())
}

// =============================================================================
// PastCone.Clone() Tests
// =============================================================================

// TestPastConeBaseClone tests that PastConeBase.Clone() deep copies all mutable state.
func TestPastConeBaseClone(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	vid2 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))

	pb := NewPastConeBase(&branchID)
	pb.vertices[vid1] = FlagPastConeVertexKnown | FlagPastConeVertexDefined
	pb.vertices[vid2] = FlagPastConeVertexKnown
	pb.addVirtuallyConsumedOutput(WrappedOutput{VID: vid1, Index: 0})
	pb.addVirtuallyConsumedOutput(WrappedOutput{VID: vid1, Index: 2})
	pb.attachmentCost = 42

	clone := pb.Clone()

	// Same content
	require.Equal(t, *pb.baselineBranchID, *clone.baselineBranchID)
	require.Equal(t, len(pb.vertices), len(clone.vertices))
	require.Equal(t, pb.vertices[vid1], clone.vertices[vid1])
	require.Equal(t, pb.vertices[vid2], clone.vertices[vid2])
	require.Equal(t, pb.attachmentCost, clone.attachmentCost)
	require.True(t, clone.virtuallyConsumed[vid1].Contains(byte(0)))
	require.True(t, clone.virtuallyConsumed[vid1].Contains(byte(2)))

	// Independence: mutate clone, original unaffected
	vid3 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1003, 50)))
	clone.vertices[vid3] = FlagPastConeVertexKnown
	require.Equal(t, 2, len(pb.vertices))
	require.Equal(t, 3, len(clone.vertices))

	clone.addVirtuallyConsumedOutput(WrappedOutput{VID: vid2, Index: 1})
	require.Equal(t, 1, len(pb.virtuallyConsumed))  // only vid1
	require.Equal(t, 2, len(clone.virtuallyConsumed)) // vid1 + vid2

	// Mutate original's virtuallyConsumed, clone unaffected
	pb.addVirtuallyConsumedOutput(WrappedOutput{VID: vid1, Index: 5})
	require.True(t, pb.virtuallyConsumed[vid1].Contains(byte(5)))
	require.False(t, clone.virtuallyConsumed[vid1].Contains(byte(5)))
}

// TestPastConeClone tests PastCone.Clone() preserves all state and is independent.
func TestPastConeClone(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "original", NewPastConeBase(&branchID))

	vid1 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1001, 50)))
	vid2 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1002, 50)))
	pc.MarkVertexKnown(vid1)
	pc.SetFlagsUp(vid1, FlagPastConeVertexDefined|FlagPastConeVertexInTheState)
	pc.MarkVertexKnown(vid2)
	pc.addVirtuallyConsumedOutput(WrappedOutput{VID: vid1, Index: 0})

	clone := pc.Clone("cloned")

	// Metadata
	require.Equal(t, "cloned", clone.name)
	require.Equal(t, pc.tip, clone.tip)
	require.Equal(t, pc.txTs, clone.txTs)
	require.Nil(t, clone.delta)

	// Content matches
	require.Equal(t, *pc.baselineBranchID, *clone.baselineBranchID)
	require.Equal(t, pc.Flags(vid1), clone.Flags(vid1))
	require.Equal(t, pc.Flags(vid2), clone.Flags(vid2))
	require.True(t, clone.isVirtuallyConsumed(WrappedOutput{VID: vid1, Index: 0}))
	require.Equal(t, pc.AttachmentCost(), clone.AttachmentCost())

	// Independence: mutate clone
	vid3 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1003, 50)))
	clone.MarkVertexKnown(vid3)
	require.False(t, pc.IsKnown(vid3))
	require.True(t, clone.IsKnown(vid3))

	// Independence: delta on clone doesn't affect original
	clone.BeginDelta()
	vid4 := WrapTxID(base.RandomTransactionID(false, 3, base.T(1004, 50)))
	clone.MarkVertexKnown(vid4)
	clone.CommitDelta()
	require.True(t, clone.IsKnown(vid4))
	require.False(t, pc.IsKnown(vid4))
}

// TestPastConeClonePanicsWithPendingDelta verifies Clone asserts no pending delta.
func TestPastConeClonePanicsWithPendingDelta(t *testing.T) {
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "test", NewPastConeBase(&branchID))
	pc.BeginDelta()

	require.Panics(t, func() {
		pc.Clone("should_panic")
	})

	pc.RollbackDelta()
}

// TestPastConeCloneWithRealTransactions tests Clone preserves attachment cost with real txs.
func TestPastConeCloneWithRealTransactions(t *testing.T) {
	u := utxodb.NewUTXODB(pastConeTestGenesisKey, true)

	vid1 := createTestTransaction(t, u, 900)
	vid2 := createTestTransaction(t, u, 902)

	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	tip := WrapTxID(tipTxID)
	ts := tipTxID.Timestamp()

	pc := newPastConeFromBase(nil, tip, ts, "original", NewPastConeBase(&branchID))

	pc.MarkVertexKnown(vid1)
	pc.MarkVertexNotInTheState(vid1)
	pc.MarkVertexKnown(vid2)
	pc.MarkVertexNotInTheState(vid2)
	pc.addVirtuallyConsumedOutput(WrappedOutput{VID: vid1, Index: 0})

	origCost := pc.AttachmentCost()
	require.Greater(t, origCost, 0)

	clone := pc.Clone("cloned")

	// Clone has same cost
	require.Equal(t, origCost, clone.AttachmentCost())
	require.Equal(t, clone.AttachmentCostDirect(), clone.AttachmentCost())

	// Adding to clone doesn't affect original
	vid3 := createTestTransaction(t, u, 904)
	clone.MarkVertexKnown(vid3)
	clone.MarkVertexNotInTheState(vid3)

	require.Equal(t, origCost, pc.AttachmentCost())
	require.Greater(t, clone.AttachmentCost(), origCost)
	require.Equal(t, clone.AttachmentCostDirect(), clone.AttachmentCost())
}
