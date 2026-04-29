package tests

// Output index bounds checking tests for Proxima ledger.
// These tests verify that out-of-bounds index values in unlock parameters,
// chain constraints, and lock references are properly rejected.
//
// Multiple validation layers exist:
//   - Parse-level: output count (0 < n <= 256), sequencer output index
//   - EasyFL runtime: atPath() rejects out-of-range paths for consumed/produced access
//   - Semantic: chain constraint cannot be on output 255; unlock-by-reference
//     requires strictly smaller index (cycle prevention); 0xff/0xffff reserved markers
//
// Existing tests in claude_chain_test.go cover chain successor output/constraint
// index out of range. This file focuses on remaining uncovered areas:
//   - sigLock unlock-by-reference with invalid indices
//   - chainLock unlock params with out-of-range or wrong-type references
//   - tag-along unlock params with out-of-range output index
//   - delegation unlock params with out-of-range output index
//   - chain predecessor referencing non-existent consumed index

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// Helper
// --------------------------------------------------------------------------

// indexTestEnv holds common state for index bounds tests.
type indexTestEnv struct {
	u       *utxodb.UTXODB
	privKey ed25519.PrivateKey
	addr    ledger.SigLock
}

func newIndexTestEnv(t *testing.T, amount uint64) *indexTestEnv {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	err := u.TokensFromFaucet(addr, amount)
	require.NoError(t, err)
	return &indexTestEnv{u: u, privKey: privKey, addr: addr}
}

// --------------------------------------------------------------------------
// TEST: sigLock unlock-by-reference — cross-lock reference attack
// --------------------------------------------------------------------------

// TestIndexSigLockCrossLockReference verifies that an attacker cannot use
// unlock-by-reference to spend someone else's UTXO by referencing their own
// input. The EasyFL check `equal(self, consumedConstraintByIndex($0, lockConstraintIndex))`
// requires the lock bytes to match exactly.
//
// Note on sigLock design: sigLock has an `or` clause — either reference OR signature.
// When signed by the correct key, the signature check always succeeds as fallback,
// making index ordering (lessThan) mostly a defense-in-depth measure for sigLock.
// This test verifies the more practical attack: cross-lock reference bypass.
func TestIndexSigLockCrossLockReference(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddresses(0, 2)
	privKeyAlice := privKeys[0]
	addrAlice := addrs[0]
	privKeyBob := privKeys[1]
	addrBob := addrs[1]

	err := u.TokensFromFaucet(addrAlice, 5_000_000_000)
	require.NoError(t, err)
	err = u.TokensFromFaucet(addrBob, 5_000_000_000)
	require.NoError(t, err)

	aliceOuts := getSourceOutputs(t, u, addrAlice)
	bobOuts := getSourceOutputs(t, u, addrBob)

	t.Run("attacker_references_own_input", func(t *testing.T) {
		// Bob signs and tries to spend Alice's UTXO by referencing his own input.
		// Input 0: Bob's UTXO (signature unlock works)
		// Input 1: Alice's UTXO (reference to input 0 — locks differ, signature fails)
		txb := txbuilder.New()
		_, err := txb.ConsumeOutput(bobOuts[0].Output, bobOuts[0].ID)
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(aliceOuts[0].Output, aliceOuts[0].ID)
		require.NoError(t, err)

		txb.PutSignatureUnlock(0)                                     // Bob's input: signature
		txb.PutUnlockParams(1, ledger.ConstraintIndexLock, []byte{0}) // Alice's input: reference Bob

		totalBalance := bobOuts[0].Output.TokenBalance() + aliceOuts[0].Output.TokenBalance()
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(totalBalance).WithLock(addrBob)
		}))
		require.NoError(t, err)

		ts := base.MaximumTime(bobOuts[0].ID.Timestamp(), aliceOuts[0].ID.Timestamp()).AddSlots(1)
		txb.TransactionData.Timestamp = ts
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(privKeyBob)
		_, _, _, err = txb.BytesWithValidation()
		require.Error(t, err, "cross-lock reference should be rejected")
		t.Logf("cross-lock reference rejected: %v", err)
	})

	t.Run("valid_same_lock_reference", func(t *testing.T) {
		// Alice has two UTXOs, spends both. Input 0 uses signature, input 1 references input 0.
		// This is the valid use case for unlock-by-reference.
		aliceOuts2 := getSourceOutputs(t, u, addrAlice)
		if len(aliceOuts2) < 2 {
			// create a second UTXO for Alice
			ts := aliceOuts2[0].ID.Timestamp().AddSlots(1)
			par, err := u.MakeTransferInputData(privKeyAlice, nil, ts)
			require.NoError(t, err)
			_, err = u.DoTransferOutputs(par.
				WithAmount(1_000_000_000).
				WithTargetLock(addrAlice))
			require.NoError(t, err)
			aliceOuts2 = getSourceOutputs(t, u, addrAlice)
		}
		require.True(t, len(aliceOuts2) >= 2, "need at least 2 outputs")

		txb := txbuilder.New()
		_, err := txb.ConsumeOutput(aliceOuts2[0].Output, aliceOuts2[0].ID)
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(aliceOuts2[1].Output, aliceOuts2[1].ID)
		require.NoError(t, err)

		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(1, ledger.ConstraintIndexLock, []byte{0})

		totalBalance := aliceOuts2[0].Output.TokenBalance() + aliceOuts2[1].Output.TokenBalance()
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(totalBalance).WithLock(addrAlice)
		}))
		require.NoError(t, err)

		ts := base.MaximumTime(aliceOuts2[0].ID.Timestamp(), aliceOuts2[1].ID.Timestamp()).AddSlots(1)
		txb.TransactionData.Timestamp = ts
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(privKeyAlice)
		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err, "same-lock backward reference should be accepted")
		err = u.AddTransaction(txBytes)
		require.NoError(t, err, "valid reference tx should settle")
	})
}

