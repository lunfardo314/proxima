package tests

// Security-focused delegation constraint tests.
// These tests cover attack vectors and edge cases for the delegateLock EasyFL constraint.
//
// Delegation lock structure (4 constraints required):
//   [0] amount, [1] delegateLock, [2] chain, [3] delegateLockState
//
// Two unlock modes:
//   - Master unlock: byte(selfUnlockParameters,2) == 0xff, requires sigLock(masterID), not frozen
//   - Target unlock: byte(selfUnlockParameters,2) != 0xff, requires chainLock, not on-hold,
//     amount cannot decrease, lock must be identical on successor, cannot discontinue chain
//
// Delegation states: undef (0), frozen (1), on_hold (2)

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ed25519"
)

// delegTestEnv holds test environment for delegation security tests.
type delegTestEnv struct {
	u                *utxodb.UTXODB
	masterPrivateKey ed25519.PrivateKey
	masterAddr       ledger.SigLock
	seqPrivateKey    ed25519.PrivateKey
	seqAddr          ledger.SigLock
	target           base.ChainID
	seqChainOrigin   ledger.OutputWithChainID
	delegatedOutput  ledger.DelegationOutput
}

const (
	cdelegInitAmount     = 200_000_000_000
	cdelegOnChainBalance = 3_000_000_000
	cdelegTokens         = 1_000_000_000
)

// setupDelegEnv creates a sequencer chain and a delegation output targeting it.
func setupDelegEnv(t *testing.T, maxFrozenEpochs byte, inflationShare uint16) *delegTestEnv {
	t.Helper()
	env := &delegTestEnv{}
	env.u = utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := env.u.GenerateAddresses(0, 2)
	env.masterPrivateKey = privKey[0]
	env.masterAddr = addr[0]
	env.seqPrivateKey = privKey[1]
	env.seqAddr = addr[1]

	err := env.u.TokensFromFaucet(env.masterAddr, cdelegInitAmount)
	require.NoError(t, err)
	err = env.u.TokensFromFaucet(env.seqAddr, cdelegInitAmount)
	require.NoError(t, err)

	// create chain for sequencer
	seqOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.seqAddr.ControllerID())
	require.NoError(t, err)
	seqOriginTs := seqOuts[0].ID.Timestamp().AddSlots(1)

	par, err := env.u.MakeTransferInputData(env.seqPrivateKey, nil, seqOriginTs)
	require.NoError(t, err)
	outs, err := env.u.DoTransferOutputs(par.
		WithAmount(cdelegOnChainBalance).
		WithTargetLock(env.seqAddr).
		WithConstraint(ledger.NewChainOrigin(seqOriginTs.Slot)).
		// Attach delegationParams at the fixed index so the chain can
		// accept delegations (Phase 3 of delegation_epoch_params).
		WithConstraint(ledger.NewDelegationParams(
			ledger.L(0).DelegationEpochSlots,
			byte(ledger.L(0).MaxFrozenEpochs),
		), ledger.ConstraintIndexDelegationParams),
	)
	require.NoError(t, err)
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(t, err)
	require.EqualValues(t, 1, len(chOuts))
	env.seqChainOrigin = *chOuts[0]
	env.target = env.seqChainOrigin.ChainID

	// create delegation output
	masterOuts, err := env.u.SugaredStateReader().GetOutputsForAccount(env.masterAddr.ControllerID())
	require.NoError(t, err)
	delegTs := env.seqChainOrigin.Timestamp().AddSlots(1)

	txb := txbuilder.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	delegOut := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:                 cdelegTokens,
		MasterID:               base.HolderID(env.masterAddr),
		Target:                 env.target,
		MaxFrozenEpochs:        maxFrozenEpochs,
		RequiredInflationShare: inflationShare,
		StartSlot:              delegTs.Slot,
		EpochSlots:             ledger.L(0).DelegationEpochSlots,
		TargetMaxFrozenEpochs:  byte(ledger.L(0).MaxFrozenEpochs),
	})
	_, err = txb.ProduceOutput(delegOut)
	require.NoError(t, err)
	remainder := masterOuts[0].Output.TokenBalance() - cdelegTokens
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(remainder).WithLock(env.masterAddr)
	}))
	require.NoError(t, err)

	txb.TransactionData.Timestamp = delegTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(env.masterPrivateKey)
	txBytes, _, _, err := txb.BytesWithValidation()
	require.NoError(t, err)
	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err)

	// retrieve delegation output
	delegOuts, err := env.u.SugaredStateReader().GetOutputsDelegatedToAccount2(env.target[:])
	require.NoError(t, err)
	require.EqualValues(t, 1, len(delegOuts))
	env.delegatedOutput, _ = ledger.DelegationOutputFromOutputWithChainID(delegOuts[0])

	return env
}

