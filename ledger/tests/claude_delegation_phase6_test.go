// Phase 6 tests for the per-target delegation epoch params refactor.
// See claude/delegation_epoch_params.md.
//
// Covers:
//   - delegationParams bounds enforcement at chain origin
//   - delegationParams immutability across chain transit
//   - foundry-delegation: a foundry chain is delegated to a sequencer by
//     swapping the lock at index 2 to delegateLock, preserving the
//     foundry (and any foundryPolicy) byte-equal, and appending
//     delegateLockState at the last tuple position (Option C — last-
//     position state). The foundryNonDestructible policy's
//     selfImmutableOnSuccessorIndex(5) check still passes byte-equal
//     across the transit.

package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// delegationParams: bounds rejection at chain origin
// --------------------------------------------------------------------------

// TestDelegationParamsBoundsRejection verifies the EasyFL bounds check
// in delegationParams() fires for each direction (epochSlots too small /
// too big, maxFrozenEpochs too small / too big) and that a value at the
// boundary passes.
func TestDelegationParamsBoundsRejection(t *testing.T) {
	lib := ledger.L(0)

	tryOrigin := func(t *testing.T, epochSlots uint32, maxFrozenEpochs byte) error {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey, _, addr := u.GenerateAddress(1)
		require.NoError(t, u.TokensFromFaucet(addr, 10_000_000_000))

		par, err := u.MakeTransferInputData(privKey, nil, base.NilLedgerTime)
		require.NoError(t, err)
		par.Timestamp = par.Inputs[0].ID.Timestamp().AddSlots(1)
		_, err = u.DoTransferOutputs(par.
			WithAmount(200_000_000).
			WithTargetLock(addr).
			WithConstraint(ledger.NewChainOrigin(par.Timestamp.Slot)).
			WithConstraint(ledger.NewDelegationParams(epochSlots, maxFrozenEpochs),
				ledger.ConstraintIndexDelegationParams),
		)
		return err
	}

	t.Run("at_lower_bounds_ok", func(t *testing.T) {
		require.NoError(t, tryOrigin(t,
			lib.DelegationEpochSlotsMin, byte(lib.DelegationMaxFrozenEpochsMin)))
	})
	t.Run("at_upper_bounds_ok", func(t *testing.T) {
		require.NoError(t, tryOrigin(t,
			lib.DelegationEpochSlotsMax, byte(lib.DelegationMaxFrozenEpochsMax)))
	})
	t.Run("epochSlots_too_small", func(t *testing.T) {
		err := tryOrigin(t, lib.DelegationEpochSlotsMin-1, byte(lib.MaxFrozenEpochs))
		require.NoError(t, util.MustErrorWith(err, "delegationParams epochSlots below minimum"))
	})
	t.Run("epochSlots_too_big", func(t *testing.T) {
		err := tryOrigin(t, lib.DelegationEpochSlotsMax+1, byte(lib.MaxFrozenEpochs))
		require.NoError(t, util.MustErrorWith(err, "delegationParams epochSlots above maximum"))
	})
	t.Run("maxFrozenEpochs_too_small", func(t *testing.T) {
		err := tryOrigin(t, lib.DelegationEpochSlots, byte(lib.DelegationMaxFrozenEpochsMin-1))
		require.NoError(t, util.MustErrorWith(err, "delegationParams maxFrozenEpochs below minimum"))
	})
	t.Run("maxFrozenEpochs_too_big", func(t *testing.T) {
		err := tryOrigin(t, lib.DelegationEpochSlots, byte(lib.DelegationMaxFrozenEpochsMax+1))
		require.NoError(t, util.MustErrorWith(err, "delegationParams maxFrozenEpochs above maximum"))
	})
}

// --------------------------------------------------------------------------
// delegationParams: immutability across chain transit
// --------------------------------------------------------------------------

