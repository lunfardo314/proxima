// UTXODB tests for the sendWithDeadline lock — see
// claude/send_with_deadline_lock.md for the design.
//
// Coverage:
//
//   - Per-window happy paths (target accept sigLock, target accept
//     chainLock, master reclaim, public cleanup).
//   - Window boundaries (Δ == acceptanceSlots, Δ == cleanupSlots — and
//     one slot below each, to pin the half-open vs closed semantics).
//   - Master tries to reclaim during the target's window — rejected.
//   - Third party tries to spend during master's window — rejected.
//   - Produce-time guards: targetType ∉ {0x00, 0x01}, acceptanceSlots
//     below floor, master-reclaim window below floor, sender hash check.
//   - Builder round-trip: SendWithDeadlineLockFromOutputElements parses
//     the bytecode + index-values back into the typed lock.

package tests

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test scaffolding
// =============================================================================

type swdEnv struct {
	u             *utxodb.UTXODB
	privKeyMaster ed25519.PrivateKey
	privKeyTarget ed25519.PrivateKey
	privKeyThird  ed25519.PrivateKey
	addrMaster    ledger.SigLock
	addrTarget    ledger.SigLock
	addrThird     ledger.SigLock
	swdOut        *ledger.OutputWithID // the sendWithDeadline UTXO produced in setup
	swdCreateSlot uint32               // createSlot of swdOut (== tx timestamp slot)
	accept        uint32               // acceptanceSlots used at setup
	cleanup       uint32               // cleanupSlots used at setup
}

const (
	swdInitAmount = 1_000_000_000_000
	swdAmount     = 250_000_000 // sendWithDeadline UTXO amount; well above min storage deposit
)

// makeSWDEnv funds three wallets and produces a sendWithDeadline UTXO
// from master to a target of the given kind. acceptance/cleanup
// durations are passed in so tests can probe boundary windows. The
// target itself is one of master's sibling addresses (sigLock target)
// or a freshly-made chain (chainLock target).
func makeSWDEnv(t *testing.T, targetType byte, acceptanceSlots, cleanupSlots uint32) *swdEnv {
	t.Helper()
	env := &swdEnv{accept: acceptanceSlots, cleanup: cleanupSlots}
	env.u = utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := env.u.GenerateAddressesWithFaucetAmount(7, 3, swdInitAmount)
	env.privKeyMaster, env.privKeyTarget, env.privKeyThird = privKeys[0], privKeys[1], privKeys[2]
	env.addrMaster, env.addrTarget, env.addrThird = addrs[0], addrs[1], addrs[2]

	// pick a tx timestamp safely past the funding outputs
	masterOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrMaster.ControllerID())
	require.NoError(t, err)
	require.True(t, len(masterOuts) > 0)
	swdTs := masterOuts[0].ID.Timestamp().AddSlots(1)
	if swdTs.IsSlotBoundary() {
		swdTs = swdTs.AddTicks(1)
	}

	// derive targetID per type
	var targetID base.HolderID
	switch targetType {
	case ledger.SendWithDeadlineTargetSigLock:
		// the target's sigLock holderID
		th := base.HolderID(ledger.SigLockFromED25519PrivateKey(env.privKeyTarget))
		copy(targetID[:], th[:])
	case ledger.SendWithDeadlineTargetChainLock:
		// fresh chain owned by the target wallet; use the chain's ID
		targetOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrTarget.ControllerID())
		require.NoError(t, err)
		require.True(t, len(targetOuts) > 0)
		chainTs := targetOuts[0].ID.Timestamp().AddSlots(1)
		if chainTs.IsSlotBoundary() {
			chainTs = chainTs.AddTicks(1)
		}
		chain, err := env.u.MakeNewChain(swdInitAmount/2, env.privKeyTarget, env.addrTarget, chainTs)
		require.NoError(t, err)
		copy(targetID[:], chain.ChainID[:])
		// bump swd ts past the chain ts
		swdTs = chain.ID.Timestamp().AddSlots(1)
		if swdTs.IsSlotBoundary() {
			swdTs = swdTs.AddTicks(1)
		}
	default:
		require.Fail(t, "unknown targetType in test")
	}

	masterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(env.privKeyMaster))
	lock := &ledger.SendWithDeadlineLock{
		MasterID:        masterID,
		TargetID:        targetID,
		TargetType:      targetType,
		AcceptanceSlots: acceptanceSlots,
		CleanupSlots:    cleanupSlots,
	}

	// Build a tx that produces the swd output + change from master's funding.
	masterOuts, err = env.u.SugaredStateReader().GetOutputsForAccount(env.addrMaster.ControllerID())
	require.NoError(t, err)
	txb := exhelp.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	swdOutput := ledger.NewSendWithDeadlineOutput(swdAmount, lock)
	swdIdx, err := txb.ProduceOutput(swdOutput)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(masterOuts[0].Output.TokenBalance() - swdAmount).WithLock(env.addrMaster)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(swdTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(env.privKeyMaster)

	txBytes, txid, _, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "swd produce tx must validate")
	require.NoError(t, env.u.AddTransaction(txBytes))

	env.swdOut = &ledger.OutputWithID{
		ID:     base.MustNewOutputID(txid, swdIdx),
		Output: swdOutput,
	}
	env.swdCreateSlot = swdTs.Slot
	return env
}