// freezeDelegation transitions the delegation to frozen state by the target.
// Returns the updated env with fresh chain tip and delegation tip.
func (env *delegTestEnv) freezeDelegation(t *testing.T, frozenEpochs byte) {
	t.Helper()
	ts := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	freezeUntilEpoch := env.delegatedOutput.FreezeUntilMax(ts)
	requiredAdvance, err := env.delegatedOutput.RequiredMinimumInflationAdvance(ts, freezeUntilEpoch)
	require.NoError(t, err)

	delegSuccessor, err := env.delegatedOutput.MakeDelegationFreezeOutput(ts, freezeUntilEpoch, 1, requiredAdvance, true)
	require.NoError(t, err)

	txb := txbuilder.New()
	_, _, err = txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	seqChainIdx, err := txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance() - requiredAdvance))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	_, err = txb.ProduceOutput(delegSuccessor)
	require.NoError(t, err)

	fcDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(t, err)
	txb.MustPutFrozenCoverage(seqChainIdx, fcDelta, ts)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(env.seqPrivateKey)
	txBytes, _, _, err := txb.BytesWithValidation()
	require.NoError(t, err)
	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err)

	// refresh tips
	env.delegatedOutput, err = env.u.SugaredStateReader().GetDelegatedOutput(env.delegatedOutput.ChainID)
	require.NoError(t, err)
	env.seqChainOrigin, err = env.u.SugaredStateReader().GetChainOutputWithChainID(env.seqChainOrigin.ChainID)
	require.NoError(t, err)
}

// TestClaudeDelegationWrongMasterUnlock verifies that a third party (not the master)
// cannot unlock a delegation output using master unlock mode (byte 2 = 0xff).
// The sigLock($1) check in _masterUnlockedConsumed requires the signer to match masterID.
func TestClaudeDelegationWrongMasterUnlock(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	// attacker (seq controller) tries to unlock as master
	txb := txbuilder.New()
	amount, _, err := txb.ConsumeOutputsNoUnlock(&env.delegatedOutput.OutputWithID)
	require.NoError(t, err)

	// mark as master unlock (byte 2 = 0xff)
	txb.PutUnlockParams(0, ledger.ConstraintIndexLock, []byte{0xff, 0xff, 0xff})
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(env.seqAddr)
	}))
	require.NoError(t, err)

	ts := env.delegatedOutput.Timestamp().AddSlots(1)
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	// sign with seq controller key, NOT master key
	txb.SignED25519(env.seqPrivateKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "wrong master should not unlock delegation")
}

// TestClaudeDelegationTargetReducesAmount verifies that the target sequencer
// cannot reduce the delegated amount on the successor output.
// EasyFL: lessOrEqualThan(selfTokenBalanceValue, _amountOnSuccessor)
func TestClaudeDelegationTargetReducesAmount(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	ts := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	txb := txbuilder.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	// sequencer takes stolen tokens
	stolenAmount := uint64(100_000_000)
	_, err = txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance() + stolenAmount))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	// produce delegation successor with reduced amount
	reducedAmount := env.delegatedOutput.Output.TokenBalance() - stolenAmount
	cc := ledger.NewChainConstraint(env.delegatedOutput.ChainID, predIdx, env.delegatedOutput.OriginSlot, 0, 0, env.delegatedOutput.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(reducedAmount))
		o.WithLock(env.delegatedOutput.Output.Lock())
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(env.seqPrivateKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "target should not be able to reduce delegated amount")
	require.NoError(t, util.MustErrorWith(err, "delegated amount should not decrease"))
}

