package consolidator

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// The two deciders of the delegation mode read only a delegation's balance,
// whether the master can spend it now, and whether its target still serves
// it; the rest of ownDelegation is left empty here.

func dlg(balance uint64, consumable bool, stale string) *ownDelegation {
	return &ownDelegation{balance: balance, consumable: consumable, stale: stale}
}

const size = 10_000 * prox

// Placement is an order of attempts: the delegations below the target size,
// smallest first, frozen or not (a frozen one is topped up through its
// target); then a new one (nil) while the count is under the target; then the
// rest, smallest first. The consolidator takes the first that can be made.
func TestPlacementOrder(t *testing.T) {
	// nothing yet: create
	require.Equal(t, []*ownDelegation{nil}, placementOrder(nil, 5, size))

	// one small delegation: it grows before a second is started
	small := dlg(100*prox, true, "")
	require.Equal(t, []*ownDelegation{small, nil}, placementOrder([]*ownDelegation{small}, 5, size))

	// the smallest one below the size comes first, frozen or not
	smaller := dlg(50*prox, true, "")
	frozen := dlg(10*prox, false, "")
	require.Equal(t, []*ownDelegation{frozen, smaller, small, nil}, placementOrder([]*ownDelegation{small, frozen, smaller}, 5, size))

	// all at size and under the count: create first, then the smallest
	full := dlg(size, true, "")
	fuller := dlg(size+1, true, "")
	require.Equal(t, []*ownDelegation{nil, full, fuller}, placementOrder([]*ownDelegation{fuller, full}, 5, size))

	// at the count: no creation, smallest first across the size boundary
	frozenBig := dlg(size+9, false, "")
	require.Equal(t, []*ownDelegation{small, full, frozenBig}, placementOrder([]*ownDelegation{frozenBig, full, small}, 3, size))
}

// A stale delegation too small to stand on its own is folded into the
// largest other consumable delegation; frozen ones and itself never qualify.
func TestLargestConsumableOther(t *testing.T) {
	tiny := dlg(5*prox, true, "sequencer not active")
	big := dlg(500*prox, true, "")
	mid := dlg(200*prox, true, "")
	frozenBig := dlg(900*prox, false, "")
	require.Same(t, big, largestConsumableOther([]*ownDelegation{tiny, mid, frozenBig, big}, tiny))
	require.Nil(t, largestConsumableOther([]*ownDelegation{tiny, frozenBig}, tiny))
	require.Nil(t, largestConsumableOther([]*ownDelegation{tiny}, tiny))
}

// Tidying: above the target count the smallest consumable delegation is folded
// into the largest consumable one; otherwise a stale consumable one is
// re-delegated; frozen delegations are never touched.
func TestPickManagement(t *testing.T) {
	big := dlg(500*prox, true, "")
	mid := dlg(200*prox, true, "sequencer keeps more")
	tiny := dlg(10*prox, true, "")
	frozenTiny := dlg(1*prox, false, "")
	frozenBig := dlg(900*prox, false, "")

	// over the count: fold tiny into big, whatever the frozen ones hold
	into, kill, re := pickManagement([]*ownDelegation{frozenBig, mid, big, tiny, frozenTiny}, 3)
	require.Same(t, big, into)
	require.Same(t, tiny, kill)
	require.Nil(t, re)

	// over the count but only one consumable: nothing to fold, so the stale one is re-delegated
	into, kill, re = pickManagement([]*ownDelegation{frozenBig, mid, frozenTiny}, 2)
	require.Nil(t, into)
	require.Nil(t, kill)
	require.Same(t, mid, re)

	// at the count: no folding, the stale one is re-delegated
	_, _, re = pickManagement([]*ownDelegation{big, mid, tiny}, 3)
	require.Same(t, mid, re)

	// at the count with nothing stale: nothing
	into, kill, re = pickManagement([]*ownDelegation{big, tiny, frozenBig}, 3)
	require.Nil(t, into)
	require.Nil(t, kill)
	require.Nil(t, re)

	// a stale but frozen delegation waits for its target
	_, _, re = pickManagement([]*ownDelegation{dlg(size, false, "sequencer not active")}, 5)
	require.Nil(t, re)
}

