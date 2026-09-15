package consolidate

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The planning rules of kb/consolidate.md decide every transaction the
// consolidator builds: when it acts, which outputs it consumes, and how their
// total splits between what the wallet keeps and what moves out. The loop
// itself needs a node; the rules are pure and pinned here.

const (
	prox    = 1_000_000 // one base token in motes, the fold-below threshold
	minimum = 100 * prox
)

// out is a consolidatable output stand-in carrying only an amount and an ID;
// the planning rules never look at the lock.
func out(amount uint64, idByte byte) *ledger.OutputWithID {
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(amount)
	})
	id := base.OutputID{}
	id[len(id)-1] = idByte
	return &ledger.OutputWithID{ID: id, Output: o}
}

func amounts(outs []*ledger.OutputWithID) []uint64 {
	ret := make([]uint64, len(outs))
	for i, o := range outs {
		ret[i] = o.Output.TokenBalance()
	}
	return ret
}

// Below twice the minimum and below the output-count threshold there is
// nothing to do, whatever the wallet holds.
func TestPlanNothingBelowBothTriggers(t *testing.T) {
	outs := []*ledger.OutputWithID{out(150*prox, 1), out(40*prox, 2)}
	require.Nil(t, planConsolidation(outs, minimum, 30, 10, prox))
}

// At twice the minimum everything above the minimum moves; the smallest
// outputs are consumed first.
func TestPlanMovesAboveMinimumSmallestFirst(t *testing.T) {
	outs := []*ledger.OutputWithID{out(120*prox, 1), out(30*prox, 2), out(50*prox, 3)}
	p := planConsolidation(outs, minimum, 30, 10, prox)
	require.NotNil(t, p)
	require.Equal(t, []uint64{30 * prox, 50 * prox, 120 * prox}, amounts(p.inputs))
	require.EqualValues(t, 200*prox, p.consumed)
	require.EqualValues(t, 100*prox, p.kept)
	require.EqualValues(t, 100*prox, p.moved)
}

// The minimum is a property of the whole account: what the input cap leaves
// unconsumed counts toward it, so the consumed set can move in full.
func TestPlanUnconsumedCountsTowardMinimum(t *testing.T) {
	outs := []*ledger.OutputWithID{out(300*prox, 1), out(10*prox, 2), out(20*prox, 3)}
	p := planConsolidation(outs, minimum, 2, 10, prox)
	require.NotNil(t, p)
	require.Equal(t, []uint64{10 * prox, 20 * prox}, amounts(p.inputs))
	require.EqualValues(t, 0, p.kept)
	require.EqualValues(t, 30*prox, p.moved)
}

// A pile of outputs is compacted even when the total is under twice the
// minimum, and then nothing moves.
func TestPlanCountTriggerOnlyCompacts(t *testing.T) {
	outs := make([]*ledger.OutputWithID, 0, 10)
	for i := byte(0); i < 10; i++ {
		outs = append(outs, out(15*prox, i))
	}
	p := planConsolidation(outs, minimum, 30, 10, prox)
	require.NotNil(t, p)
	require.Len(t, p.inputs, 10)
	require.EqualValues(t, 150*prox, p.kept)
	require.EqualValues(t, 0, p.moved)
}

// A remainder under one base token is not worth an output of its own: it is
// folded into what moves. Likewise a movable amount under one base token stays.
func TestPlanFoldsTinyAmounts(t *testing.T) {
	// the input cap leaves the largest output (99.5 PROX) unconsumed, so kept
	// would be 100 - 99.5 = 0.5 PROX
	outs := []*ledger.OutputWithID{out(99*prox+prox/2, 1), out(50*prox, 2), out(60*prox, 3)}
	p := planConsolidation(outs, minimum, 2, 1000, prox)
	require.NotNil(t, p)
	require.EqualValues(t, 110*prox, p.consumed)
	require.EqualValues(t, 0, p.kept)
	require.EqualValues(t, p.consumed, p.moved)

	// the cap consumes only two dust outputs while the rest covers the
	// minimum, so moved would be 0.5 PROX: it stays, as a compaction
	outs = []*ledger.OutputWithID{out(prox/4, 1), out(prox/4, 2), out(500*prox, 3)}
	p = planConsolidation(outs, minimum, 2, 1000, prox)
	require.NotNil(t, p)
	require.EqualValues(t, prox/2, p.consumed)
	require.EqualValues(t, 0, p.moved)
	require.EqualValues(t, p.consumed, p.kept)
}

// One input going straight back to the wallet achieves nothing and is never
// planned, so an already consolidated account costs nothing per tick.
func TestPlanSkipsPointlessSingleInput(t *testing.T) {
	require.Nil(t, planConsolidation([]*ledger.OutputWithID{out(150*prox, 1)}, minimum, 30, 1, prox))
	// but a single input with something to move is a transaction
	p := planConsolidation([]*ledger.OutputWithID{out(250*prox, 1)}, minimum, 30, 10, prox)
	require.NotNil(t, p)
	require.EqualValues(t, 150*prox, p.moved)
}

func target(idByte byte, tolerance uint16) delegationTarget {
	id := base.ChainID{}
	id[len(id)-1] = idByte
	return delegationTarget{id: id, tolerance: tolerance}
}

// A pinned target is used only when it is active and leaves the required cut;
// a random draw takes any active sequencer that leaves it, and says what the
// network offers when none does.
func TestSelectDelegationTarget(t *testing.T) {
	active := []delegationTarget{target(1, 950), target(2, 800)}

	id, err := selectDelegationTarget(active, nil, 900)
	require.NoError(t, err)
	require.Equal(t, active[0].id, id)

	_, err = selectDelegationTarget(active, nil, 960)
	require.ErrorContains(t, err, "widest is 950")

	_, err = selectDelegationTarget(nil, nil, 900)
	require.ErrorContains(t, err, "no sequencer has been active")

	pinned := active[1].id
	_, err = selectDelegationTarget(active, &pinned, 900)
	require.ErrorContains(t, err, "leaves delegators 800")
	id, err = selectDelegationTarget(active, &pinned, 800)
	require.NoError(t, err)
	require.Equal(t, pinned, id)

	absent := target(3, 1000).id
	_, err = selectDelegationTarget(active, &absent, 0)
	require.ErrorContains(t, err, "no milestone")
}