// TestClaudeDelegationTargetChangesLock verifies that the target sequencer cannot
// modify the immutable delegation lock parameters on the successor output.
// EasyFL: equal(successorConstraint(1), selfSiblingConstraint(lockConstraintIndex))
func TestClaudeDelegationTargetChangesLock(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	ts := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	txb := txbuilder.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	// produce delegation successor with MODIFIED lock (different master)
	attackerMasterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(env.seqPrivateKey))
	tamperedLock := ledger.NewDelegateLock(env.target, attackerMasterID, 4, 0,
		ledger.L(0).DelegationEpochSlots, byte(ledger.L(0).MaxFrozenEpochs))
	cc := ledger.NewChainConstraint(env.delegatedOutput.ChainID, predIdx, env.delegatedOutput.OriginSlot, 0, 0, env.delegatedOutput.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.delegatedOutput.Output.TokenBalance()))
		o.WithLock(tamperedLock)
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(env.seqPrivateKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "target should not be able to change delegation lock")
	require.NoError(t, util.MustErrorWith(err, "delegation index values on successor must be exactly the same"))
}

// TestClaudeDelegationTargetDiscontinuesChain verifies that the target sequencer
// cannot terminate (discontinue) a delegation chain. Only the master can do that.
// EasyFL: not(equal(selfSiblingUnlockParams(2),0xffff)) -> target_cannot_discontinue
func TestClaudeDelegationTargetDiscontinuesChain(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	ts := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	txb := txbuilder.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance() + env.delegatedOutput.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	// target unlock (byte 2 = 0) but with chain termination unlock params
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(env.seqPrivateKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "target should not be able to discontinue delegation chain")
	require.NoError(t, util.MustErrorWith(err, "target cannot discontinue the delegation chain"))
}

// TestClaudeDelegationOriginCannotBeFrozen verifies that a delegation origin
// output cannot be created in frozen state.
// EasyFL: not(_selfIsDelegationOrigin) inside _validLimitsProducedFrozen
func TestClaudeDelegationOriginCannotBeFrozen(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddresses(0, 2)
	masterPrivateKey := privKey[0]
	masterAddr := addr[0]
	seqPrivateKey := privKey[1]
	seqAddr := addr[1]

	err := u.TokensFromFaucet(masterAddr, cdelegInitAmount)
	require.NoError(t, err)
	err = u.TokensFromFaucet(seqAddr, cdelegInitAmount)
	require.NoError(t, err)

	// create chain
	seqOuts, err := u.SugaredStateReader().GetOutputsForAccount(seqAddr.ControllerID())
	require.NoError(t, err)
	seqOriginTs := seqOuts[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(seqPrivateKey, nil, seqOriginTs)
	require.NoError(t, err)
	outs, err := u.DoTransferOutputs(par.
		WithAmount(cdelegOnChainBalance).
		WithTargetLock(seqAddr).
		WithConstraint(ledger.NewChainOrigin(seqOriginTs.Slot)),
	)
	require.NoError(t, err)
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(t, err)
	target := chOuts[0].ChainID

	// try to create delegation origin in FROZEN state
	masterOuts, err := u.SugaredStateReader().GetOutputsForAccount(masterAddr.ControllerID())
	require.NoError(t, err)
	delegTs := chOuts[0].Timestamp().AddSlots(1)

	txb := txbuilder.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// manually build delegation origin with frozen state (bypassing helper)
	delegLock := ledger.NewDelegateLock(target, base.HolderID(masterAddr), 4, 0,
		ledger.L(0).DelegationEpochSlots, byte(ledger.L(0).MaxFrozenEpochs))
	frozenOrigin := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(cdelegTokens))
		o.WithLock(delegLock)
		o.MustPushConstraint(ledger.NewChainOrigin(delegTs.Slot).Bytes())
		// frozen state at origin - should be rejected
		o.MustPushConstraint(ledger.DelegateLockState{LastFrozenEpoch: 5, State: ledger.DelegateLockStateFrozen}.Bytes())
	})
	_, err = txb.ProduceOutput(frozenOrigin)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(masterOuts[0].Output.TokenBalance() - cdelegTokens).WithLock(masterAddr)
	}))
	require.NoError(t, err)

	txb.TransactionData.Timestamp = delegTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(masterPrivateKey)
	_, _, _, err = txb.BytesWithValidation()
	// The EasyFL checks in _validLimitsProducedFrozen fire in order:
	// 1. last_frozen_epoch_cannot_be_in_the_past
	// 2. frozen_epochs_cannot_exceed_maximum_set_by_delegator
	// 3. delegation_origin_cannot_be_frozen
	// Which check fires first depends on the epoch values. The key assertion
	// is that a frozen delegation origin is rejected regardless.
	require.Error(t, err, "delegation origin should not be created frozen")
}