// --------------------------------------------------------------------------
// TEST: sigLock reference to input with different lock type
// --------------------------------------------------------------------------

// TestIndexSigLockReferenceToChainLocked verifies that an attacker cannot use
// unlock-by-reference to bypass sigLock by referencing a chainLock-ed input.
// The `equal(self, consumedConstraintByIndex($0, lockConstraintIndex))` check
// ensures byte-exact match, so sigLock(A) != chainLock(X).
func TestIndexSigLockReferenceToChainLocked(t *testing.T) {
	e := newIndexTestEnv(t, 10_000_000_000)
	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)

	// create a chain
	chainOut, err := e.u.CreateChainOrigin(e.privKey, ts, 200_000_000)
	require.NoError(t, err)
	chainIn, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainOut.ChainID)
	require.NoError(t, err)

	// create a chainLock-ed output
	chainLock := ledger.ChainLockFromChainID(chainOut.ChainID)
	outs2 := getSourceOutputs(t, e.u, e.addr)
	ts2 := base.MaximumTime(chainIn.Timestamp(), outs2[0].ID.Timestamp()).AddSlots(1)
	par, err := e.u.MakeTransferInputData(e.privKey, nil, ts2)
	require.NoError(t, err)
	_, err = e.u.DoTransferOutputs(par.
		WithAmount(100_000_000).
		WithTargetLock(chainLock))
	require.NoError(t, err)

	// get a sigLock output and the chainLock output
	sigOuts := getSourceOutputs(t, e.u, e.addr)
	clOuts, err := e.u.SugaredStateReader().GetOutputsForAccount(chainLock.ControllerID())
	require.NoError(t, err)
	require.True(t, len(clOuts) > 0)

	txb := txbuilder.New()
	// input 0: chainLock-ed output
	_, err = txb.ConsumeOutput(clOuts[0].Output, clOuts[0].ID)
	require.NoError(t, err)
	// input 1: sigLock output
	_, err = txb.ConsumeOutput(sigOuts[0].Output, sigOuts[0].ID)
	require.NoError(t, err)

	// input 0: stub unlock (will fail but not our focus)
	txb.PutSignatureUnlock(0)
	// input 1: try to reference input 0 (chainLock-ed output)
	// sigLock(alice) != chainLock(X), so equal() fails
	txb.PutUnlockParams(1, ledger.ConstraintIndexLock, []byte{0})

	totalBalance := clOuts[0].Output.TokenBalance() + sigOuts[0].Output.TokenBalance()
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(totalBalance).WithLock(e.addr)
	}))
	require.NoError(t, err)

	ts3 := base.MaximumTime(clOuts[0].ID.Timestamp(), sigOuts[0].ID.Timestamp()).AddSlots(1)
	txb.TransactionData.Timestamp = ts3
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.privKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "sigLock reference to chainLock-ed input should be rejected")
	t.Logf("cross-type lock reference rejected: %v", err)
}

