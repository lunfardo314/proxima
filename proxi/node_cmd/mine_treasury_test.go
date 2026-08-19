package node_cmd

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The arithmetic behind the miner's treasury loop: how much it delegates (D),
// how much it keeps liquid (W), and what comes back as change. The loop itself
// needs a node, but these rules decide every action it takes, so they are worth
// pinning on their own.

const tstReserve = 100_000_000 // W: 100 PROX

// treasuryMiner is a miner carrying the real ledger constants (the sizing rules
// read A and the wall-clock slot off them) and nothing else.
func treasuryMiner(t *testing.T, delegateAmount uint64) *miner {
	t.Helper()
	return &miner{
		consts:         ledger.ConstantsFromLibrary(ledger.L(base.MaxSlot).Library),
		delegateAmount: delegateAmount,
		reserve:        tstReserve,
	}
}

// out is a claimable output stand-in carrying only an amount and an ID; the
// sizing rules never look at the lock.
func out(amount uint64, idByte byte) *ledger.OutputWithID {
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(amount)
	})
	id := base.OutputID{}
	id[len(id)-1] = idByte
	return &ledger.OutputWithID{ID: id, Output: o}
}

// D defaults to ten mine rewards and follows A rather than being frozen at
// startup, because A grows with the slot once the ramp begins.
func TestDelegateAmountDefaultsToTenRewards(t *testing.T) {
	m := treasuryMiner(t, 0)
	require.EqualValues(t, 10*m.currentA(), m.delegateAmountNow())
	require.EqualValues(t, 42, treasuryMiner(t, 42).delegateAmountNow())
}

// The reserve is what makes the trigger D+W rather than D: a miner that
// delegated down to D exactly would have nothing left to pay the tag-along fee
// of its own next compaction, and freshly delegated capital is frozen.
func TestDelegateTriggerRequiresAmountPlusReserve(t *testing.T) {
	m := treasuryMiner(t, 0)
	d := m.delegateAmountNow()
	const fee = 10_000

	// At exactly the trigger the action leaves the whole reserve behind, less
	// the fee it just paid.
	change, ok := changeAfter(d+m.reserve, d, fee)
	require.True(t, ok)
	require.EqualValues(t, m.reserve-fee, change)

	// A mote short of it the change would dip below the reserve - which is what
	// the +W in the trigger exists to prevent, since the transaction would
	// otherwise still balance and go through.
	change, ok = changeAfter(d+m.reserve-1, d, fee)
	require.True(t, ok)
	require.Less(t, change, m.reserve-fee)
}

// A set that does not cover D plus the fee defers the action instead of
// building a transaction that cannot balance.
func TestChangeAfterShortSet(t *testing.T) {
	_, ok := changeAfter(1_000, 1_000, 1)
	require.False(t, ok)

	// Exactly covering leaves no change output at all.
	change, ok := changeAfter(1_001, 1_000, 1)
	require.True(t, ok)
	require.Zero(t, change)
}

// Over the per-transaction input cap the miner takes the largest outputs. The
// dust it leaves behind is not stranded: an action always drops the count, so
// the next tick's set is small enough to include it.
func TestLargestOutputsKeepsBiggest(t *testing.T) {
	outs := []*ledger.OutputWithID{out(1, 1), out(500, 2), out(7, 3), out(300, 4)}
	capped := largestOutputs(outs, 2)
	require.Len(t, capped, 2)
	require.EqualValues(t, 500, capped[0].Output.TokenBalance())
	require.EqualValues(t, 300, capped[1].Output.TokenBalance())
	require.EqualValues(t, 800, sumBalance(capped))
}

// How the loop tells a settled action from one still in flight: the LRB
// snapshot keeps returning an output until the transaction spending it settles,
// so any of the consumed IDs still showing up means "do not spend these twice".
func TestAnyPresentDetectsUnsettledAction(t *testing.T) {
	consumed := outputIDs([]*ledger.OutputWithID{out(1, 1), out(2, 2)})

	require.True(t, anyPresent([]*ledger.OutputWithID{out(1, 1), out(9, 9)}, consumed))
	require.False(t, anyPresent([]*ledger.OutputWithID{out(9, 9)}, consumed))
	require.False(t, anyPresent(nil, consumed))
}