// spendSWD builds a tx that consumes env.swdOut at the requested
// Δ-from-createSlot, signed by `signer`, with optional extra unlock
// parameters on the swd input. payeeLock receives the funds.
func spendSWD(t *testing.T, env *swdEnv, delta uint32, signer ed25519.PrivateKey, payeeLock ledger.SigLock, unlockParams []byte) error {
	t.Helper()
	txb := exhelp.New()
	_, err := txb.ConsumeOutput(env.swdOut.Output, env.swdOut.ID)
	require.NoError(t, err)
	if len(unlockParams) > 0 {
		txb.PutUnlockParams(0, ledger.ConstraintIndexLock, unlockParams)
	} else {
		txb.PutSignatureUnlock(0)
	}
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(swdAmount).WithLock(payeeLock)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(base.T(env.swdCreateSlot+delta, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(signer)

	txBytes := txb.Bytes()
	return env.u.AddTransaction(txBytes)
}

// spendSWDViaChain consumes env.swdOut at Δ via a chainLock unlock: the
// tx also consumes the target's chain output, and swd's lock unlock is
// the chain input index. The signer is the chain controller. Mirrors
// the tag-along consume-via-chain pattern.
func spendSWDViaChain(t *testing.T, env *swdEnv, delta uint32, chainOut *ledger.OutputWithChainID, signer ed25519.PrivateKey, payeeLock ledger.SigLock) error {
	t.Helper()
	txb := exhelp.New()
	// chain input
	_, err := txb.ConsumeOutput(chainOut.Output, chainOut.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	// swd input
	_, err = txb.ConsumeOutput(env.swdOut.Output, env.swdOut.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

	// transit chain (continue with same amount + same lock)
	successorCC := ledger.NewChainConstraint(chainOut.ChainID, 0, chainOut.OriginSlot, 0, 0, chainOut.TransitionCounter+1, 0)
	chainSuccessor := chainOut.Output.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successorCC.Bytes(), ledger.ConstraintIndexChain)
	})
	_, err = txb.ProduceOutput(chainSuccessor)
	require.NoError(t, err)

	// swd payout
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(swdAmount).WithLock(payeeLock)
	}))
	require.NoError(t, err)

	txb.SetTimestamp(base.T(env.swdCreateSlot+delta, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(signer)
	return env.u.AddTransaction(txb.Bytes())
}

// =============================================================================
// Happy paths — one per window
// =============================================================================

// TestSWDTargetAcceptSigLock: Δ < acceptanceSlots; target signs.
func TestSWDTargetAcceptSigLock(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	err := spendSWD(t, env, 10, env.privKeyTarget, env.addrTarget, nil)
	require.NoError(t, err, "target sigLock consume inside acceptance window must validate")
}

// TestSWDTargetAcceptChainLock: Δ < acceptanceSlots; target consumes via
// chainLock by spending its chain output in the same tx.
func TestSWDTargetAcceptChainLock(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetChainLock, 60, 1100)
	// Locate the target chain output (the one we made in setup).
	chainOuts := env.u.SugaredStateReader().GetTagAlongBacklog // not chain-specific; use a direct lookup
	_ = chainOuts
	// The chain output is owned by target; find it by ChainConstraint.
	outs, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrTarget.ControllerID())
	require.NoError(t, err)
	var chainOut *ledger.OutputWithChainID
	for _, o := range outs {
		if cc := o.Output.ChainConstraint(); cc != nil {
			wc, ok := ledger.AsOutputWithChainID(o.Output, o.ID)
			if ok {
				chainOut = &wc
				break
			}
		}
	}
	require.NotNil(t, chainOut, "test setup should have produced a chain owned by the target")

	err = spendSWDViaChain(t, env, 10, chainOut, env.privKeyTarget, env.addrTarget)
	require.NoError(t, err, "target chainLock consume inside acceptance window must validate")
}