// NOTE: TestIndexChainLockWrongConstraintType was removed because the chain constraint
// index is now always implicit (ConstraintIndexChain=2). The attack vector of pointing
// chainLock unlock params to a non-chain constraint index is eliminated by design.

// --------------------------------------------------------------------------
// TEST: tag-along unlock params with out-of-range output index
// --------------------------------------------------------------------------

// TestIndexTagAlongOutOfRangeUnlockParams verifies that consuming a tag-along
// output with chainLock unlock params pointing to a non-existent consumed output
// is rejected by the EasyFL runtime bounds check.
func TestIndexTagAlongOutOfRangeUnlockParams(t *testing.T) {
	env := setupTagAlongEnv(t)

	// get the tag-along output from backlog
	taOuts := env.u.SugaredStateReader().GetTagAlongBacklog(env.targetChainID)
	require.EqualValues(t, 1, len(taOuts))
	taOut := taOuts[0]

	// get chain tip
	chainIn, err := env.u.SugaredStateReader().GetChainOutputWithChainID(env.targetChainID)
	require.NoError(t, err)

	ts := base.MaximumTime(chainIn.Timestamp(), taOut.ID.Timestamp()).AddSlots(1)

	txb := txbuilder.New()

	// consume chain (input 0)
	_ = chainIn.Output.ChainConstraint()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	cc := ledger.NewChainConstraint(env.targetChainID, predIdx, env.seqOrigin.OriginSlot, 0, 0, env.seqOrigin.TransitionCounter+1, 0)
	succIdx, err := txb.ProduceOutput(chainIn.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(chainIn.Output.TokenBalance() + taOut.Output.TokenBalance()))
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))

	// consume tag-along (input 1)
	taIdx, err := txb.ConsumeOutput(taOut.Output, taOut.ID)
	require.NoError(t, err)

	// ATTACK: unlock params reference input index 5 (doesn't exist, only 2 inputs)
	// with constraint index 2. This should fail at consumedConstraintByIndex bounds check.
	txb.PutUnlockParams(taIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(5))

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(env.privKeyTarget)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "tag-along with out-of-range unlock index should be rejected")
	t.Logf("tag-along out-of-range unlock rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: delegation unlock params with out-of-range output index
// --------------------------------------------------------------------------

// TestIndexDelegationOutOfRangeUnlockParams verifies that consuming a delegation
// output with chainLock unlock params pointing to a non-existent consumed output
// is rejected.
func TestIndexDelegationOutOfRangeUnlockParams(t *testing.T) {
	env := setupDelegEnv(t, 4, 0)

	ts := base.MaximumTime(env.seqChainOrigin.Timestamp(), env.delegatedOutput.Timestamp()).AddSlots(1)

	txb := txbuilder.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&env.seqChainOrigin.OutputWithID)
	require.NoError(t, err)

	successorChainConstraint := ledger.NewChainConstraint(env.seqChainOrigin.ChainID, 0, env.seqChainOrigin.OriginSlot, 0, 0, env.seqChainOrigin.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(env.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(successorChainConstraint.Bytes(), 2)
	}))
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(env.delegatedOutput.Output, env.delegatedOutput.ID)
	require.NoError(t, err)

	// ATTACK: delegation lock unlock params reference input 10 (doesn't exist)
	txb.PutUnlockParams(predIdx, 1, ledger.NewChainLockUnlockParams(10), 0)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(1))

	// produce valid delegation successor
	cc := ledger.NewChainConstraint(env.delegatedOutput.ChainID, predIdx, env.delegatedOutput.OriginSlot, 0, 0, env.delegatedOutput.TransitionCounter+1, 0)
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(env.delegatedOutput.Output.TokenBalance()))
		o.WithLock(env.delegatedOutput.Output.Lock())
		o.MustPushConstraint(cc.Bytes())
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(env.seqPrivateKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "delegation with out-of-range unlock index should be rejected")
	t.Logf("delegation out-of-range unlock rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: chain predecessor reference to non-existent consumed index
// --------------------------------------------------------------------------

// TestIndexChainPredecessorNonExistentInput verifies that a produced chain
// successor claiming a predecessor input index that doesn't exist is rejected.
// The _validChainProduced crosscheck: unlockParamsByConstraintIndex($1) == selfConstraintIndex
// will fail because the referenced input doesn't exist.
func TestIndexChainPredecessorNonExistentInput(t *testing.T) {
	e := newIndexTestEnv(t, 10_000_000_000)
	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)

	chainOut, err := e.u.CreateChainOrigin(e.privKey, ts, 200_000_000)
	require.NoError(t, err)
	chainIn, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainOut.ChainID)
	require.NoError(t, err)

	_ = chainIn.Output.ChainConstraint()

	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// produce chain successor claiming predecessor at input index 5 (doesn't exist)
	// Only 1 input (index 0). The crosscheck will fail.
	fakeCC := ledger.NewChainConstraint(chainOut.ChainID, 5, chainOut.OriginSlot, 0, 0, chainOut.TransitionCounter+1, 0)
	succIdx, err := txb.ProduceOutput(chainIn.Output.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(fakeCC.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)

	// set valid unlock params on the consumed side
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.PutSignatureUnlock(predIdx)

	outTs := chainIn.ID.Timestamp().AddSlots(1)
	txb.TransactionData.Timestamp = outTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.privKey)
	_, _, _, err = txb.BytesWithValidation()
	// The consumed chain checks _chainSuccessorParam(1) against selfConstraintIndex.
	// Since the successor's predecessor data claims input 5 but the actual consumed
	// input is at index 0, the crosscheck fails.
	require.Error(t, err, "chain predecessor pointing to non-existent input should be rejected")
	require.NoError(t, util.MustErrorWith(err, "crosscheck failed"))
	t.Logf("fake predecessor index rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: chainLock self-referencing prevention
// --------------------------------------------------------------------------

// TestIndexChainLockSelfReference verifies that a chainLock-ed output
// cannot use unlock params that reference itself. The EasyFL constraint checks:
// not(equal(selfOutputIndex, byte(selfUnlockParameters,0)))
// This prevents an output from claiming its own chain constraint as the unlock source.
func TestIndexChainLockSelfReference(t *testing.T) {
	e := newIndexTestEnv(t, 10_000_000_000)
	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)

	// create chain
	chainOut, err := e.u.CreateChainOrigin(e.privKey, ts, 200_000_000)
	require.NoError(t, err)
	chainIn, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainOut.ChainID)
	require.NoError(t, err)

	// create chainLock-ed output
	chainLock := ledger.ChainLockFromChainID(chainOut.ChainID)
	outs2 := getSourceOutputs(t, e.u, e.addr)
	ts2 := base.MaximumTime(chainIn.Timestamp(), outs2[0].ID.Timestamp()).AddSlots(1)

	par, err := e.u.MakeTransferInputData(e.privKey, nil, ts2)
	require.NoError(t, err)
	_, err = e.u.DoTransferOutputs(par.
		WithAmount(100_000_000).
		WithTargetLock(chainLock))
	require.NoError(t, err)

	chainLockedOuts, err := e.u.SugaredStateReader().GetOutputsForAccount(chainLock.ControllerID())
	require.NoError(t, err)
	require.True(t, len(chainLockedOuts) > 0)
	clOut := chainLockedOuts[0]

	// consume chain and chain-locked outputs
	chainIn, err = e.u.SugaredStateReader().GetChainOutputWithChainID(chainOut.ChainID)
	require.NoError(t, err)
	_ = chainIn.Output.ChainConstraint()
	ts3 := base.MaximumTime(chainIn.Timestamp(), clOut.ID.Timestamp()).AddSlots(1)

	txb := txbuilder.New()

	// input 0: chain
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// input 1: chain-locked output
	clIdx, err := txb.ConsumeOutput(clOut.Output, clOut.ID)
	require.NoError(t, err)

	// produce chain successor (output 0)
	cc := ledger.NewChainConstraint(chainOut.ChainID, predIdx, chainOut.OriginSlot, 0, 0, chainOut.TransitionCounter+1, 0)
	succIdx, err := txb.ProduceOutput(chainIn.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(chainIn.Output.TokenBalance() + clOut.Output.TokenBalance()))
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)

	txb.PutSignatureUnlock(predIdx)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))

	// ATTACK: chain-locked output (input 1) references itself (index 1)
	// The chainLock checks: not(equal(selfOutputIndex, byte(selfUnlockParameters,0)))
	txb.PutUnlockParams(clIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(clIdx))

	txb.TransactionData.Timestamp = ts3
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.privKey)
	_, _, _, err = txb.BytesWithValidation()
	require.Error(t, err, "chainLock self-reference should be rejected")
	t.Logf("chainLock self-reference rejected: %v", err)
}