// The not-frozen timeout counts from the first slot in which the target can
// freeze the delegation: right after its safe revocation window for a freeze
// that has run out, the slot after the output otherwise. Inside the window only
// the master can act, so an expired freeze must not be moved there just because
// the transaction that froze it is old. A delegation on hold is stale at once:
// its target can never freeze it again.
func TestClassifyFreezeTimeout(t *testing.T) {
	c := &txbuildercore.Constants{DelegationEpochSlots: 600, SafeRevocationSlots: 60}
	k := &Consolidator{consts: c}
	target := base.ChainID{} // epoch offset 0: epoch e ends at slot 600*e
	market := map[base.ChainID]txbuildercore.SequencerCandidate{target: {ID: target, ShareLeft: 900}}

	const outSlot = 10_000
	classify := func(state byte, lastFrozenEpoch, slot uint32) *ownDelegation {
		d := &ownDelegation{
			view: &txbuildercore.DelegationOutputView{OriginSlot: outSlot, Target: target, State: state, LastFrozenEpoch: lastFrozenEpoch},
			oid:  base.MustNewOutputID(base.RandomTransactionID(false, 2, base.T(outSlot, 50)), 0),
		}
		k.classify([]*ownDelegation{d}, market, slot)
		return d
	}

	// frozen until the end of epoch 20 (slot 12_000), unfreezes in 12_001,
	// target can freeze it again from 12_061, stale after 12_121
	const lastEpoch = 20
	require.False(t, classify(txbuildercore.DelegateLockStateFrozen, lastEpoch, 12_000).consumable, "still frozen")
	inWindow := classify(txbuildercore.DelegateLockStateFrozen, lastEpoch, 12_030)
	require.True(t, inWindow.consumable)
	require.Empty(t, inWindow.stale, "inside the window the target has had no chance to freeze it")
	require.Empty(t, classify(txbuildercore.DelegateLockStateFrozen, lastEpoch, 12_121).stale)
	require.NotEmpty(t, classify(txbuildercore.DelegateLockStateFrozen, lastEpoch, 12_122).stale)

	// never frozen: freezable from the slot after the output
	require.Empty(t, classify(0, 0, outSlot+61).stale)
	require.NotEmpty(t, classify(0, 0, outSlot+62).stale)

	// on hold: stale at once
	require.NotEmpty(t, classify(txbuildercore.DelegateLockStateOnHold, 0, outSlot+1).stale)
}

// Under the ledger's minimum sequencer balance a sequencer cannot freeze a delegation
// afresh. The consolidator neither draws it nor accepts it as a pinned target for a new
// delegation, treats an unfrozen delegation at it as stale, so it is re-delegated, and
// leaves a frozen one alone: its target still tops it up, and it moves once it expires.
func TestConsolidatorUnderMinimumSequencerBalance(t *testing.T) {
	const minBalance = 10_000 * prox
	rich := target(1, 600, 50_000*prox)
	poor := target(2, 600, 5_000*prox)
	rich.MinBalance, poor.MinBalance = minBalance, minBalance
	active := []txbuildercore.SequencerCandidate{rich, poor}

	// only the one that can freeze is drawn: a single ticket
	got, err := SelectDelegationTarget(active, nil, 100*prox, func(n int) int {
		require.Equal(t, 1, n)
		return 0
	})
	require.NoError(t, err)
	require.Equal(t, rich.ID, got.ID)
	_, err = SelectDelegationTarget(active, &poor.ID, 100*prox, nil)
	require.ErrorContains(t, err, "minimum sequencer balance")

	c := &txbuildercore.Constants{DelegationEpochSlots: 600, SafeRevocationSlots: 60}
	k := &Consolidator{consts: c}
	market := map[base.ChainID]txbuildercore.SequencerCandidate{rich.ID: rich, poor.ID: poor}
	const outSlot = 10_000
	classify := func(to base.ChainID, state byte, lastFrozenEpoch uint32) *ownDelegation {
		d := &ownDelegation{
			view: &txbuildercore.DelegationOutputView{OriginSlot: outSlot, Target: to, State: state, LastFrozenEpoch: lastFrozenEpoch},
			oid:  base.MustNewOutputID(base.RandomTransactionID(false, 2, base.T(outSlot, 50)), 0),
		}
		k.classify([]*ownDelegation{d}, market, outSlot+5)
		return d
	}
	require.Contains(t, classify(poor.ID, 0, 0).stale, "minimum sequencer balance", "unfrozen at a poor target: re-delegated")
	require.Empty(t, classify(rich.ID, 0, 0).stale, "unfrozen at a rich target: waits to be frozen")
	frozen := classify(poor.ID, txbuildercore.DelegateLockStateFrozen, 20) // frozen until slot 12_000
	require.False(t, frozen.consumable, "frozen at a poor target: left alone")
}