// TestDelegationParamsImmutable verifies a sequencer chain that carries
// delegationParams at index 6 cannot be transited to a successor that
// changes those bytes. selfImmutableOnSuccessorIndex(6) on the
// delegationParams constraint enforces this.
func TestDelegationParamsImmutable(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	require.NoError(t, u.TokensFromFaucet(addr, 10_000_000_000))

	par, err := u.MakeTransferInputData(privKey, nil, base.NilLedgerTime)
	require.NoError(t, err)
	par.Timestamp = par.Inputs[0].ID.Timestamp().AddSlots(1)

	origDP := ledger.NewDelegationParams(ledger.L(0).DelegationEpochSlots, byte(ledger.L(0).MaxFrozenEpochs))
	outs, err := u.DoTransferOutputs(par.
		WithAmount(200_000_000).
		WithTargetLock(addr).
		WithConstraint(ledger.NewChainOrigin(par.Timestamp.Slot)).
		WithConstraint(origDP, ledger.ConstraintIndexDelegationParams),
	)
	require.NoError(t, err)
	chOuts, err := ledger.FilterChainOutputs(outs)
	require.NoError(t, err)
	require.EqualValues(t, 1, len(chOuts))
	chOrigin := chOuts[0]

	// Transit the chain, attempting to replace delegationParams with a
	// different (still in-bounds) value. selfImmutableOnSuccessorIndex(6)
	// on the consumed side reads our delegationParams and the produced
	// successor's index 6; byte mismatch fails.
	newDP := ledger.NewDelegationParams(origDP.EpochSlots+1, origDP.MaxFrozenEpochs)

	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(chOrigin.Output, chOrigin.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, predIdx)
	txb.PutSignatureUnlock(0)
	ts := chOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))

	successorCC := ledger.NewChainConstraint(chOrigin.ChainID, predIdx, chOrigin.OriginSlot,
		chOrigin.CumulativeChainInflation, chOrigin.CumulativeBranchBonus,
		chOrigin.TransitionCounter+1, chOrigin.BranchCounter)
	succ := chOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successorCC.Bytes(), ledger.ConstraintIndexChain)
		// Replace delegationParams with the mutated one.
		o.PutConstraint(newDP.Bytes(), ledger.ConstraintIndexDelegationParams)
	})
	succIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(privKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err)
	// selfImmutableOnSuccessorIndex fails — the equality check on
	// position 6 returns false, which trips the surrounding AND inside
	// delegationParams (no explicit !!! message, the constraint just
	// returns falsy and the chain's evaluation framework reports the
	// constraint failure).
	require.NoError(t, util.MustErrorWith(err, "delegationParams"))
}

// --------------------------------------------------------------------------
// Foundry-delegation: the canonical Option C scenario
// --------------------------------------------------------------------------

// foundryDelegationEnv extends the plain delegation test environment
// with a separately-owned foundry chain. The foundry's controller is
// the master that will delegate the foundry chain to the sequencer.
type foundryDelegationEnv struct {
	td      *testData
	foundry base.ChainID
}

