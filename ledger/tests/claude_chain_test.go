// Chain constraint validation tests for Proxima ledger.
// These tests verify that the chain constraint (UTXO accounts / chained accounts)
// correctly enforces all validation rules during chain lifecycle:
// origin creation, successor transitions, and chain termination.
//
// All tests assume inflation = 0. Inflation-related chain behavior
// is a separate topic to be tested independently.
//
// See claude/chain_constraint.md for detailed chain constraint documentation.

package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// --------------------------------------------------------------------------
// Helpers for chain tests
// --------------------------------------------------------------------------

// chainTestEnv holds common state for chain constraint tests.
type chainTestEnv struct {
	u       *utxodb.UTXODB
	privKey ed25519.PrivateKey
	addr    ledger.SigLock
}

// newChainTestEnv creates a fresh UTXODB with a funded address.
func newChainTestEnv(t *testing.T, amount uint64) *chainTestEnv {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	err := u.TokensFromFaucet(addr, amount)
	require.NoError(t, err)
	return &chainTestEnv{u: u, privKey: privKey, addr: addr}
}

// createChainOrigin creates a chain origin output with the given amount using
// the high-level UTXODB helper. Returns the chain output with derived chain ID.
func (e *chainTestEnv) createChainOrigin(t *testing.T, amount uint64) *ledger.OutputWithChainID {
	t.Helper()
	// Get timestamp from actual outputs to avoid timing issues
	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	chainOut, err := e.u.CreateChainOrigin(e.privKey, ts, amount)
	require.NoError(t, err)
	return chainOut
}

// getChainOutput retrieves the current chain output from state by chain ID.
func (e *chainTestEnv) getChainOutput(t *testing.T, chainID base.ChainID) *ledger.OutputWithID {
	t.Helper()
	chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	parsed, err := chs.Parse()
	require.NoError(t, err)
	return parsed
}

// buildChainTransition builds a chain transition transaction consuming the given
// chain output and producing a successor with the same amount and lock.
// The modifier function allows tests to tamper with the chain constraint or unlock params.
// Returns the transaction bytes and the builder.
func (e *chainTestEnv) buildChainTransition(
	t *testing.T,
	chainIn *ledger.OutputWithID,
	chainData *ledger.OutputWithChainID,
	modifier func(txb *exhelp.Builder, predIdx byte, succIdx *byte),
) ([]byte, *exhelp.Builder) {
	t.Helper()

	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc, "output must have a chain constraint")

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// Build successor chain constraint with correct values.
	// TransitionCounter increments each transition; for the helper we use 1
	// (callers doing multi-step transitions override via modifier).
	nextCC := ledger.NewChainConstraint(
		chainData.ChainID, predIdx,
		cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0,
	)

	chainOut := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
	})

	succIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	// Default correct unlock params
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.PutSignatureUnlock(predIdx)

	// Allow test to modify constraint or unlock params
	if modifier != nil {
		modifier(txb, predIdx, &succIdx)
	}

	ts := chainIn.ID.Timestamp().AddTicks(int(ledger.L(chainIn.ID.Slot()).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	return txb.Bytes(), txb
}

// --------------------------------------------------------------------------
// TEST: Chain origin creation and chain ID derivation
// --------------------------------------------------------------------------

// TestChainOriginCreation verifies that a chain origin output is correctly created
// and the chain ID is derived as blake2b(originOutputID).
func TestChainOriginCreation(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)

	// The chain constraint in the origin output should have NilChainID
	cc := chainOut.Output.ChainConstraint()
	require.NotNil(t, cc, "origin output must have chain constraint")
	require.True(t, cc.IsOrigin(), "chain constraint must be origin")
	require.EqualValues(t, 0xff, cc.PredecessorInputIndex)

	// Chain ID should be the first ChainIDLength (24) bytes of blake2b(outputID)
	expectedChainID := base.MakeOriginChainID(chainOut.ID)
	require.EqualValues(t, expectedChainID, chainOut.ChainID,
		"chain ID must be blake2b hash of origin output ID")

	// Chain output must be retrievable from state by chain ID
	stateOut := e.getChainOutput(t, chainOut.ChainID)
	require.EqualValues(t, chainOut.ID, stateOut.ID,
		"state must map chain ID to the origin output")

	// Origin slot must match
	require.EqualValues(t, chainOut.ID.Slot(), cc.OriginSlot)

	t.Logf("chain origin created: chainID=%s, outputID=%s, slot=%d",
		chainOut.ChainID.StringShort(), chainOut.ID.StringShort(), cc.OriginSlot)
}