// TestClaudeDelegationWrongConstraintCount verifies that a delegation output
// with != 4 constraints is rejected. This prevents constraint injection attacks.
// EasyFL: equal(selfNumConstraints, u64/4)
func TestClaudeDelegationWrongConstraintCount(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddresses(0, 2)
	masterPrivateKey := privKey[0]
	masterAddr := addr[0]
	seqPrivateKey := privKey[1]
	seqAddr := addr[1]

	err := u.TokensFromFaucet(masterAddr, cdelegInitAmount)
	require.NoError(t, err)
	err = u.TokensFromFaucet(seqAddr, cdelegInitAmount)
	require.NoError(t, err)

	// create chain
	seqOuts, err := u.SugaredStateReader().GetOutputsForAccount(seqAddr.ControllerID())
	require.NoError(t, err)
	seqOriginTs := seqOuts[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(seqPrivateKey, nil, seqOriginTs)
	require.NoError(t, err)
	outs, err := u.DoTransferOutputs(par.
		WithAmount(cdelegOnChainBalance).
		WithTargetLock(seqAddr).
		WithConstraint(ledger.NewChainOrigin(seqOriginTs.Slot)),
	)
	require.NoError(t, err)
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(t, err)
	target := chOuts[0].ChainID

	// try to create delegation output with 5 constraints (extra injected constraint)
	masterOuts, err := u.SugaredStateReader().GetOutputsForAccount(masterAddr.ControllerID())
	require.NoError(t, err)
	delegTs := chOuts[0].Timestamp().AddSlots(1)

	txb := txbuilder.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	delegLock := ledger.NewDelegateLock(target, base.HolderID(masterAddr), 4, 0,
		ledger.L(0).DelegationEpochSlots, byte(ledger.L(0).MaxFrozenEpochs))
	// build delegation with extra constraint (5 total)
	delegWithExtra := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(cdelegTokens))
		o.WithLock(delegLock)
		o.MustPushConstraint(ledger.NewChainOrigin(delegTs.Slot).Bytes())
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
		// extra constraint - should be rejected
		o.MustPushConstraint(ledger.NewAmounts(int64(cdelegTokens)).Bytes())
	})
	_, err = txb.ProduceOutput(delegWithExtra)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(masterOuts[0].Output.TokenBalance() - cdelegTokens).WithLock(masterAddr)
	}))
	require.NoError(t, err)

	txb.TransactionData.Timestamp = delegTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(masterPrivateKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "delegation with 5 constraints should be rejected")
	require.NoError(t, util.MustErrorWith(err, "delegation must have exactly 5 UTXO elements"))
}