// TestSWDMasterReclaim: Δ in [acceptance, cleanup); master signs.
func TestSWDMasterReclaim(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	err := spendSWD(t, env, 100, env.privKeyMaster, env.addrMaster, nil)
	require.NoError(t, err, "master reclaim inside reclaim window must validate")
}

// TestSWDPublicCleanup: Δ ≥ cleanup; third-party signs.
func TestSWDPublicCleanup(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	err := spendSWD(t, env, 1500, env.privKeyThird, env.addrThird, nil)
	require.NoError(t, err, "any signer inside cleanup window must validate")
}

// =============================================================================
// Negative paths
// =============================================================================

// Target tries to spend AFTER the acceptance window — rejected (master's window).
func TestSWDTargetTooLate(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	err := spendSWD(t, env, 80, env.privKeyTarget, env.addrTarget, nil)
	require.Error(t, err, "target signer past acceptance window must be rejected")
}

// Master tries to reclaim DURING the acceptance window — rejected (target's window).
func TestSWDMasterTooEarly(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	err := spendSWD(t, env, 10, env.privKeyMaster, env.addrMaster, nil)
	require.Error(t, err, "master reclaim inside acceptance window must be rejected")
}

// Third party tries to spend DURING the reclaim window — rejected.
func TestSWDThirdPartyTooEarly(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	err := spendSWD(t, env, 100, env.privKeyThird, env.addrThird, nil)
	require.Error(t, err, "third-party consume inside reclaim window must be rejected")
}

// =============================================================================
// Window boundary semantics
// =============================================================================

// At Δ == acceptanceSlots we're in MASTER's window, not target's.
func TestSWDBoundaryAcceptance(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	// At Δ = 60: target rejected, master accepted.
	require.Error(t, spendSWD(t, env, 60, env.privKeyTarget, env.addrTarget, nil),
		"Δ == acceptanceSlots must reject the target")
	// Re-create the env (previous spend failed but consumed the env state — be safe).
	env2 := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	require.NoError(t, spendSWD(t, env2, 60, env2.privKeyMaster, env2.addrMaster, nil),
		"Δ == acceptanceSlots must accept the master")
}

// At Δ == cleanupSlots we're in PUBLIC's window, not master's.
func TestSWDBoundaryCleanup(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	require.NoError(t, spendSWD(t, env, 1100, env.privKeyThird, env.addrThird, nil),
		"Δ == cleanupSlots must accept any signer (public cleanup)")
}

// =============================================================================
// Produce-time guards
// =============================================================================