// --------------------------------------------------------------------------
// TEST: Valid chain transition (origin → successor)
// --------------------------------------------------------------------------

// TestChainValidTransition verifies a complete chain transition from origin to successor.
// The successor output must preserve chain ID, origin slot, and origin amount.
// Inflation = 0 for this test.
func TestChainValidTransition(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainID := chainOut.ChainID

	chainIn := e.getChainOutput(t, chainID)
	txBytes, _ := e.buildChainTransition(t, chainIn, chainOut, nil)

	err := e.u.AddTransaction(txBytes)
	require.NoError(t, err, "valid chain transition must succeed")

	// Chain output must still exist in state with the same chain ID
	newChainOut := e.getChainOutput(t, chainID)
	require.NotEqual(t, chainIn.ID, newChainOut.ID,
		"successor output must have a different output ID")

	// Verify the successor chain constraint
	cc := newChainOut.Output.ChainConstraint()
	require.NotNil(t, cc)
	require.False(t, cc.IsOrigin(), "successor must not be an origin")
	require.EqualValues(t, chainID, cc.ChainID)
	require.EqualValues(t, chainOut.OriginSlot, cc.OriginSlot, "origin slot must be preserved")

	t.Logf("chain transition succeeded: %s -> %s", chainIn.ID.StringShort(), newChainOut.ID.StringShort())
}

// --------------------------------------------------------------------------
// TEST: Multi-step chain transition (origin → succ1 → succ2)
// --------------------------------------------------------------------------

// TestChainMultiStepTransition verifies that chains can be transitioned multiple times.
// Each step must preserve origin slot and origin amount. Inflation = 0.
func TestChainMultiStepTransition(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainID := chainOut.ChainID

	// Step 1: origin → successor1
	chainIn1 := e.getChainOutput(t, chainID)
	txBytes1, _ := e.buildChainTransition(t, chainIn1, chainOut, nil)
	err := e.u.AddTransaction(txBytes1)
	require.NoError(t, err, "first chain transition must succeed")

	// Step 2: successor1 → successor2
	chainIn2 := e.getChainOutput(t, chainID)
	require.NotEqual(t, chainIn1.ID, chainIn2.ID)

	// For the second transition, chainData still carries the same ChainID
	txBytes2, _ := e.buildChainTransition(t, chainIn2, chainOut, nil)
	err = e.u.AddTransaction(txBytes2)
	require.NoError(t, err, "second chain transition must succeed")

	// Verify final state
	finalOut := e.getChainOutput(t, chainID)
	cc := finalOut.Output.ChainConstraint()
	require.NotNil(t, cc)
	require.EqualValues(t, chainID, cc.ChainID)
	require.EqualValues(t, chainOut.OriginSlot, cc.OriginSlot, "origin slot must survive 2 transitions")

	t.Logf("multi-step chain: %s -> %s -> %s",
		chainIn1.ID.StringShort(), chainIn2.ID.StringShort(), finalOut.ID.StringShort())
}

// --------------------------------------------------------------------------
// TEST: Chain termination
// --------------------------------------------------------------------------