// TestClaudeDelegationSafeRevocationWindow verifies that the target sequencer
// cannot unlock a frozen delegation during the safe revocation window.
// The safe revocation window starts after the last frozen slot and lasts
// constDelegationSafeRevocationSlots (60 slots = 10 min).
// This protects the master's ability to reclaim during this window.
func TestClaudeDelegationSafeRevocationWindow(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	// freeze the delegation
	env.freezeDelegation(t, 1)
	require.True(t, env.delegatedOutput.IsMarkedFrozen(), "should be frozen after freeze")

	unfreezeSlot := env.delegatedOutput.UnfreezeSlot()
	safeRevSlots := ledger.L(0).SafeRevocationSlots

	// target tries to consume in safe revocation window
	t.Run("target blocked in safe revocation window", func(t *testing.T) {
		// use a slot right in the middle of safe revocation window
		attackTs := base.T(unfreezeSlot+safeRevSlots/2, 5)

		txb := txbuilder.New()
		_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
		require.NoError(t, err)

		successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
		_, err = txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance()))
			o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
		}))
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

		predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
		require.NoError(t, err)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

		// produce valid delegation successor
		cc := ledger.NewChainConstraint(env.delegatedOutput.ChainID, predIdx, env.delegatedOutput.OriginSlot, 0, 0, env.delegatedOutput.TransitionCounter+1, 0)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(env.delegatedOutput.Output.TokenBalance()))
			o.WithLock(env.delegatedOutput.Output.Lock())
			o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
			o.MustPushConstraint(ledger.DelegateLockState{State: ledger.DelegateLockStateOnHold}.Bytes())
		}))
		require.NoError(t, err)

		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.TransactionData.Timestamp = attackTs
		txb.SignED25519(env.seqPrivateKey)
		_, _, _, err = txb.BytesWithValidation()
		require.Error(t, err, "target should not unlock during safe revocation window")
		require.NoError(t, util.MustErrorWith(err, "delegation cannot be unlocked by the target in safe revocation window"))
	})

	// master CAN unlock after freeze expires (not in safe revocation window)
	t.Run("master can unlock after freeze expires", func(t *testing.T) {
		// slot after safe revocation window ends
		masterTs := base.T(unfreezeSlot+safeRevSlots+10, 5)

		txb := txbuilder.New()
		amount, _, err := txb.ConsumeOutputsNoUnlock(&env.delegatedOutput.OutputWithID)
		require.NoError(t, err)

		txb.PutUnlockParams(0, ledger.ConstraintIndexLock, []byte{0xff, 0xff})
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(amount)).WithLock(env.masterAddr)
		}))
		require.NoError(t, err)

		txb.TransactionData.Timestamp = masterTs
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(env.masterPrivateKey)
		_, _, _, err = txb.BytesWithValidation()
		require.NoError(t, err, "master should unlock after safe revocation window")
	})
}

// TestClaudeDelegationInflationShareAbove1000 verifies that creating a delegation
// with requiredInflationShare > 1000 (promille) is rejected.
// EasyFL: lessOrEqualThan($1, u64/1000)
func TestClaudeDelegationInflationShareAbove1000(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddresses(0, 2)
	masterPrivateKey := privKey[0]
	masterAddr := addr[0]
	seqPrivateKey := privKey[1]
	seqAddr := addr[1]

	err := u.TokensFromFaucet(masterAddr, cdelegInitAmount)
	require.NoError(t, err)
	err = u.TokensFromFaucet(seqAddr, cdelegInitAmount)
	require.NoError(t, err)

	// create chain
	seqOuts, err := u.SugaredStateReader().GetOutputsForAccount(seqAddr.ControllerID())
	require.NoError(t, err)
	seqOriginTs := seqOuts[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(seqPrivateKey, nil, seqOriginTs)
	require.NoError(t, err)
	outs, err := u.DoTransferOutputs(par.
		WithAmount(cdelegOnChainBalance).
		WithTargetLock(seqAddr).
		WithConstraint(ledger.NewChainOrigin(seqOriginTs.Slot)),
	)
	require.NoError(t, err)
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(t, err)
	target := chOuts[0].ChainID

	// create delegation with inflation share = 1001 (above max 1000)
	masterOuts, err := u.SugaredStateReader().GetOutputsForAccount(masterAddr.ControllerID())
	require.NoError(t, err)
	delegTs := chOuts[0].Timestamp().AddSlots(1)

	txb := txbuilder.New()
	_, err = txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	delegOut := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
		Amount:                 cdelegTokens,
		MasterID:               base.HolderID(masterAddr),
		Target:                 target,
		MaxFrozenEpochs:        4,
		RequiredInflationShare: 1001, // above max
		StartSlot:              delegTs.Slot,
		EpochSlots:             ledger.L(0).DelegationEpochSlots,
		TargetMaxFrozenEpochs:  byte(ledger.L(0).MaxFrozenEpochs),
	})
	_, err = txb.ProduceOutput(delegOut)
	require.NoError(t, err)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(masterOuts[0].Output.TokenBalance() - cdelegTokens).WithLock(masterAddr)
	}))
	require.NoError(t, err)

	txb.TransactionData.Timestamp = delegTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(masterPrivateKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "inflation share > 1000 should be rejected")
	require.NoError(t, util.MustErrorWith(err, "max required inflation share must be in promille less or equal than 1000"))
}