// newFoundryDelegationEnv sets up a sequencer chain (the delegation
// target, accepting delegations via delegationParams at index 6) and a
// foundry chain owned by the master account. policy is optional: when
// non-nil it goes at ConstraintIndexFoundryPolicy on the foundry origin
// — typically foundryNonDestructible to exercise the canonical
// "non-destructible foundry, then delegate it" scenario.
func newFoundryDelegationEnv(t *testing.T, policy []byte) *foundryDelegationEnv {
	td := &testData{T: t}
	td.init() // also creates the sequencer chain with delegationParams

	// Create a foundry chain owned by the master (the "delegator-to-be").
	outs := getSourceOutputs(t, td.u, td.masterAddr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	txb := txbuilder.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	ts = base.MaximumTime(inTs, ts)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	const foundryOnChain = uint64(500_000_000)
	foundryOut := txbuilder.MakeFoundryOriginOutput(foundryOnChain, td.masterAddr, ts.Slot, 0, policy)
	require.NoError(t, foundryOut.EnoughAmountForStorageDeposit())
	foundryIdx, err := txb.ProduceOutput(foundryOut)
	require.NoError(t, err)
	addRemainderIfNeeded(t, txb, td.masterAddr)

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(td.masterPrivateKey)
	txBytes, txid, failedTx, err := txb.BytesWithValidation()
	require.NoError(t, err, "foundry origin build failed: %s", failedTx)
	require.NoError(t, td.u.AddTransaction(txBytes))

	foundryOid, err := base.NewOutputID(txid, foundryIdx)
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(foundryOid)

	// Settle the foundry tag: do a zero-supply transit so the
	// foundry constraint's tag flips from NilChainID at origin to the
	// real chain ID. The foundryPolicy at index 5 (if any) survives
	// byte-equal across this transit, exercising the policy's
	// selfImmutableOnSuccessorIndex(5) check.
	{
		settleTxb := txbuilder.New()
		fIn := &ledger.OutputDataWithChainID{
			OutputDataWithID: ledger.OutputDataWithID{ID: foundryOid, Data: foundryOut.Bytes()},
			ChainID:          chainID,
		}
		_, err = settleTxb.TransitFoundry(fIn, 0)
		require.NoError(t, err)
		settleTxb.PutSignatureUnlock(0)
		// pure-PRXI funding for the storage deposit on the produced
		// foundry chain output (its size unchanged, so the existing
		// 500_000_000 balance keeps covering the deposit, no
		// additional funding strictly required).
		settleTs := ts.AddTicks(int(ledger.L(0).TransactionPace))
		settleTxb.TransactionData.Timestamp = settleTs
		settleTxb.TransactionData.InputCommitment = ledger.HashOutputs(settleTxb.ConsumedOutputs...)
		settleTxb.SignED25519(td.masterPrivateKey)
		settleBytes, _, failed, err := settleTxb.BytesWithValidation()
		require.NoError(t, err, "foundry tag-settle transit failed: %s", failed)
		require.NoError(t, td.u.AddTransaction(settleBytes))
	}

	return &foundryDelegationEnv{
		td:      td,
		foundry: chainID,
	}
}

// delegateFoundryChain transits the foundry chain to a delegation
// pointing at the sequencer target. The transit:
//   - replaces sigLock at index 2 with delegateLock(master=master,
//     target=sequencer)
//   - preserves the foundry constraint at index 4 byte-equal
//   - preserves the foundryPolicy at index 5 byte-equal (if attached)
//   - appends delegateLockState at the last tuple position
//
// Returns the transit tx error.
func (e *foundryDelegationEnv) delegateFoundryChain(t *testing.T) error {
	t.Helper()
	td := e.td

	// Fetch the current foundry chain output.
	chData, err := td.u.StateReader().GetUTXOForChainID(e.foundry)
	require.NoError(t, err)
	chParsed, err := chData.Parse()
	require.NoError(t, err)
	chIn, ok := ledger.AsOutputWithChainID(chParsed.Output, chParsed.ID)
	require.True(t, ok)

	// Build the delegation lock with the target's inlined params.
	lib := ledger.L(0)
	delLock := ledger.NewDelegateLock(
		td.target,
		base.HolderID(td.masterAddr),
		byte(lib.MaxFrozenEpochs), // delegator's chosen max = target's max
		900,                       // 90% inflation share
		lib.DelegationEpochSlots,
		byte(lib.MaxFrozenEpochs),
	)

	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(chIn.Output, chIn.ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, predIdx)
	txb.PutSignatureUnlock(0)
	ts := chIn.Timestamp().AddTicks(int(lib.TransactionPace))

	// Build the successor by cloning the existing foundry chain output
	// and overlaying the new lock at index 2 + new chain constraint at
	// index 3 + appended delegateLockState at the last position. Clone
	// preserves the foundry at index 4 and foundryPolicy at index 5 (if
	// any) byte-equal — which is what foundryNonDestructible /
	// foundryMaxSupply's selfImmutableOnSuccessorIndex(5) requires.
	successorCC := ledger.NewChainConstraint(
		chIn.ChainID, predIdx, chIn.OriginSlot,
		chIn.CumulativeChainInflation, chIn.CumulativeBranchBonus,
		chIn.TransitionCounter+1, chIn.BranchCounter,
	)
	succ := chIn.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithLock(delLock)
		o.PutConstraint(successorCC.Bytes(), ledger.ConstraintIndexChain)
		// Append delegateLockState at the last position. The output
		// builder picks the next available index after the existing
		// constraints; for foundry-no-policy that's 5, for foundry-with-
		// policy that's 6. Either way it lands at NumElements - 1, which
		// is what Option C requires.
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	})
	succIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(td.masterPrivateKey)
	txBytes, _, failedTx, err := txb.BytesWithValidation()
	if err != nil {
		t.Logf("foundry-delegate build failed:\n%s", failedTx)
		return err
	}
	return td.u.AddTransaction(txBytes)
}

// TestDelegateFoundryChainNoPolicy delegates a plain foundry (no policy
// at index 5) to the sequencer target. The produced output has
// delegateLock at 2, chain at 3, foundry at 4, delegateLockState at 5 —
// delegateLockState is the last position, satisfying Option C.
func TestDelegateFoundryChainNoPolicy(t *testing.T) {
	e := newFoundryDelegationEnv(t, nil /* no policy */)
	require.NoError(t, e.delegateFoundryChain(t))

	// Re-read the delegated output and verify its shape.
	delOut, err := e.td.u.SugaredStateReader().GetChainOutputWithChainID(e.foundry)
	require.NoError(t, err)
	require.EqualValues(t, ledger.DelegateLockName, delOut.Output.Lock().Name(),
		"chain output should now be a delegation")
	// Foundry preserved at index 4.
	fBytes, err := delOut.Output.ConstraintAt(ledger.ConstraintIndexFoundry)
	require.NoError(t, err)
	_, err = ledger.FoundryFromBytes(fBytes)
	require.NoError(t, err, "foundry constraint at index 4 preserved")
	// delegateLockState at the last index.
	n := delOut.Output.NumElements()
	stateBytes, err := delOut.Output.ConstraintAt(byte(n - 1))
	require.NoError(t, err)
	_, err = ledger.DelegateLockStateFromBytesWithLib(stateBytes, ledger.L(0))
	require.NoError(t, err, "last constraint is delegateLockState")
	// Concretely: amounts (0), index-values (1), delegateLock (2),
	// chain (3), foundry (4), state (5) → 6 elements.
	require.EqualValues(t, 6, n)
}