// TestChainTermination verifies that a chain can be discontinued by setting
// unlock params to 0xFFFF. After termination, the chain ID must no longer
// exist in state. Inflation = 0.
func TestChainTermination(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainID := chainOut.ChainID

	chainIn := e.getChainOutput(t, chainID)
	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)

	// Build a transaction that terminates the chain:
	// consume the chain output, produce a non-chain output, set chain unlock to empty (discontinue)
	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// Produce output without chain constraint (same amount, same lock)
	nonChainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(chainIn.Output.TokenBalance())).WithLock(chainIn.Output.Lock())
	})
	_, err = txb.ProduceOutput(nonChainOut)
	require.NoError(t, err)

	// Set chain unlock params to empty (discontinue)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)
	txb.PutSignatureUnlock(predIdx)

	ts := chainIn.ID.Timestamp().AddTicks(int(ledger.L(chainIn.ID.Slot()).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	txBytes := txb.Bytes()
	err = e.u.AddTransaction(txBytes)
	require.NoError(t, err, "chain termination must succeed")

	// Chain ID must no longer exist in state
	_, err = e.u.StateReader().GetUTXOForChainID(chainID)
	require.Error(t, err, "terminated chain must not be in state")

	t.Logf("chain %s successfully terminated", chainID.StringShort())
}

// --------------------------------------------------------------------------
// TEST: Invalid predecessor reference in successor
// --------------------------------------------------------------------------

// TestChainInvalidPredecessorReference verifies that chain transitions with wrong
// predecessor input index or constraint index are rejected. Inflation = 0.
func TestChainInvalidPredecessorReference(t *testing.T) {
	// Set predecessor input index to 0xFF in the successor constraint
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainIn := e.getChainOutput(t, chainOut.ChainID)

	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// Wrong: predecessor input index = 0xFF
	wrongCC := ledger.NewChainConstraint(chainOut.ChainID, 0xff, cc.OriginSlot, 0, 0, 1, 0)
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainSucc)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.PutSignatureUnlock(predIdx)

	ts := chainIn.ID.Timestamp().AddTicks(int(ledger.L(chainIn.ID.Slot()).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err)
	t.Logf("wrong predecessor input index rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Origin slot immutability
// --------------------------------------------------------------------------

// TestChainOriginSlotImmutability verifies that changing the origin slot in a successor
// chain constraint is rejected. The origin slot must remain constant throughout the
// chain's lifetime. Inflation = 0.
func TestChainOriginSlotImmutability(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainIn := e.getChainOutput(t, chainOut.ChainID)

	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// Tamper: origin slot + 1
	wrongCC := ledger.NewChainConstraint(chainOut.ChainID, predIdx, cc.OriginSlot+1, 0, 0, 1, 0)
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainSucc)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.PutSignatureUnlock(predIdx)

	ts := chainIn.ID.Timestamp().AddTicks(int(ledger.L(chainIn.ID.Slot()).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "changing origin slot must be rejected")
	require.NoError(t, util.MustErrorWith(err, "origin slot mismatch"))
	t.Logf("origin slot mismatch enforced: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Transition counter immutability
// --------------------------------------------------------------------------

// TestChainTransitionCounterWrong verifies that providing a wrong transition counter
// in a successor chain constraint is rejected. The transition counter must be exactly
// predecessor's counter + 1. Inflation = 0.
func TestChainTransitionCounterWrong(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainIn := e.getChainOutput(t, chainOut.ChainID)

	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// Tamper: wrong transition counter (should be 1 for first transition from origin, use 5)
	wrongCC := ledger.NewChainConstraint(chainOut.ChainID, predIdx, cc.OriginSlot, 0, 0, 5, 0)
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainSucc)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.PutSignatureUnlock(predIdx)

	ts := chainIn.ID.Timestamp().AddTicks(int(ledger.L(chainIn.ID.Slot()).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "wrong transition counter must be rejected")
	t.Logf("wrong transition counter rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Chain ID mismatch between consumed and successor
// --------------------------------------------------------------------------

// TestChainIDMismatch verifies that a successor with a different chain ID than
// the consumed chain output is rejected. Inflation = 0.
func TestChainIDMismatch(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainIn := e.getChainOutput(t, chainOut.ChainID)

	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// Create a fake chain ID (different from the real one), truncated to ChainIDLength
	var fakeChainID base.ChainID
	fakeHash := blake2b.Sum256([]byte("fake chain ID"))
	copy(fakeChainID[:], fakeHash[:])
	wrongCC := ledger.NewChainConstraint(fakeChainID, predIdx, cc.OriginSlot, 0, 0, 1, 0)
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainSucc)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.PutSignatureUnlock(predIdx)

	ts := chainIn.ID.Timestamp().AddTicks(int(ledger.L(chainIn.ID.Slot()).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "chain ID mismatch must be rejected")
	require.NoError(t, util.MustErrorWith(err, "chain ID mismatch with successor"))
	t.Logf("chain ID mismatch rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Invalid unlock params on consumed chain output
// --------------------------------------------------------------------------

// TestChainInvalidUnlockParams verifies that wrong unlock params on a consumed chain
// output are rejected. Tests several variations. Inflation = 0.
func TestChainInvalidUnlockParams(t *testing.T) {
	t.Run("successor_output_index_out_of_range", func(t *testing.T) {
		// Unlock params point to a non-existent successor output
		e := newChainTestEnv(t, 1_000_000_000)
		chainOut := e.createChainOrigin(t, 200_000_000)
		chainIn := e.getChainOutput(t, chainOut.ChainID)

		cc := chainIn.Output.ChainConstraint()
		require.NotNil(t, cc)

		txb := exhelp.New()
		predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
		require.NoError(t, err)

		nextCC := ledger.NewChainConstraint(chainOut.ChainID, predIdx, cc.OriginSlot, 0, 0, 1, 0)
		chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
			out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
		})
		_, err = txb.ProduceOutput(chainSucc)
		require.NoError(t, err)

		// Wrong: unlock params point to output 0xFF (doesn't exist)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, []byte{0xff})
		txb.PutSignatureUnlock(predIdx)

		ts := chainIn.ID.Timestamp().AddTicks(int(ledger.L(chainIn.ID.Slot()).TransactionPace))
		txb.SetTimestamp(ts)
		txb.ComputeInputCommitment()
		txb.SignED25519(e.privKey)

		_, _, _, err = txbtest.BuildAndValidate(txb)
		require.Error(t, err)
		t.Logf("successor output index out of range rejected: %v", err)
	})

	t.Run("discontinue_with_orphaned_successor", func(t *testing.T) {
		// Empty unlock params (discontinuation) but a chain successor is produced.
		// The consumed constraint accepts discontinuation, but the produced successor's
		// crosscheck fails because no consumed input's unlock params point to it.
		e := newChainTestEnv(t, 1_000_000_000)
		chainOut := e.createChainOrigin(t, 200_000_000)
		chainIn := e.getChainOutput(t, chainOut.ChainID)

		cc := chainIn.Output.ChainConstraint()
		require.NotNil(t, cc)

		txb := exhelp.New()
		predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
		require.NoError(t, err)

		nextCC := ledger.NewChainConstraint(chainOut.ChainID, predIdx, cc.OriginSlot, 0, 0, 1, 0)
		chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
			out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
		})
		_, err = txb.ProduceOutput(chainSucc)
		require.NoError(t, err)

		// Empty bytes = discontinuation — consumed chain is happy,
		// but produced successor fails crosscheck
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)
		txb.PutSignatureUnlock(predIdx)

		ts := chainIn.ID.Timestamp().AddTicks(int(ledger.L(chainIn.ID.Slot()).TransactionPace))
		txb.SetTimestamp(ts)
		txb.ComputeInputCommitment()
		txb.SignED25519(e.privKey)

		_, _, _, err = txbtest.BuildAndValidate(txb)
		require.Error(t, err, "orphaned successor with discontinuation unlock must be rejected")
		require.NoError(t, util.MustErrorWith(err, "predecessor reference crosscheck failed"))
		t.Logf("orphaned successor correctly rejected: %v", err)
	})
}

// --------------------------------------------------------------------------
// TEST: Chain transition after first transition (non-origin consumed)
// --------------------------------------------------------------------------

// TestChainTransitionFromNonOrigin verifies that chain transitions work correctly
// when the consumed output is a non-origin successor (i.e., chain ID matching
// uses direct comparison rather than blake2b(inputID)). Inflation = 0.
func TestChainTransitionFromNonOrigin(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainID := chainOut.ChainID

	// First transition: origin → successor1
	chainIn1 := e.getChainOutput(t, chainID)
	txBytes1, _ := e.buildChainTransition(t, chainIn1, chainOut, nil)
	err := e.u.AddTransaction(txBytes1)
	require.NoError(t, err)

	// Verify successor1 is non-origin
	chainIn2 := e.getChainOutput(t, chainID)
	cc2 := chainIn2.Output.ChainConstraint()
	require.False(t, cc2.IsOrigin(), "successor must be non-origin")
	require.EqualValues(t, chainID, cc2.ChainID, "chain ID must match")

	// Second transition: successor1 → successor2
	// This tests the non-origin branch of _validChainConsumed:
	// equal($0, _chainSuccessorParam(0)) — direct chain ID comparison
	txBytes2, _ := e.buildChainTransition(t, chainIn2, chainOut, nil)
	err = e.u.AddTransaction(txBytes2)
	require.NoError(t, err, "transition from non-origin consumed must succeed")

	finalOut := e.getChainOutput(t, chainID)
	ccFinal := finalOut.Output.ChainConstraint()
	require.EqualValues(t, chainID, ccFinal.ChainID)
	require.EqualValues(t, chainOut.OriginSlot, ccFinal.OriginSlot)

	t.Logf("non-origin transition succeeded: origin -> succ1 -> succ2")
}

// ==========================================================================
// ChainLock tests
// ==========================================================================
//
// ChainLock (short name "c") is a lock type that restricts output unlocking
// to whoever controls a specific chain. To spend a chain-locked output,
// the holder must consume the chain output in the same transaction and
// reference it in the unlock params.
//
// All tests assume inflation = 0.

// --------------------------------------------------------------------------
// TEST: Valid ChainLock unlock
// --------------------------------------------------------------------------

// TestChainLockValidUnlock verifies that a chain-locked output can be spent
// by the chain controller in the same transaction that transitions the chain.
// Flow: create chain → send tokens to ChainLock(chainID) → spend them via chain transition.
func TestChainLockValidUnlock(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)

	// Create chain origin
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainID := chainOut.ChainID
	chainAddr := ledger.ChainLockFromChainID(chainID)

	// Create a second address to send tokens to the chain lock
	privKey2, _, addr2 := e.u.GenerateAddress(2)
	err := e.u.TokensFromFaucet(addr2, 200_000_000)
	require.NoError(t, err)

	// Send tokens from addr2 to chainAddr (chain-locked output)
	outs2 := getSourceOutputs(t, e.u, addr2)
	ts := outs2[0].ID.Timestamp().AddTicks(int(ledger.L(outs2[0].ID.Slot()).TransactionPace))
	par, err := e.u.MakeTransferInputData(privKey2, nil, ts)
	require.NoError(t, err)
	err = e.u.DoTransfer(par.WithAmount(50_000_000).WithTargetLock(chainAddr))
	require.NoError(t, err)

	require.EqualValues(t, 50_000_000, e.u.Balance(chainAddr))

	// Now spend the chain-locked output by transitioning the chain.
	// Get the chain output and the chain-locked output.
	chainIn := e.getChainOutput(t, chainID)
	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)

	// Get the chain-locked outputs
	lockedOutsData, err := e.u.StateReader().GetUTXOsForController(chainAddr.ControllerID())
	require.NoError(t, err)
	lockedOuts, err := ledger.ParseAndSortOutputData(lockedOutsData, nil)
	require.NoError(t, err)
	require.True(t, len(lockedOuts) > 0, "must have chain-locked outputs")

	// Build transaction: consume chain output + chain-locked output,
	// produce chain successor + target output
	txb := exhelp.New()

	// Input 0: chain output
	chainIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// Input 1: chain-locked output
	lockedIdx, err := txb.ConsumeOutput(lockedOuts[0].Output, lockedOuts[0].ID)
	require.NoError(t, err)

	// Output 0: chain successor
	nextCC := ledger.NewChainConstraint(chainID, chainIdx,
		cc.OriginSlot, 0, 0, 1, 0)
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainSucc)
	require.NoError(t, err)

	// Output 1: spend the chain-locked tokens to addr (chain controller)
	spentOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(lockedOuts[0].Output.TokenBalance())).WithLock(e.addr)
	})
	_, err = txb.ProduceOutput(spentOut)
	require.NoError(t, err)

	// Unlock chain output: signature + chain transition params
	txb.PutSignatureUnlock(chainIdx)
	txb.PutUnlockParams(chainIdx, ledger.ConstraintIndexChain,
		ledger.NewChainUnlockParams(succIdx))

	// Unlock chain-locked output: reference the chain input
	txb.PutUnlockParams(lockedIdx, ledger.ConstraintIndexLock,
		ledger.NewChainLockUnlockParams(chainIdx))

	maxTs := chainIn.ID.Timestamp()
	if lockedOuts[0].ID.Timestamp().After(maxTs) {
		maxTs = lockedOuts[0].ID.Timestamp()
	}
	txb.SetTimestamp(maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	txBytes := txb.Bytes()
	err = e.u.AddTransaction(txBytes)
	require.NoError(t, err, "valid chain-lock unlock must succeed")

	// Chain-locked balance should be zero now
	require.EqualValues(t, 0, e.u.Balance(chainAddr))

	t.Logf("chain-locked output successfully unlocked via chain transition")
}

