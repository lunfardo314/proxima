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
	lib := ledger.L(base.MaxSlot)
	cls, err := txbuildercore.ClassifySpendable(lib, o.Bytes(), scCreate, account, targetSlot, lib.TagAlongSlots)
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

// =============================================================================
// tagAlong — sender reclaim
// =============================================================================

// tagAlong windows are ledger constants, not lock arguments, so the classifier
// is told constTagAlongSlots by the caller. Below constTagAlongSlots the fee is
// the target sequencer's to take; from there on it falls back to the sender.
func tagAlongOut(t *testing.T, sender base.HolderID) *ledger.Output {
	t.Helper()
	return ledger.NewTagAlongOutput(scAmount, base.RandomChainID(), sender)
}

// Inside the sequencer's exclusive window the sender has no claim yet.
func TestClassifyTagAlongSenderTooEarly(t *testing.T) {
	sender := base.HolderID(ledger.SigLockRandom())
	lib := ledger.L(base.MaxSlot)
	require.Equal(t, txbuildercore.SpendNotForAccount,
		classify(t, tagAlongOut(t, sender), sender, scCreate+lib.TagAlongSlots-1))
}

// At exactly constTagAlongSlots the sequencer's claim lapses — half-open window.
func TestClassifyTagAlongSenderAtBoundary(t *testing.T) {
	sender := base.HolderID(ledger.SigLockRandom())
	lib := ledger.L(base.MaxSlot)
	require.Equal(t, txbuildercore.SpendSimple,
		classify(t, tagAlongOut(t, sender), sender, scCreate+lib.TagAlongSlots))
}

// Past constTagAlongReclaimSlots the lock also opens to anyone, but the output
// is still the sender's own prepaid fee, so a sweep keeps claiming it rather
// than stranding the wallet's tokens.
func TestClassifyTagAlongSenderPastPublicDeadlineStillSimple(t *testing.T) {
	sender := base.HolderID(ledger.SigLockRandom())
	lib := ledger.L(base.MaxSlot)
	require.Equal(t, txbuildercore.SpendSimple,
		classify(t, tagAlongOut(t, sender), sender, scCreate+lib.TagAlongReclaimSlots+10))
}

// A third party never gets a claim, not even in the public window: sweeping
// other people's abandoned fees is a separate cleanup flow, not compacting.
func TestClassifyTagAlongThirdPartyNeverClaimable(t *testing.T) {
	sender := base.HolderID(ledger.SigLockRandom())
	stranger := base.HolderID(ledger.SigLockRandom())
	lib := ledger.L(base.MaxSlot)
	o := tagAlongOut(t, sender)
	require.Equal(t, txbuildercore.SpendNotForAccount, classify(t, o, stranger, scCreate+lib.TagAlongSlots))
	require.Equal(t, txbuildercore.SpendNotForAccount, classify(t, o, stranger, scCreate+lib.TagAlongReclaimSlots+10))
}

// The target side is a 24-byte chainID, never a holderID, and consuming it
// needs the sequencer's chain input — so it is not classifiable as a simple
// spend even for the chain's controller.
func TestClassifyTagAlongTargetNotSimple(t *testing.T) {
	sender := base.HolderID(ledger.SigLockRandom())
	target := base.RandomChainID()
	lib := ledger.L(base.MaxSlot)
	o := ledger.NewTagAlongOutput(scAmount, target, sender)
	var asHolder base.HolderID
	copy(asHolder[:], target[:])
	require.Equal(t, txbuildercore.SpendNotForAccount,
		classify(t, o, asHolder, scCreate+lib.TagAlongSlots-1))
}