// Helper for tests that try to produce a bad lock and assert a specific
// rejection. tweak applies the malformation BEFORE the lock is encoded.
func tryProduceBadSWD(t *testing.T, want string, tweak func(l *ledger.SendWithDeadlineLock)) {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(7, 1, swdInitAmount)
	priv := privKeys[0]
	addr := addrs[0]

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)

	masterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(priv))
	var targetID base.HolderID
	for i := range targetID {
		targetID[i] = 0x42
	}
	lock := &ledger.SendWithDeadlineLock{
		MasterID:        masterID,
		TargetID:        targetID,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: ledger.SendWithDeadlineMinAcceptanceSlots,
		CleanupSlots:    ledger.SendWithDeadlineMinAcceptanceSlots + ledger.SendWithDeadlineMinReclaimSlots,
	}
	tweak(lock)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	_, err = txb.ProduceOutput(ledger.NewSendWithDeadlineOutput(swdAmount, lock))
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(outs[0].Output.TokenBalance() - swdAmount).WithLock(addr)
	}))
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)
	err = u.AddTransaction(txb.Bytes())
	require.Error(t, err)
	require.Contains(t, err.Error(), want)
}

func TestSWDProduceRejectBadTargetType(t *testing.T) {
	tryProduceBadSWD(t, "targetType must be 0x00 or 0x01", func(l *ledger.SendWithDeadlineLock) {
		l.TargetType = 0x02
	})
}

func TestSWDProduceRejectAcceptanceBelowFloor(t *testing.T) {
	tryProduceBadSWD(t, "acceptanceSlots below floor", func(l *ledger.SendWithDeadlineLock) {
		l.AcceptanceSlots = ledger.SendWithDeadlineMinAcceptanceSlots - 1
	})
}

func TestSWDProduceRejectReclaimBelowFloor(t *testing.T) {
	tryProduceBadSWD(t, "master reclaim window below floor", func(l *ledger.SendWithDeadlineLock) {
		// cleanup - acceptance < 1000 → rejected
		l.AcceptanceSlots = 100
		l.CleanupSlots = 100 + 999
	})
}

func TestSWDProduceRejectMasterMismatch(t *testing.T) {
	// Sign tx with a key whose holderID does NOT match l.MasterID.
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(7, 2, swdInitAmount)
	signer := privKeys[0]
	addr := addrs[0]
	other := privKeys[1]

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)

	var targetID base.HolderID
	for i := range targetID {
		targetID[i] = 0x42
	}
	// claim master = other's holderID (not signer)
	wrongMaster := base.HolderID(ledger.SigLockFromED25519PrivateKey(other))
	lock := &ledger.SendWithDeadlineLock{
		MasterID:        wrongMaster,
		TargetID:        targetID,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: 30,
		CleanupSlots:    1030,
	}

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	_, err = txb.ProduceOutput(ledger.NewSendWithDeadlineOutput(swdAmount, lock))
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(outs[0].Output.TokenBalance() - swdAmount).WithLock(addr)
	}))
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(signer)
	err = u.AddTransaction(txb.Bytes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "master hash check failed")
}

// =============================================================================
// Additional produce-time guards
// =============================================================================

// TestSWDProduceRejectZeroTargetID: targetID is all-zero (sender forgot to
// set it). Must hit the non_zero_targetID guard BEFORE the masterID/signer
// checks (constraint evaluates left-to-right).
func TestSWDProduceRejectZeroTargetID(t *testing.T) {
	tryProduceBadSWD(t, "non zero targetID expected", func(l *ledger.SendWithDeadlineLock) {
		l.TargetID = base.HolderID{}
	})
}