// --------------------------------------------------------------------------
// TEST: ChainLock wrong chain ID
// --------------------------------------------------------------------------

// TestChainLockWrongChainID verifies that a chain-locked output cannot be unlocked
// by referencing a different chain than the one specified in the lock.
// Creates two chains (A, B), locks output to chain A, tries to unlock via chain B.
func TestChainLockWrongChainID(t *testing.T) {
	e := newChainTestEnv(t, 2_000_000_000)

	// Create chain A
	chainOutA := e.createChainOrigin(t, 200_000_000)
	chainIDA := chainOutA.ChainID

	// Create chain B (need more tokens from faucet first)
	chainOutB := e.createChainOrigin(t, 200_000_000)
	chainIDB := chainOutB.ChainID
	require.NotEqual(t, chainIDA, chainIDB, "chains must have different IDs")

	chainAddrA := ledger.ChainLockFromChainID(chainIDA)

	// Create a second address and send tokens to ChainLock(chainA)
	privKey2, _, addr2 := e.u.GenerateAddress(2)
	err := e.u.TokensFromFaucet(addr2, 200_000_000)
	require.NoError(t, err)

	outs2 := getSourceOutputs(t, e.u, addr2)
	ts := outs2[0].ID.Timestamp().AddTicks(int(ledger.L(outs2[0].ID.Slot()).TransactionPace))
	par, err := e.u.MakeTransferInputData(privKey2, nil, ts)
	require.NoError(t, err)
	err = e.u.DoTransfer(par.WithAmount(50_000_000).WithTargetLock(chainAddrA))
	require.NoError(t, err)

	// Get chain B output and chain-locked output
	chainInB := e.getChainOutput(t, chainIDB)
	ccB := chainInB.Output.ChainConstraint()
	require.NotNil(t, ccB)

	lockedOutsData, err := e.u.StateReader().GetUTXOsForController(chainAddrA.ControllerID())
	require.NoError(t, err)
	lockedOuts, err := ledger.ParseAndSortOutputData(lockedOutsData, nil)
	require.NoError(t, err)
	require.True(t, len(lockedOuts) > 0)

	// Build transaction: try to unlock chain-locked-to-A output via chain B
	txb := exhelp.New()

	// Input 0: chain B output (wrong chain)
	chainIdx, err := txb.ConsumeOutput(chainInB.Output, chainInB.ID)
	require.NoError(t, err)

	// Input 1: chain-locked output (locked to chain A)
	lockedIdx, err := txb.ConsumeOutput(lockedOuts[0].Output, lockedOuts[0].ID)
	require.NoError(t, err)

	// Output 0: chain B successor
	nextCCB := ledger.NewChainConstraint(chainIDB, chainIdx,
		ccB.OriginSlot, 0, 0, 1, 0)
	chainSuccB := chainInB.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(nextCCB.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainSuccB)
	require.NoError(t, err)

	// Output 1: try to spend chain-locked tokens
	spentOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(lockedOuts[0].Output.TokenBalance())).WithLock(e.addr)
	})
	_, err = txb.ProduceOutput(spentOut)
	require.NoError(t, err)

	// Unlock chain B: signature + chain transition
	txb.PutSignatureUnlock(chainIdx)
	txb.PutUnlockParams(chainIdx, ledger.ConstraintIndexChain,
		ledger.NewChainUnlockParams(succIdx))

	// Unlock chain-locked output: reference chain B (WRONG — should be chain A)
	txb.PutUnlockParams(lockedIdx, ledger.ConstraintIndexLock,
		ledger.NewChainLockUnlockParams(chainIdx))

	maxTs := chainInB.ID.Timestamp()
	if lockedOuts[0].ID.Timestamp().After(maxTs) {
		maxTs = lockedOuts[0].ID.Timestamp()
	}
	txb.SetTimestamp(maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "unlocking with wrong chain must be rejected")
	// The chainLock constraint on the consumed chain-locked output fails because
	// chain B's chain ID doesn't match the lock's chain ID (chain A)
	t.Logf("wrong chain ID correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: ChainLock self-referencing prevention
// --------------------------------------------------------------------------

// TestChainLockSelfReference verifies that a chain-locked output cannot reference
// itself in the unlock params. The EasyFL rule is:
//
//	not(equal(selfOutputIndex, byte(selfUnlockParameters, 0)))
//
// This prevents an output from claiming it unlocks itself.
func TestChainLockSelfReference(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)

	// Create chain origin
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainID := chainOut.ChainID
	chainAddr := ledger.ChainLockFromChainID(chainID)

	// Send tokens to the chain lock
	privKey2, _, addr2 := e.u.GenerateAddress(2)
	err := e.u.TokensFromFaucet(addr2, 200_000_000)
	require.NoError(t, err)

	outs2 := getSourceOutputs(t, e.u, addr2)
	ts := outs2[0].ID.Timestamp().AddTicks(int(ledger.L(outs2[0].ID.Slot()).TransactionPace))
	par, err := e.u.MakeTransferInputData(privKey2, nil, ts)
	require.NoError(t, err)
	err = e.u.DoTransfer(par.WithAmount(50_000_000).WithTargetLock(chainAddr))
	require.NoError(t, err)

	// Get chain output and chain-locked output
	chainIn := e.getChainOutput(t, chainID)
	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)

	lockedOutsData, err := e.u.StateReader().GetUTXOsForController(chainAddr.ControllerID())
	require.NoError(t, err)
	lockedOuts, err := ledger.ParseAndSortOutputData(lockedOutsData, nil)
	require.NoError(t, err)
	require.True(t, len(lockedOuts) > 0)

	// Build transaction where the chain-locked output tries to reference itself
	txb := exhelp.New()

	// Input 0: chain output
	chainIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// Input 1: chain-locked output
	lockedIdx, err := txb.ConsumeOutput(lockedOuts[0].Output, lockedOuts[0].ID)
	require.NoError(t, err)

	// Output 0: chain successor
	nextCC := ledger.NewChainConstraint(chainID, chainIdx,
		cc.OriginSlot, 0, 0, 1, 0)
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainSucc)
	require.NoError(t, err)

	// Output 1: spend the chain-locked tokens
	spentOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(lockedOuts[0].Output.TokenBalance())).WithLock(e.addr)
	})
	_, err = txb.ProduceOutput(spentOut)
	require.NoError(t, err)

	// Unlock chain output normally
	txb.PutSignatureUnlock(chainIdx)
	txb.PutUnlockParams(chainIdx, ledger.ConstraintIndexChain,
		ledger.NewChainUnlockParams(succIdx))

	// ATTACK: chain-locked output references itself (lockedIdx, not chainIdx)
	// This means byte 0 of unlock params equals selfOutputIndex of the chain-locked input
	txb.PutUnlockParams(lockedIdx, ledger.ConstraintIndexLock,
		ledger.NewChainLockUnlockParams(lockedIdx))

	maxTs := chainIn.ID.Timestamp()
	if lockedOuts[0].ID.Timestamp().After(maxTs) {
		maxTs = lockedOuts[0].ID.Timestamp()
	}
	txb.SetTimestamp(maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "self-referencing chain-lock must be rejected")
	t.Logf("self-referencing correctly rejected: %v", err)
}
