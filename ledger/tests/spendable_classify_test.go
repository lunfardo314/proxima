// Unit tests for txbuildercore.ClassifySpendable — the shared spendable
// classifier used by the node's get_outputs spendable filter and by
// `proxi node compact`. The tests build raw outputs and classify them with
// the real ledger library (which satisfies txbuildercore.BytecodeParser);
// no utxodb settlement is needed since the classifier works on raw bytes +
// an explicit createSlot.
package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

const (
	scAmount  = uint64(1_000_000_000)
	scAccept  = uint32(60)
	scCleanup = uint32(1100)
	scCreate  = uint32(100) // output createSlot used throughout
)

func classify(t *testing.T, o *ledger.Output, account base.HolderID, targetSlot uint32) txbuildercore.SpendClass {
	t.Helper()
	cls, err := txbuildercore.ClassifySpendable(ledger.L(base.MaxSlot), o.Bytes(), scCreate, account, targetSlot)
	require.NoError(t, err)
	return cls
}

// swdLock builds a sigLock-target SWD lock from master to target.
func swdLock(master, target base.HolderID) *ledger.SendWithDeadlineLock {
	return &ledger.SendWithDeadlineLock{
		MasterID:        master,
		TargetID:        target,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: scAccept,
		CleanupSlots:    scCleanup,
	}
}

// Plain 3-element sigLock to the account → Simple, regardless of slot.
func TestClassifySigLockSimple(t *testing.T) {
	lock := ledger.SigLockRandom()
	account := base.HolderID(lock)
	o := ledger.OutputBasic(int64(scAmount), lock)
	require.Equal(t, txbuildercore.SpendSimple, classify(t, o, account, scCreate+10))
}

// sigLock to someone else → NotForAccount.
func TestClassifySigLockWrongAccount(t *testing.T) {
	o := ledger.OutputBasic(int64(scAmount), ledger.SigLockRandom())
	other := base.HolderID(ledger.SigLockRandom())
	require.Equal(t, txbuildercore.SpendNotForAccount, classify(t, o, other, scCreate+10))
}

// sigLock(account) carrying an extra inline literal (e.g. a returnToSender
// receipt UTXO) → Unknown: structure isn't the canonical 3-element shape.
func TestClassifySigLockWithExtraIsUnknown(t *testing.T) {
	lock := ledger.SigLockRandom()
	account := base.HolderID(lock)
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(scAmount).WithLock(lock)
		o.MustPushConstraint(easyfl.InlineDataBytecode([]byte{0x05}))
	})
	require.Equal(t, txbuildercore.SpendUnknown, classify(t, o, account, scCreate+10))
}

// SWD sigLock-target accept window, no extras → Simple.
func TestClassifySWDTargetAcceptSimple(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	o := ledger.NewSendWithDeadlineOutput(scAmount, swdLock(master, target))
	// Δ = 10 < acceptanceSlots(60) → target window.
	require.Equal(t, txbuildercore.SpendSimple, classify(t, o, target, scCreate+10))
}

// SWD sigLock-target accept window WITH returnToSender → NeedsReturn.
func TestClassifySWDTargetAcceptNeedsReturn(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	rtsBin, err := ledger.ReturnToSenderBytecode(scAmount / 2)
	require.NoError(t, err)
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(scAmount).WithLock(swdLock(master, target))
		o.PutConstraint(rtsBin, 3)
	})
	require.Equal(t, txbuildercore.SpendNeedsReturn, classify(t, o, target, scCreate+10))
}

// SWD master-reclaim window WITH returnToSender → Simple (noop for master).
func TestClassifySWDMasterReclaimWithReturnIsSimple(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	rtsBin, err := ledger.ReturnToSenderBytecode(scAmount / 2)
	require.NoError(t, err)
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(scAmount).WithLock(swdLock(master, target))
		o.PutConstraint(rtsBin, 3)
	})
	// Δ = 100 ≥ acceptanceSlots(60) → master window.
	require.Equal(t, txbuildercore.SpendSimple, classify(t, o, master, scCreate+100))
}

// SWD master before the reclaim window opens → NotForAccount.
func TestClassifySWDMasterTooEarly(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	o := ledger.NewSendWithDeadlineOutput(scAmount, swdLock(master, target))
	require.Equal(t, txbuildercore.SpendNotForAccount, classify(t, o, master, scCreate+10))
}

// SWD target after the acceptance window closes → NotForAccount.
func TestClassifySWDTargetTooLate(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	o := ledger.NewSendWithDeadlineOutput(scAmount, swdLock(master, target))
	require.Equal(t, txbuildercore.SpendNotForAccount, classify(t, o, target, scCreate+100))
}

// SWD target accept window but with an unrecognised extra constraint → Unknown.
func TestClassifySWDTargetUnknownExtra(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(scAmount).WithLock(swdLock(master, target))
		o.PutConstraint(easyfl.InlineDataBytecode([]byte{0x07}), 3) // not returnToSender
	})
	require.Equal(t, txbuildercore.SpendUnknown, classify(t, o, target, scCreate+10))
}

// A third party (neither master nor target) → NotForAccount.
func TestClassifySWDThirdParty(t *testing.T) {
	master := base.HolderID(ledger.SigLockRandom())
	target := base.HolderID(ledger.SigLockRandom())
	third := base.HolderID(ledger.SigLockRandom())
	o := ledger.NewSendWithDeadlineOutput(scAmount, swdLock(master, target))
	require.Equal(t, txbuildercore.SpendNotForAccount, classify(t, o, third, scCreate+10))
}