// A frozen delegation at an active sequencer under the minimum sequencer balance is
// asked to stop early, once: that sequencer can never freeze it again, and only it can
// take the request. Left alone: an unfrozen one (the stale rule moves it), one on hold,
// one at a sequencer that can freeze, one at an inactive sequencer (it cannot take the
// request), one already asked, one about to unfreeze.
func TestAskStopCandidate(t *testing.T) {
	c := &txbuildercore.Constants{DelegationEpochSlots: 600, SafeRevocationSlots: 60}
	rich := target(1, 600, 50_000*prox)
	poor := target(2, 600, 5_000*prox)
	gone := target(3, 600, 5_000*prox)
	rich.MinBalance, poor.MinBalance, gone.MinBalance = 10_000*prox, 10_000*prox, 10_000*prox
	market := map[base.ChainID]txbuildercore.SequencerCandidate{rich.ID: rich, poor.ID: poor} // gone is not active

	const slot = 10_000
	frozen := func(to base.ChainID, idByte byte, lastEpoch uint32) *ownDelegation {
		id := base.ChainID{}
		id[0] = idByte
		return &ownDelegation{view: &txbuildercore.DelegationOutputView{ChainID: id, Target: to,
			State: txbuildercore.DelegateLockStateFrozen, LastFrozenEpoch: lastEpoch}}
	}
	atPoor := frozen(poor.ID, 1, 20) // frozen until slot 12_000
	asked := map[base.ChainID]uint32{}

	require.Same(t, atPoor, askStopCandidate([]*ownDelegation{frozen(rich.ID, 2, 20), atPoor}, market, asked, slot, c))
	require.Nil(t, askStopCandidate([]*ownDelegation{frozen(gone.ID, 3, 20)}, market, asked, slot, c), "an inactive target cannot take it")
	require.Nil(t, askStopCandidate([]*ownDelegation{{view: atPoor.view, consumable: true}}, market, asked, slot, c), "an unfrozen one is the stale rule's")
	onHold := frozen(poor.ID, 4, 20)
	onHold.view.State = txbuildercore.DelegateLockStateOnHold
	require.Nil(t, askStopCandidate([]*ownDelegation{onHold}, market, asked, slot, c), "already on hold")
	require.Nil(t, askStopCandidate([]*ownDelegation{atPoor}, market, asked, 11_998, c), "about to unfreeze")
	asked[atPoor.view.ChainID] = slot
	require.Nil(t, askStopCandidate([]*ownDelegation{atPoor}, market, asked, slot+1, c), "asked already")
}

// The fee of a stop request comes from the wallet's outputs, largest first, leaving no
// change or change at least the storage deposit; nil when the wallet cannot pay it.
func TestFeeInputs(t *testing.T) {
	const fee, floor = 1 * prox, 3 * prox
	got, total := feeInputs([]*ledger.OutputWithID{out(10*prox, 1), out(2*prox, 2)}, fee, floor)
	require.Len(t, got, 1)
	require.EqualValues(t, 10*prox, total)
	got, _ = feeInputs([]*ledger.OutputWithID{out(1*prox, 1)}, fee, floor)
	require.Len(t, got, 1, "exactly the fee, no change")
	got, _ = feeInputs([]*ledger.OutputWithID{out(2*prox, 1), out(1*prox, 2)}, fee, floor)
	require.Nil(t, got, "3 PROX cannot pay 1 and keep a change of at least 3")
	got, _ = feeInputs(nil, fee, floor)
	require.Nil(t, got)
}