// TestDelegateFoundryChainNonDestructible is the canonical Option C
// scenario: a foundryNonDestructible foundry chain is delegated to a
// sequencer. The foundry's policy at index 5 self-locks via
// selfImmutableOnSuccessorIndex(5); the transit preserves it byte-equal
// so the policy still passes, and the delegateLockState lands at index
// 6 (= last).
func TestDelegateFoundryChainNonDestructible(t *testing.T) {
	policy := ledger.FoundryNonDestructibleBytecode()
	e := newFoundryDelegationEnv(t, policy)
	require.NoError(t, e.delegateFoundryChain(t))

	delOut, err := e.td.u.SugaredStateReader().GetChainOutputWithChainID(e.foundry)
	require.NoError(t, err)
	require.EqualValues(t, ledger.DelegateLockName, delOut.Output.Lock().Name())

	// Foundry constraint preserved at 4, foundryPolicy preserved at 5,
	// delegateLockState appended at 6 (= NumElements - 1).
	fBytes, err := delOut.Output.ConstraintAt(ledger.ConstraintIndexFoundry)
	require.NoError(t, err)
	_, err = ledger.FoundryFromBytes(fBytes)
	require.NoError(t, err)

	gotPolicy, err := delOut.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy)
	require.NoError(t, err)
	require.Equal(t, policy, gotPolicy, "foundryNonDestructible policy preserved byte-equal across transit")

	n := delOut.Output.NumElements()
	require.EqualValues(t, 7, n,
		"layout: amounts, iv, delegateLock, chain, foundry, foundryPolicy, delegateLockState")
	stateBytes, err := delOut.Output.ConstraintAt(byte(n - 1))
	require.NoError(t, err)
	_, err = ledger.DelegateLockStateFromBytesWithLib(stateBytes, ledger.L(0))
	require.NoError(t, err)
}

// TestDelegateLockStateMustBeLast injects an extra constraint after the
// delegateLockState; the state's own "I must be at the last index"
// check (Option C) refuses. The structural check inside the
// delegateLock body also panics via parseBytecode (the last-position
// bytecode isn't a delegateLockState). Either path is acceptable as
// long as the tx is rejected; we match on a stable substring shared by
// every related failure path.
func TestDelegateLockStateMustBeLast(t *testing.T) {
	td := &testData{T: t}
	td.init()

	// Build a delegation origin output and try to append junk after the
	// state. We use the existing delegationOriginDirect builder via a
	// manual tx so we can inject the extra constraint.
	ts := td.seqChainOrigin.Timestamp().AddTicks(1)
	masterOuts, _ := td.u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(
		td.masterAddr, delegatedTokens+1_000)
	require.True(t, len(masterOuts) > 0)

	lib := ledger.L(0)
	delLock := ledger.NewDelegateLock(td.target, base.HolderID(td.masterAddr), 4, 0,
		lib.DelegationEpochSlots, byte(lib.MaxFrozenEpochs))

	txb := txbuilder.New()
	idx, err := txb.ConsumeOutput(masterOuts[0].Output, masterOuts[0].ID)
	require.NoError(t, err)
	require.EqualValues(t, 0, idx)
	txb.PutSignatureUnlock(0)

	delOriginOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(delegatedTokens))
		o.WithLock(delLock)
		o.MustPushConstraint(ledger.NewChainOrigin(ts.Slot).Bytes())
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
		// Junk after the state — would put delegateLockState at index 4
		// while NumElements is 6 → state's "must be last" check fails,
		// and the parseBytecode lookup in _validStructureProduced panics
		// on the non-delegateLockState bytecode at index 5.
		o.MustPushConstraint(ledger.NewAmounts(int64(123)).Bytes())
	})
	_, err = txb.ProduceOutput(delOriginOut)
	require.NoError(t, err)
	// Remainder back to master so the tx balances.
	remainder := masterOuts[0].Output.TokenBalance() - delegatedTokens
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(remainder).WithLock(td.masterAddr)
	}))
	require.NoError(t, err)

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(td.masterPrivateKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "delegation with junk after delegateLockState must be rejected")
}
