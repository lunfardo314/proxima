package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// Library upgrade 3, delegation part (kb/library_upgrade3.md): from the upgrade slot a
// delegation is produced frozen only by its target, from a delegation, and the honest
// life of a delegation is unchanged. The VRF part is tested in tests/upgrade3_*.

// upgrade3Slot is where these tests activate upgrade 3: past upgrade 2's slot by at
// least the minimum spacing between upgrades.
const upgrade3Slot = 210

// withUpgrade3 lists upgrades 1 to 3, applies them to the test ledger's store as a
// node does at start, and restores the package ledger afterwards.
func withUpgrade3(t *testing.T, td *testData) {
	t.Helper()
	ledger.Upgrades = []*ledger.UpgradeDefinition{
		{Slot: upgrade1Slot, Build: ledger.BuildUpgrade1Library},
		{Slot: upgrade2Slot, Build: ledger.BuildUpgrade2Library},
		{Slot: upgrade3Slot, Build: ledger.BuildUpgrade3Library},
	}
	t.Cleanup(func() {
		ledger.Upgrades = nil
		initTestLedger()
	})
	restartNode(t, td.u)
}

// newUpgrade3Env is the delegation environment with upgrade 3 listed.
func newUpgrade3Env(t *testing.T) *testData {
	td := &testData{T: t}
	td.init()
	withUpgrade3(t, td)
	return td
}

// frozenFrom is a frozen delegateLockState lasting the maximum freeze from the
// epoch of ts.
func (td *testData) frozenFrom(ts base.LedgerTime) ledger.DelegateLockState {
	lib := ledger.L(ts.Slot)
	txEpoch := lib.EpochFromSlotDirect(td.target, ts.Slot, lib.DelegationEpochSlots)
	return ledger.DelegateLockState{
		LastFrozenEpoch: txEpoch + uint32(lib.DelegationMaxFrozenEpochs) - 1,
		State:           ledger.DelegateLockStateFrozen,
	}
}

// freezeFromPlainChain: the master makes a plain sigLock chain at ts and a slot later
// turns it into a delegation to the sequencer, frozen. Returns the validation error.
func (td *testData) freezeFromPlainChain(ts base.LedgerTime) error {
	const amount = 2 * seqOnChainBalance

	par, err := td.u.MakeTransferInputData(td.masterPrivateKey, nil, ts)
	require.NoError(td, err)
	outs, err := td.u.DoTransferOutputs(par.
		WithAmount(amount).
		WithTargetLock(td.masterAddr).
		WithConstraint(ledger.NewChainOrigin(ts.Slot)))
	require.NoError(td, err)
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(td, err)
	x := chOuts[0]
	xcc := x.Output.ChainConstraint()

	tsFreeze := x.Timestamp().AddSlots(1)
	txb := exhelp.New()
	_, err = txb.ConsumeOutput(x.Output, x.ID)
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount))
		o.WithLock(ledger.NewDelegateLock(td.target, base.HolderID(td.masterAddr), 0))
		o.PutConstraint(ledger.NewChainConstraint(x.ChainID, 0, xcc.OriginSlot, 0, 0, xcc.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(td.frozenFrom(tsFreeze).Bytes())
	}))
	require.NoError(td, err)
	return td.addMasterFreeze(txb, tsFreeze, x.ChainID)
}

// freezeByMaster: the master makes an unfrozen delegation at ts and a slot later
// unlocks it as master and re-locks it frozen. Returns the validation error.
func (td *testData) freezeByMaster(ts base.LedgerTime) error {
	_, err := td.delegationOriginDirect(ts, false, 0, 0, true)
	require.NoError(td, err)
	d := td.delegatedOutput
	dcc := d.Output.ChainConstraint()

	tsFreeze := d.Timestamp().AddSlots(1)
	txb := exhelp.New()
	_, err = txb.ConsumeOutput(d.Output, d.ID)
	require.NoError(td, err)
	txb.PutUnlockParams(0, ledger.ConstraintIndexLock, []byte{0, ledger.DelegationUnlockedByMaster})
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
	_, err = txb.ProduceOutput(d.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(d.Output.TokenBalance()))
		o.PutConstraint(ledger.NewChainConstraint(d.ChainID, 0, dcc.OriginSlot, 0, 0, dcc.TransitionCounter+1, 0).Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(td.frozenFrom(tsFreeze).Bytes(), byte(d.Output.NumConstraints()-1))
	}))
	require.NoError(td, err)
	return td.addMasterFreeze(txb, tsFreeze, d.ChainID)
}

// addMasterFreeze signs the master's freeze, validates it and adds it.
func (td *testData) addMasterFreeze(txb *exhelp.Builder, ts base.LedgerTime, delegationID base.ChainID) error {
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(td.masterPrivateKey)
	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		return err
	}
	require.NoError(td, td.u.AddTransaction(txBytes))
	td.delegatedOutput, err = td.u.SugaredStateReader().GetDelegatedOutput(delegationID)
	require.NoError(td, err)
	require.True(td, td.delegatedOutput.IsMarkedFrozen())
	return nil
}

// The upgraded library reaches the wallet constants.
func TestUpgrade3Library(t *testing.T) {
	newUpgrade3Env(t)
	require.EqualValues(t, 2, ledger.L(upgrade3Slot-1).Constants.UpgradeIndex)
	require.EqualValues(t, 3, ledger.L(upgrade3Slot).Constants.UpgradeIndex)
}

// From the upgrade slot a delegation is frozen only by its target: neither the
// master re-locking its delegation frozen nor a plain chain turned into a frozen
// delegation is valid.
func TestUpgrade3FreezeOnlyByTarget(t *testing.T) {
	for _, c := range []struct {
		name string
		fun  func(td *testData, ts base.LedgerTime) error
	}{
		{"plain chain", (*testData).freezeFromPlainChain},
		{"master", (*testData).freezeByMaster},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := newUpgrade3Env(t)
			err := c.fun(td, base.T(upgrade3Slot, 10))
			require.Error(t, err)
			require.NoError(t, util.MustErrorWith(err, "only the target can freeze a delegation"))
		})
	}
}

// The honest life of a delegation after the upgrade: the target freezes it, tops it
// up and puts it on hold, and its frozen coverage goes back to zero.
func TestUpgrade3HonestDelegation(t *testing.T) {
	td := newUpgrade3Env(t)
	_, _, err := td.initDelegationUTXOMake(base.T(upgrade3Slot, 10), 4, 900)
	require.NoError(t, err)

	ts := td.timestampSlotsForward(1)
	require.NoError(t, td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
		ts:                       ts,
		freezeUntilEpoch:         td.delegatedOutput.FreezeUntilMax(ts),
		inflate:                  true,
		disableConsistencyChecks: true,
	}))
	require.True(t, td.delegatedOutput.IsMarkedFrozen())
	require.Greater(t, td.seqChainOrigin.Output.FrozenCoverage(0), int64(0), "the target is credited")

	req := td.makeTopUpRequestOutput(td.timestampSlotsForward(1), td.masterPrivateKey, td.masterAddr, td.delegatedOutput.ChainID, topUpAmount)
	require.NoError(t, td.topUpDelegation(topUpParams{ts: td.timestampSlotsForward(2), request: req}))
	require.True(t, td.delegatedOutput.IsMarkedFrozen())

	require.NoError(t, td.revokeDelegation(td.timestampSlotsForward(1), true, false))
	require.True(t, td.delegatedOutput.IsMarkedOnHold())
	require.EqualValues(t, 0, td.seqChainOrigin.Output.FrozenCoverage(0))
}
