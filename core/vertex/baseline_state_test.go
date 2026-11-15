package vertex

import (
	"context"
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util/lazyargs"
	"github.com/stretchr/testify/require"
)

// panickingAssertLogging reports an assertion failure as a panic, so a test can observe it: the
// node's own Assertf exits the process with a crash log.
type panickingAssertLogging struct {
	*global.Global
}

func (l panickingAssertLogging) Assertf(cond bool, format string, args ...any) {
	if !cond {
		panic(fmt.Sprintf(format, lazyargs.Eval(args...)...)) // lazy arguments, as the node's Assertf
	}
}

// TestMissingBaselineStateAsserted checks that the past-cone checks reading the baseline state stop
// at an assertion naming the baseline when the state is not available, instead of dereferencing a
// nil reader: CheckConflicts, CheckAndClean and CoverageDeltaRaw.
func TestMissingBaselineStateAsserted(t *testing.T) {
	tipTxID := base.RandomTransactionID(true, 5, base.T(1010, 50))
	branchID := base.RandomTransactionID(true, 2, base.T(1000, 0))
	noState := func(base.TransactionID) multistate.StateReader { return nil }

	newPC := func() *PastCone {
		pc := NewPastCone(panickingAssertLogging{global.NewDefault()}, WrapTxID(tipTxID), tipTxID.Timestamp(), "test")
		pc.SetBaseline(&branchID)
		return pc
	}
	want := fmt.Sprintf("baseline state %s is not available", branchID.StringShort())
	requirePanicsWith := func(f func()) {
		defer func() {
			r := recover()
			require.NotNil(t, r)
			require.Contains(t, fmt.Sprint(r), want)
		}()
		f()
	}
	requirePanicsWith(func() { _, _ = newPC().CheckConflicts(context.Background(), noState) })
	requirePanicsWith(func() { _, _, _ = newPC().CheckAndClean(context.Background(), noState) })
	requirePanicsWith(func() { _, _ = newPC().CoverageDeltaRaw(context.Background(), noState) })
}