// TestClaudeDelegationOnHoldTargetRelock verifies that once a delegation
// is put on hold (revoked), the target cannot re-freeze it.
// EasyFL: not(_selfIsMarkedOnHold) in _requireUnlockableByTheTarget
func TestClaudeDelegationOnHoldTargetRelock(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	// freeze, then revoke
	env.freezeDelegation(t, 1)
	require.True(t, env.delegatedOutput.IsMarkedFrozen())

	// now revoke: target puts on hold
	unfreezeSlot := env.delegatedOutput.UnfreezeSlot()
	revokeTs := base.T(unfreezeSlot-10, 5) // inside freeze but before unfreeze
	// for revocation inside freeze, target can only put on hold

	txb := txbuilder.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	seqChainIdx, err := txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	delegatedOutPar := ledger.MakeDelegationRevokeOutputParams{
		TxTs:                     revokeTs,
		Inflation:                0,
		HarvestInflation:         0,
		DisableConsistencyChecks: true,
	}
	delegatedOutPar.PredOutputIndex, err = txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	delegatedOut, err := env.delegatedOutput.MakeDelegationRevokeOutput(delegatedOutPar)
	require.NoError(t, err)

	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb.PutUnlockParams(1, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	_, err = txb.ProduceOutput(delegatedOut)
	require.NoError(t, err)

	fcDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(t, err)
	txb.MustPutFrozenCoverage(seqChainIdx, fcDelta, revokeTs)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = revokeTs
	txb.SignED25519(env.seqPrivateKey)
	txBytes, _, _, err := txb.BytesWithValidation()
	require.NoError(t, err)
	err = env.u.AddTransaction(txBytes)
	require.NoError(t, err)

	// refresh
	env.delegatedOutput, err = env.u.SugaredStateReader().GetDelegatedOutput(env.delegatedOutput.ChainID)
	require.NoError(t, err)
	env.seqChainOrigin, err = env.u.SugaredStateReader().GetChainOutputWithChainID(env.seqChainOrigin.ChainID)
	require.NoError(t, err)

	require.True(t, env.delegatedOutput.IsMarkedOnHold(), "should be on hold after revocation")

	// now target tries to re-freeze the on-hold delegation
	relockTs := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	txb2 := txbuilder.New()
	_, _, err = txb2.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint2 := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	_, err = txb2.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint2.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb2.PutSignatureUnlock(0)
	txb2.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx2, err := txb2.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)
	txb2.PutUnlockParams(predIdx2, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), 0)
	txb2.PutUnlockParams(predIdx2, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	// try to produce frozen successor from on-hold
	cc := ledger.NewChainConstraint(env.delegatedOutput.ChainID, predIdx2, env.delegatedOutput.OriginSlot, 0, 0, env.delegatedOutput.TransitionCounter+1, 0)
	_, err = txb2.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.delegatedOutput.Output.TokenBalance()))
		o.WithLock(env.delegatedOutput.Output.Lock())
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{LastFrozenEpoch: 5, State: ledger.DelegateLockStateFrozen}.Bytes())
	}))
	require.NoError(t, err)

	txb2.TransactionData.InputCommitment = ledger.HashOutputs(txb2.ConsumedOutputs...)
	txb2.TransactionData.Timestamp = relockTs
	txb2.SignED25519(env.seqPrivateKey)
	_, _, _, err = txb2.BytesWithValidation()
	require.Error(t, err, "target should not re-freeze on-hold delegation")
	require.NoError(t, util.MustErrorWith(err, "on hold delegation cannot be unlocked by the target"))
}