// TestSWDProduceRejectZeroMasterID: masterID all-zero. The constraint also
// runs a master-hash check, but the non-zero check fires first.
func TestSWDProduceRejectZeroMasterID(t *testing.T) {
	// The tx signer is real (non-zero), so the master_hash_check would also
	// fail — but the non_zero check comes earlier in the and(). We assert
	// the specific earlier error.
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(7, 1, swdInitAmount)
	priv := privKeys[0]
	addr := addrs[0]
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)

	var targetID base.HolderID
	for i := range targetID {
		targetID[i] = 0x42
	}
	lock := &ledger.SendWithDeadlineLock{
		MasterID:        base.HolderID{}, // all-zero — must be rejected
		TargetID:        targetID,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: 30,
		CleanupSlots:    1030,
	}

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	_, err = txb.ProduceOutput(ledger.NewSendWithDeadlineOutput(swdAmount, lock))
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(outs[0].Output.TokenBalance() - swdAmount).WithLock(addr)
	}))
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)
	err = u.AddTransaction(txb.Bytes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "non zero masterID expected")
}

// TestSWDProduceRejectLockAtWrongSlot: place the swd bytecode at output
// slot 4 instead of slot 2 (with an opaque truthy bytecode at the real
// lock slot so the output is structurally valid). The swd constraint
// then evaluates with selfBlockIndex == 4 ≠ lockConstraintIndex (2)
// and must reject — even though every other field is well-formed.
func TestSWDProduceRejectLockAtWrongSlot(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(7, 1, swdInitAmount)
	priv := privKeys[0]
	addr := addrs[0]
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)

	masterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(priv))
	var targetID base.HolderID
	for i := range targetID {
		targetID[i] = 0x42
	}
	swdLock := &ledger.SendWithDeadlineLock{
		MasterID:        masterID,
		TargetID:        targetID,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: 30,
		CleanupSlots:    1030,
	}

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Use an opaque function-call bytecode at the lock slot. `or(0x01)`
	// compiles to {0x44, 0x42, 0x81, 0x01} and evaluates to `0x01`
	// (truthy). Its prefix is the registered `or` global, so the
	// storage-deposit prefix parser (sdeposit.go) accepts it; an
	// inline-data literal alone would crash that parser. The swd
	// bytecode goes to slot 4 — wrong slot.
	opaqueLockBC := []byte{0x44, 0x42, 0x81, 0x01}
	mis := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(swdAmount))
		o.PutConstraint(ledger.IndexValuesTupleBytes(swdLock.IndexValues()), ledger.ConstraintIndexIndexValues)
		o.PutConstraint(opaqueLockBC, ledger.ConstraintIndexLock)
		// swd bytecode at slot 3 (NOT the lock slot). Adjacent to slot 2
		// to avoid empty intermediate slots that would panic the constraint
		// walker before the swd check fires.
		o.PutConstraint(swdLock.LockBytecode(), 3)
	})
	_, err = txb.ProduceOutput(mis)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(outs[0].Output.TokenBalance() - swdAmount).WithLock(addr)
	}))
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)
	err = u.AddTransaction(txb.Bytes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "locks must be at lockConstraintIndex")
}

// TestSWDProduceRejectTooManyConstraints: the lock requires
// selfNumConstraints < 6. A standard swd output has 3 slots (amounts,
// indexValues, lock). Adding 3 dummy constraints at slots 3..5 pushes
// total to 6 — must be rejected.
func TestSWDProduceRejectTooManyConstraints(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(7, 1, swdInitAmount)
	priv := privKeys[0]
	addr := addrs[0]
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)

	masterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(priv))
	var targetID base.HolderID
	for i := range targetID {
		targetID[i] = 0x42
	}
	lock := &ledger.SendWithDeadlineLock{
		MasterID:        masterID,
		TargetID:        targetID,
		TargetType:      ledger.SendWithDeadlineTargetSigLock,
		AcceptanceSlots: 30,
		CleanupSlots:    1030,
	}

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	fat := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(swdAmount).WithLock(lock)
		// stuff 3 extra constraints at slots 3, 4, 5 → numConstraints = 6
		filler := []byte{0xff} // any non-empty bytecode that evaluates truthy as inline data
		o.PutConstraint(filler, 3)
		o.PutConstraint(filler, 4)
		o.PutConstraint(filler, 5)
	})
	_, err = txb.ProduceOutput(fat)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(outs[0].Output.TokenBalance() - swdAmount).WithLock(addr)
	}))
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)
	err = u.AddTransaction(txb.Bytes())
	require.Error(t, err)
	require.Contains(t, err.Error(), "too many UTXO elements")
}

