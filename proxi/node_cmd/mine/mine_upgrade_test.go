package mine

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// The miner across a library upgrade (kb/library_upgrade1.md): it mines every
// version from minMinerVersion to its own, each by its own rules, taken from the
// constants of the slot it builds for.

// This build mines versions 4 to 6 and refuses anything else.
func TestMinerVersionSupported(t *testing.T) {
	require.False(t, minerVersionSupported(3))
	require.True(t, minerVersionSupported(4))
	require.True(t, minerVersionSupported(5))
	require.True(t, minerVersionSupported(6))
	require.False(t, minerVersionSupported(7))
}

// Version 4 stamps a transit at tick 1, as on the running network; version 5 at
// the settlement tick minus the sequencer pace, 83 with the production timing.
func TestTransitTick(t *testing.T) {
	f := newVerifyFixture(t, 6)
	v4, v5 := *f.m.consts, *f.m.consts
	v4.MinerVersion, v5.MinerVersion = 4, 5
	require.EqualValues(t, 1, transitTick(&v4))
	require.EqualValues(t, v5.MineSettlementTick()-v5.TransactionPaceSequencer, transitTick(&v5))
	require.EqualValues(t, 83, transitTick(&v5))
}

// A transit takes the rules of its own slot: built for a slot whose library
// requires version 5 it carries version 5, the transaction version of that
// library and the late tick; for a slot under the old library, version 4 and
// tick 1. Across the upgrade nothing has to happen to the miner.
func TestTransitFollowsSlotRules(t *testing.T) {
	f := newVerifyFixture(t, 6)
	predSlot := f.pred.oid.Timestamp().Slot
	old, upgraded := *f.m.consts, *f.m.consts
	old.MinerVersion, old.UpgradeIndex = 4, 0
	upgraded.MinerVersion, upgraded.UpgradeIndex, upgraded.UpgradeSlot = 5, 1, predSlot+2
	f.m.slotConsts = map[uint32]*txbuildercore.Constants{predSlot + 1: &old, predSlot + 2: &upgraded}

	for _, c := range []struct {
		slot    uint32
		version uint64
		index   uint16
		tick    uint8
	}{
		{predSlot + 1, 4, 0, 1},
		{predSlot + 2, 5, 1, 83},
	} {
		succB, succC := f.m.consts.MineRetarget(f.pred.ml.B, f.pred.ml.C, predSlot, c.slot)
		txb, _ := f.m.buildTransit(f.pred, c.slot, succB, succC, f.m.fullPayoutTo(f.m.tagAlongSeqID, c.slot))
		require.EqualValues(t, c.index, txb.TxData.UpgradeIndex)
		require.EqualValues(t, c.tick, txb.TxData.Timestamp.Tick)
		succ, err := txbuildercore.OutputFromBytes(txb.TxData.OutputBytes[0])
		require.NoError(t, err)
		ml, err := f.m.lib.ParseMineLock(succ.MustConstraintAt(txbuildercore.ConstraintIndexLock))
		require.NoError(t, err)
		require.EqualValues(t, c.version, ml.V)
	}
}

// A top-up reward takes the guarantor form of its slot: the 24-byte form while the
// library requires version 5, the strict form from version 6 on, which the ledger
// then demands for the full reward (kb/library_upgrade6.md).
func TestTopUpFormFollowsSlotRules(t *testing.T) {
	f := newVerifyFixture(t, 6)
	predSlot := f.pred.oid.Timestamp().Slot
	v5, v6 := *f.m.consts, *f.m.consts
	v5.MinerVersion = 5
	v6.MinerVersion = 6
	f.m.slotConsts = map[uint32]*txbuildercore.Constants{predSlot + 1: &v5, predSlot + 2: &v6}

	delegationID := base.ChainID{7}
	standard, err := f.m.lib.NewEnsureTopUpDelegationConstraint(delegationID)
	require.NoError(t, err)
	strict, err := f.m.lib.NewEnsureStrictTopUpDelegationConstraint(delegationID)
	require.NoError(t, err)

	for _, c := range []struct {
		slot uint32
		want []byte
	}{
		{predSlot + 1, standard},
		{predSlot + 2, strict},
	} {
		succB, succC := f.m.consts.MineRetarget(f.pred.ml.B, f.pred.ml.C, predSlot, c.slot)
		p := minePayout{option: txbuildercore.MinePayoutFull, minted: f.m.consts.MineAmountAtSlot(c.slot), target: f.m.tagAlongSeqID, topUp: true, delegationID: delegationID}
		txb, _ := f.m.buildTransit(f.pred, c.slot, succB, succC, p)
		reward, err := txbuildercore.OutputFromBytes(txb.TxData.OutputBytes[1])
		require.NoError(t, err)
		require.Equal(t, 5, reward.NumElements())
		require.Equal(t, c.want, reward.MustConstraintAt(4))
	}
}
