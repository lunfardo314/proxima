package vertex

import (
	"context"
	"errors"
	"testing"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/stretchr/testify/require"
)

// shutdownRecorder is a node global that records the reason of a graceful shutdown instead of
// stopping the process, so a test can check that a shutdown was asked for and why.
type shutdownRecorder struct {
	*global.Global
	reasons []string
}

func (l *shutdownRecorder) GracefulShutdown(reason string) {
	l.reasons = append(l.reasons, reason)
}

// TestMissingBaselineStateStopsNode checks that the past-cone checks reading the baseline state
// (CheckConflicts, CheckAndClean, CoverageDeltaRaw) do not dereference a missing state: each asks
// for an orderly node shutdown whose reason names the baseline and carries the past cone, and returns
// ErrBaselineStateNotAvailable, an error the attacher treats as no verdict on the transaction.
func TestMissingBaselineStateStopsNode(t *testing.T) {
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	noState := func(base.TransactionID) multistate.StateReader { return nil }

	requireStopped := func(name string, run func(pc *PastCone) error) {
		node := &shutdownRecorder{Global: global.NewDefault()}
		pc := NewPastCone(node, WrapTxID(tipTxID), tipTxID.Timestamp(), "test")
		pc.SetBaseline(&branchID)

		err := run(pc)
		require.True(t, errors.Is(err, ErrBaselineStateNotAvailable), "%s: unexpected error %v", name, err)
		require.Len(t, node.reasons, 1, "%s: one graceful shutdown expected", name)
		require.Contains(t, node.reasons[0], branchID.StringShort(), "%s: the reason names the baseline", name)
		require.Contains(t, node.reasons[0], "---- past cone ----", "%s: the reason carries the past cone", name)
	}
	requireStopped("CheckConflicts", func(pc *PastCone) error {
		conflict, err := pc.CheckConflicts(context.Background(), noState)
		require.Nil(t, conflict)
		return err
	})
	requireStopped("CheckAndClean", func(pc *PastCone) error {
		conflict, pending, err := pc.CheckAndClean(context.Background(), noState)
		require.Nil(t, conflict)
		require.Nil(t, pending)
		return err
	})
	requireStopped("CoverageDeltaRaw", func(pc *PastCone) error {
		_, err := pc.CoverageDeltaRaw(context.Background(), noState)
		return err
	})
}