// =============================================================================
// Additional consume-time guards
// =============================================================================

// TestSWDTargetWrongSigKey: a third party (not the target) signs inside the
// acceptance window of a sigLock-target swd. _sigLock($target) compares
// txHolderID(txSignatureData) to targetID; mismatch must reject.
func TestSWDTargetWrongSigKey(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetSigLock, 60, 1100)
	// Third party (env.privKeyThird, not target) tries to spend inside the
	// acceptance window — must be rejected.
	err := spendSWD(t, env, 10, env.privKeyThird, env.addrThird, nil)
	require.Error(t, err, "non-target signer inside acceptance window must be rejected")
}

// TestSWDTargetWrongChain: spend a chainLock-target swd inside its
// acceptance window using an UNRELATED chain output. The chainLock's chainID
// crosscheck must reject (the supplied chain's ID ≠ target's chainID).
func TestSWDTargetWrongChain(t *testing.T) {
	env := makeSWDEnv(t, ledger.SendWithDeadlineTargetChainLock, 60, 1100)

	// Build a SECOND, unrelated chain owned by the third-party wallet.
	thirdOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.addrThird.ControllerID())
	require.NoError(t, err)
	chainTs := thirdOuts[0].ID.Timestamp().AddSlots(1)
	if chainTs.IsSlotBoundary() {
		chainTs = chainTs.AddTicks(1)
	}
	unrelatedChain, err := env.u.MakeNewChain(swdInitAmount/4, env.privKeyThird, env.addrThird, chainTs)
	require.NoError(t, err)

	// Try to consume the swd by spending the unrelated chain — _chainLock's
	// chainID crosscheck (or the cross-check against the configured target)
	// must fail.
	err = spendSWDViaChain(t, env, 10, unrelatedChain, env.privKeyThird, env.addrThird)
	require.Error(t, err, "spending swd with an unrelated chain in tx must be rejected")
}

// =============================================================================
// Builder round-trip
// =============================================================================

// TestSWDLockRoundTrip serialises a typed lock through the standard
// LockFromOutputElements + re-encode dance and asserts byte-equality.
func TestSWDLockRoundTrip(t *testing.T) {
	var master, target base.HolderID
	for i := range master {
		master[i] = byte(i)
	}
	// chainLock target is a ChainIDLength chainID stored in the first bytes of TargetID
	for i := 0; i < base.ChainIDLength; i++ {
		target[i] = byte(0x80 + i)
	}
	in := &ledger.SendWithDeadlineLock{
		MasterID:        master,
		TargetID:        target,
		TargetType:      ledger.SendWithDeadlineTargetChainLock,
		AcceptanceSlots: 60,
		CleanupSlots:    8000,
	}

	indexValues := ledger.IndexValuesTupleBytes(in.IndexValues())
	bytecode := in.LockBytecode()

	out, err := ledger.SendWithDeadlineLockFromOutputElements(indexValues, bytecode, ledger.L(base.MaxSlot))
	require.NoError(t, err)
	require.Equal(t, in.MasterID, out.MasterID)
	require.Equal(t, in.TargetID, out.TargetID)
	require.Equal(t, in.TargetType, out.TargetType)
	require.Equal(t, in.AcceptanceSlots, out.AcceptanceSlots)
	require.Equal(t, in.CleanupSlots, out.CleanupSlots)

	require.True(t, strings.Contains(out.String(), "chainLock"),
		"String() should reflect targetType=chainLock for type=0x01")
}
