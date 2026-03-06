// Tests for the chain constraint $3/$4/$5 enforcement:
// - $3: cumulative chain inflation (z64, 0x at origin)
// - $4: cumulative branch inflation bonus (z64, 0x at origin)
// - $5: transition counter (z32, 0x at origin)
//
// These tests verify that the produced-side enforcement rules correctly validate
// cumulative inflation, branch bonus, and transition counter values.
// All tests use non-branch, same-slot transactions where inflation = 0,
// unless noted otherwise.
//
// See claude/chain_constraint2.md for the refactoring spec.

package tests

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// TEST: Origin must have $3/$4/$5/$6 = 0x (empty bytes)
// --------------------------------------------------------------------------

// TestChainOriginCumulativesMustBeEmpty verifies that a chain origin with non-zero
// cumulative fields ($3/$4/$5/$6) is rejected. The EasyFL rule checks that these
// arguments equal 0x (empty bytes) at origin.
func TestChainOriginCumulativesMustBeEmpty(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	// Manually construct a chain "origin" source with non-empty $3/$4/$5.
	// Uses NilChainID and empty predecessor (0x) to trigger the origin path
	// in EasyFL, but with z64/z32 encoded values instead of 0x.
	nilChainIDHex := hex.EncodeToString(base.NilChainID[:])

	t.Run("non_zero_cumulative_inflation", func(t *testing.T) {
		// $3 = z64/100 instead of 0x
		src := fmt.Sprintf("chain(0x%s, 0x, z32/%d, z64/100, 0x, 0x, 0x)", nilChainIDHex, ts.Slot)
		_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(src)
		require.NoError(t, err)

		par, err := e.u.MakeTransferInputData(e.privKey, nil, ts)
		require.NoError(t, err)
		err = e.u.DoTransfer(par.WithAmount(200_000_000).WithTargetLock(e.addr).WithConstraintBinary(code))
		require.Error(t, err, "origin with non-empty $3 must be rejected")
		require.NoError(t, util.MustErrorWith(err, "invalid chain origin data"))
		t.Logf("origin with non-empty $3 rejected: %v", err)
	})

	t.Run("non_zero_branch_bonus", func(t *testing.T) {
		// $4 = z64/50 instead of 0x
		src := fmt.Sprintf("chain(0x%s, 0x, z32/%d, 0x, z64/50, 0x, 0x)", nilChainIDHex, ts.Slot)
		_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(src)
		require.NoError(t, err)

		par, err := e.u.MakeTransferInputData(e.privKey, nil, ts)
		require.NoError(t, err)
		err = e.u.DoTransfer(par.WithAmount(200_000_000).WithTargetLock(e.addr).WithConstraintBinary(code))
		require.Error(t, err, "origin with non-empty $4 must be rejected")
		require.NoError(t, util.MustErrorWith(err, "invalid chain origin data"))
		t.Logf("origin with non-empty $4 rejected: %v", err)
	})

	t.Run("non_zero_transition_counter", func(t *testing.T) {
		// $5 = z32/1 instead of 0x
		src := fmt.Sprintf("chain(0x%s, 0x, z32/%d, 0x, 0x, z64/1, 0x)", nilChainIDHex, ts.Slot)
		_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(src)
		require.NoError(t, err)

		par, err := e.u.MakeTransferInputData(e.privKey, nil, ts)
		require.NoError(t, err)
		err = e.u.DoTransfer(par.WithAmount(200_000_000).WithTargetLock(e.addr).WithConstraintBinary(code))
		require.Error(t, err, "origin with non-empty $5 must be rejected")
		require.NoError(t, util.MustErrorWith(err, "invalid chain origin data"))
		t.Logf("origin with non-empty $5 rejected: %v", err)
	})

	t.Run("all_non_zero", func(t *testing.T) {
		// All three non-empty
		src := fmt.Sprintf("chain(0x%s, 0x, z32/%d, z64/100, z64/50, z64/1, z32/1)", nilChainIDHex, ts.Slot)
		_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(src)
		require.NoError(t, err)

		par, err := e.u.MakeTransferInputData(e.privKey, nil, ts)
		require.NoError(t, err)
		err = e.u.DoTransfer(par.WithAmount(200_000_000).WithTargetLock(e.addr).WithConstraintBinary(code))
		require.Error(t, err, "origin with all non-empty $3/$4/$5 must be rejected")
		require.NoError(t, util.MustErrorWith(err, "invalid chain origin data"))
		t.Logf("origin with all non-empty $3/$4/$5 rejected: %v", err)
	})

	t.Run("zero_valued_z_encoded_is_empty", func(t *testing.T) {
		// z64/0 encodes to empty bytes in EasyFL (zero-compression), same as 0x.
		// So chain(... z64/0, z64/0, z32/0) is a valid origin — it IS empty at bytecode level.
		src := fmt.Sprintf("chain(0x%s, 0x, z32/%d, z64/0, z64/0, z64/0, z32/0)", nilChainIDHex, ts.Slot)
		_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(src)
		require.NoError(t, err)

		par, err := e.u.MakeTransferInputData(e.privKey, nil, ts)
		require.NoError(t, err)
		err = e.u.DoTransfer(par.WithAmount(200_000_000).WithTargetLock(e.addr).WithConstraintBinary(code))
		require.NoError(t, err, "z64/0 encodes to 0x (empty), so this is a valid origin")
		t.Logf("z64/0 = 0x at bytecode level, valid origin accepted")
	})
}

// --------------------------------------------------------------------------
// TEST: Transition counter must increment by exactly 1
// --------------------------------------------------------------------------

// TestChainTransitionCounterIncrement verifies that the transition counter
// increments by exactly 1 on each chain transition. Tests counter=0 (wrong),
// counter=2 (wrong), and counter=1 (correct first transition from origin).
func TestChainTransitionCounterIncrement(t *testing.T) {
	t.Run("counter_zero_rejected", func(t *testing.T) {
		// First transition from origin (counter=0) → successor must have counter=1, not 0
		e := newChainTestEnv(t, 1_000_000_000)
		chainOut := e.createChainOrigin(t, 200_000_000)
		chainIn := e.getChainOutput(t, chainOut.ChainID)

		_, txb := e.buildChainTransition(t, chainIn, chainOut,
			func(txb *txbuilder.TxBuilder, predIdx byte, succIdx *byte) {
				// Replace successor with counter=0 (should be 1)
				wrongCC := ledger.NewChainConstraint(
					chainOut.ChainID, predIdx, chainIn.Output.ChainConstraint().OriginSlot,
					0, 0, 0, 0, // counter=0 is wrong
				)
				chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
					out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
				})
				txb.TransactionData.Outputs[*succIdx] = chainSucc
			},
		)
		_, _, _, err := txb.BytesWithValidation()
		require.Error(t, err, "counter=0 on first transition must be rejected")
		require.NoError(t, util.MustErrorWith(err, "wrong transition counter"))
		t.Logf("counter=0 rejected: %v", err)
	})

	t.Run("counter_skip_rejected", func(t *testing.T) {
		// First transition from origin → successor must have counter=1, not 2
		e := newChainTestEnv(t, 1_000_000_000)
		chainOut := e.createChainOrigin(t, 200_000_000)
		chainIn := e.getChainOutput(t, chainOut.ChainID)

		_, txb := e.buildChainTransition(t, chainIn, chainOut,
			func(txb *txbuilder.TxBuilder, predIdx byte, succIdx *byte) {
				wrongCC := ledger.NewChainConstraint(
					chainOut.ChainID, predIdx, chainIn.Output.ChainConstraint().OriginSlot,
					0, 0, 2, 0, // counter=2 is wrong (skips 1)
				)
				chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
					out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
				})
				txb.TransactionData.Outputs[*succIdx] = chainSucc
			},
		)
		_, _, _, err := txb.BytesWithValidation()
		require.Error(t, err, "counter=2 on first transition must be rejected")
		require.NoError(t, util.MustErrorWith(err, "wrong transition counter"))
		t.Logf("counter=2 (skip) rejected: %v", err)
	})

	t.Run("counter_correct_first_transition", func(t *testing.T) {
		// First transition: counter=1 (correct)
		e := newChainTestEnv(t, 1_000_000_000)
		chainOut := e.createChainOrigin(t, 200_000_000)
		chainIn := e.getChainOutput(t, chainOut.ChainID)

		txBytes, _ := e.buildChainTransition(t, chainIn, chainOut, nil)
		err := e.u.AddTransaction(txBytes)
		require.NoError(t, err, "counter=1 on first transition must succeed")

		// Verify counter value in state
		newOut := e.getChainOutput(t, chainOut.ChainID)
		cc := newOut.Output.ChainConstraint()
		require.EqualValues(t, 1, cc.TransitionCounter)
		t.Logf("counter=1 accepted, verified in state")
	})
}

// --------------------------------------------------------------------------
// TEST: Multi-step transition counter tracking
// --------------------------------------------------------------------------

// TestChainTransitionCounterMultiStep verifies that the transition counter
// correctly tracks through multiple chain transitions: 0 → 1 → 2 → 3.
func TestChainTransitionCounterMultiStep(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainID := chainOut.ChainID

	// Verify origin counter = 0
	chainIn := e.getChainOutput(t, chainID)
	cc := chainIn.Output.ChainConstraint()
	require.EqualValues(t, 0, cc.TransitionCounter, "origin counter must be 0")

	// Transition 1: counter 0 → 1
	txBytes, _ := e.buildChainTransition(t, chainIn, chainOut, nil)
	err := e.u.AddTransaction(txBytes)
	require.NoError(t, err)

	chainIn = e.getChainOutput(t, chainID)
	cc = chainIn.Output.ChainConstraint()
	require.EqualValues(t, 1, cc.TransitionCounter, "counter must be 1 after first transition")

	// Transition 2: counter 1 → 2
	txBytes, _ = e.buildChainTransition(t, chainIn, chainOut, nil)
	err = e.u.AddTransaction(txBytes)
	require.NoError(t, err)

	chainIn = e.getChainOutput(t, chainID)
	cc = chainIn.Output.ChainConstraint()
	require.EqualValues(t, 2, cc.TransitionCounter, "counter must be 2 after second transition")

	// Transition 3: counter 2 → 3
	txBytes, _ = e.buildChainTransition(t, chainIn, chainOut, nil)
	err = e.u.AddTransaction(txBytes)
	require.NoError(t, err)

	chainIn = e.getChainOutput(t, chainID)
	cc = chainIn.Output.ChainConstraint()
	require.EqualValues(t, 3, cc.TransitionCounter, "counter must be 3 after third transition")

	t.Logf("multi-step counter: 0 → 1 → 2 → 3 verified")
}

// --------------------------------------------------------------------------
// TEST: Wrong cumulative chain inflation is rejected
// --------------------------------------------------------------------------

// TestChainWrongCumulativeInflation verifies that a successor with incorrect
// cumulative chain inflation ($3) is rejected. For non-branch same-slot
// transactions, inflation is 0, so cumulative inflation must equal predecessor's.
func TestChainWrongCumulativeInflation(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainIn := e.getChainOutput(t, chainOut.ChainID)

	t.Run("inflation_nonzero_on_same_slot_non_branch", func(t *testing.T) {
		// Non-branch, same-slot tx: inflation should be 0.
		// Setting $3 = 1000 (wrong) should be rejected.
		_, txb := e.buildChainTransition(t, chainIn, chainOut,
			func(txb *txbuilder.TxBuilder, predIdx byte, succIdx *byte) {
				wrongCC := ledger.NewChainConstraint(
					chainOut.ChainID, predIdx, chainIn.Output.ChainConstraint().OriginSlot,
					1000, 0, 1, 0, // cumulative inflation = 1000 is wrong
				)
				chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
					out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
				})
				txb.TransactionData.Outputs[*succIdx] = chainSucc
			},
		)
		_, _, _, err := txb.BytesWithValidation()
		require.Error(t, err, "wrong cumulative chain inflation must be rejected")
		require.NoError(t, util.MustErrorWith(err, "wrong cumulative chain inflation"))
		t.Logf("wrong cumulative chain inflation rejected: %v", err)
	})

	t.Run("inflation_large_value", func(t *testing.T) {
		// Try a very large inflation value
		_, txb := e.buildChainTransition(t, chainIn, chainOut,
			func(txb *txbuilder.TxBuilder, predIdx byte, succIdx *byte) {
				wrongCC := ledger.NewChainConstraint(
					chainOut.ChainID, predIdx, chainIn.Output.ChainConstraint().OriginSlot,
					999_999_999, 0, 1, 0,
				)
				chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
					out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
				})
				txb.TransactionData.Outputs[*succIdx] = chainSucc
			},
		)
		_, _, _, err := txb.BytesWithValidation()
		require.Error(t, err, "large bogus cumulative inflation must be rejected")
		require.NoError(t, util.MustErrorWith(err, "wrong cumulative chain inflation"))
		t.Logf("large bogus inflation rejected: %v", err)
	})
}

// --------------------------------------------------------------------------
// TEST: Wrong cumulative branch bonus is rejected
// --------------------------------------------------------------------------

// TestChainWrongCumulativeBranchBonus verifies that a successor with incorrect
// cumulative branch bonus ($4) is rejected. For non-branch transactions,
// branch bonus must remain unchanged from predecessor.
func TestChainWrongCumulativeBranchBonus(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainIn := e.getChainOutput(t, chainOut.ChainID)

	t.Run("bonus_nonzero_on_non_branch", func(t *testing.T) {
		// Non-branch tx: branch bonus should remain 0.
		// Setting $4 = 500 (wrong) should be rejected.
		_, txb := e.buildChainTransition(t, chainIn, chainOut,
			func(txb *txbuilder.TxBuilder, predIdx byte, succIdx *byte) {
				wrongCC := ledger.NewChainConstraint(
					chainOut.ChainID, predIdx, chainIn.Output.ChainConstraint().OriginSlot,
					0, 500, 1, 0, // branch bonus = 500 is wrong on non-branch
				)
				chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
					out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
				})
				txb.TransactionData.Outputs[*succIdx] = chainSucc
			},
		)
		_, _, _, err := txb.BytesWithValidation()
		require.Error(t, err, "wrong cumulative branch bonus must be rejected")
		require.NoError(t, util.MustErrorWith(err, "wrong cumulative branch bonus"))
		t.Logf("wrong branch bonus rejected: %v", err)
	})

	t.Run("bonus_large_value", func(t *testing.T) {
		_, txb := e.buildChainTransition(t, chainIn, chainOut,
			func(txb *txbuilder.TxBuilder, predIdx byte, succIdx *byte) {
				wrongCC := ledger.NewChainConstraint(
					chainOut.ChainID, predIdx, chainIn.Output.ChainConstraint().OriginSlot,
					0, 999_999_999, 1, 0,
				)
				chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
					out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
				})
				txb.TransactionData.Outputs[*succIdx] = chainSucc
			},
		)
		_, _, _, err := txb.BytesWithValidation()
		require.Error(t, err, "large bogus branch bonus must be rejected")
		require.NoError(t, util.MustErrorWith(err, "wrong cumulative branch bonus"))
		t.Logf("large bogus branch bonus rejected: %v", err)
	})
}

// --------------------------------------------------------------------------
// TEST: Correct zero cumulatives accepted on non-branch same-slot transition
// --------------------------------------------------------------------------

// TestChainCorrectZeroCumulatives verifies that a chain transition with zero
// cumulative inflation and zero branch bonus is accepted for non-branch
// same-slot transactions (where inflation = 0).
func TestChainCorrectZeroCumulatives(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainIn := e.getChainOutput(t, chainOut.ChainID)

	// buildChainTransition defaults to (0, 0, counter+1) which is correct
	txBytes, _ := e.buildChainTransition(t, chainIn, chainOut, nil)
	err := e.u.AddTransaction(txBytes)
	require.NoError(t, err, "zero cumulatives on non-branch same-slot must succeed")

	// Verify all fields in state
	newOut := e.getChainOutput(t, chainOut.ChainID)
	cc := newOut.Output.ChainConstraint()
	require.NotNil(t, cc)
	require.EqualValues(t, chainOut.ChainID, cc.ChainID)
	require.EqualValues(t, chainOut.OriginSlot, cc.OriginSlot)
	require.EqualValues(t, 0, cc.CumulativeChainInflation, "cumulative inflation must be 0")
	require.EqualValues(t, 0, cc.CumulativeBranchBonus, "branch bonus must be 0")
	require.EqualValues(t, 1, cc.TransitionCounter, "counter must be 1")

	t.Logf("correct zero cumulatives accepted, all fields verified")
}

// --------------------------------------------------------------------------
// TEST: Combined wrong $3/$4/$5 — multiple fields wrong simultaneously
// --------------------------------------------------------------------------

// TestChainMultipleWrongCumulatives verifies that a transition with multiple
// wrong cumulative fields is rejected. Tests that the enforcement catches
// combinations of errors.
func TestChainMultipleWrongCumulatives(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainIn := e.getChainOutput(t, chainOut.ChainID)

	t.Run("all_three_wrong", func(t *testing.T) {
		// $3=100, $4=50, $5=5 — all wrong
		_, txb := e.buildChainTransition(t, chainIn, chainOut,
			func(txb *txbuilder.TxBuilder, predIdx byte, succIdx *byte) {
				wrongCC := ledger.NewChainConstraint(
					chainOut.ChainID, predIdx, chainIn.Output.ChainConstraint().OriginSlot,
					100, 50, 5, 0,
				)
				chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
					out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
				})
				txb.TransactionData.Outputs[*succIdx] = chainSucc
			},
		)
		_, _, _, err := txb.BytesWithValidation()
		require.Error(t, err, "all three wrong cumulatives must be rejected")
		// Should fail on whichever check runs first (chain inflation)
		t.Logf("all three wrong rejected: %v", err)
	})

	t.Run("inflation_and_bonus_wrong_counter_correct", func(t *testing.T) {
		// $3=100, $4=50, $5=1 — counter correct but inflation/bonus wrong
		_, txb := e.buildChainTransition(t, chainIn, chainOut,
			func(txb *txbuilder.TxBuilder, predIdx byte, succIdx *byte) {
				wrongCC := ledger.NewChainConstraint(
					chainOut.ChainID, predIdx, chainIn.Output.ChainConstraint().OriginSlot,
					100, 50, 1, 0,
				)
				chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
					out.PutConstraint(wrongCC.Bytes(), ledger.ConstraintIndexChain)
				})
				txb.TransactionData.Outputs[*succIdx] = chainSucc
			},
		)
		_, _, _, err := txb.BytesWithValidation()
		require.Error(t, err, "wrong inflation+bonus with correct counter must be rejected")
		require.NoError(t, util.MustErrorWith(err, "wrong cumulative chain inflation"))
		t.Logf("inflation+bonus wrong rejected: %v", err)
	})
}

// --------------------------------------------------------------------------
// TEST: Cumulatives preserved through multi-step transitions
// --------------------------------------------------------------------------

// TestChainCumulativesMultiStep verifies that cumulative fields are correctly
// preserved through multiple chain transitions. Since all transitions are
// non-branch same-slot (inflation=0), cumulatives should remain 0 and counter
// should increment by 1 each step.
func TestChainCumulativesMultiStep(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainID := chainOut.ChainID

	for step := 1; step <= 4; step++ {
		chainIn := e.getChainOutput(t, chainID)
		txBytes, _ := e.buildChainTransition(t, chainIn, chainOut, nil)
		err := e.u.AddTransaction(txBytes)
		require.NoError(t, err, "transition step %d must succeed", step)

		newOut := e.getChainOutput(t, chainID)
		cc := newOut.Output.ChainConstraint()
		require.EqualValues(t, 0, cc.CumulativeChainInflation,
			"step %d: cumulative inflation must remain 0", step)
		require.EqualValues(t, 0, cc.CumulativeBranchBonus,
			"step %d: branch bonus must remain 0", step)
		require.EqualValues(t, step, int(cc.TransitionCounter),
			"step %d: counter must equal step number", step)
	}
	t.Logf("4-step chain: all cumulatives correct through transitions")
}

// --------------------------------------------------------------------------
// TEST: ChainConstraint round-trip serialization for new fields
// --------------------------------------------------------------------------

// TestChainConstraintSerializationRoundTrip verifies that the ChainConstraint
// Go struct correctly serializes and deserializes the new $3/$4/$5 fields
// for both origin and transition constraints.
func TestChainConstraintSerializationRoundTrip(t *testing.T) {
	t.Run("origin_round_trip", func(t *testing.T) {
		orig := ledger.NewChainOrigin(42)
		back, err := ledger.ChainConstraintFromBytes(orig.Bytes())
		require.NoError(t, err)
		require.True(t, back.IsOrigin())
		require.EqualValues(t, 42, back.OriginSlot)
		require.EqualValues(t, 0, back.CumulativeChainInflation)
		require.EqualValues(t, 0, back.CumulativeBranchBonus)
		require.EqualValues(t, 0, back.TransitionCounter)
		t.Logf("origin round-trip OK")
	})

	t.Run("transition_round_trip", func(t *testing.T) {
		chainID := base.RandomChainID()
		cc := ledger.NewChainConstraint(chainID, 0, 100, 500_000, 100_000, 42, 0)
		back, err := ledger.ChainConstraintFromBytes(cc.Bytes())
		require.NoError(t, err)
		require.False(t, back.IsOrigin())
		require.EqualValues(t, chainID, back.ChainID)
		require.EqualValues(t, 0, back.PredecessorInputIndex)
		require.EqualValues(t, 100, back.OriginSlot)
		require.EqualValues(t, 500_000, back.CumulativeChainInflation)
		require.EqualValues(t, 100_000, back.CumulativeBranchBonus)
		require.EqualValues(t, 42, back.TransitionCounter)
		t.Logf("transition round-trip OK: inflation=%d, bonus=%d, counter=%d",
			back.CumulativeChainInflation, back.CumulativeBranchBonus, back.TransitionCounter)
	})

	t.Run("large_values_round_trip", func(t *testing.T) {
		chainID := base.RandomChainID()
		cc := ledger.NewChainConstraint(chainID, 3, 999_999,
			18_446_744_073_709_551_000, // near max uint64
			9_223_372_036_854_775_000,  // large uint64
			4_294_967_290,              // large transition counter (z64)
			999_999,                    // branch counter (z32)
		)
		back, err := ledger.ChainConstraintFromBytes(cc.Bytes())
		require.NoError(t, err)
		require.EqualValues(t, uint64(18_446_744_073_709_551_000), back.CumulativeChainInflation)
		require.EqualValues(t, uint64(9_223_372_036_854_775_000), back.CumulativeBranchBonus)
		require.EqualValues(t, uint64(4_294_967_290), back.TransitionCounter)
		require.EqualValues(t, uint32(999_999), back.BranchCounter)
		t.Logf("large values round-trip OK")
	})

	t.Run("zero_cumulatives_transition_round_trip", func(t *testing.T) {
		// Transition with all-zero cumulatives (first transition from origin, non-branch)
		chainID := base.RandomChainID()
		cc := ledger.NewChainConstraint(chainID, 0, 50, 0, 0, 1, 0)
		back, err := ledger.ChainConstraintFromBytes(cc.Bytes())
		require.NoError(t, err)
		require.False(t, back.IsOrigin())
		require.EqualValues(t, 0, back.CumulativeChainInflation)
		require.EqualValues(t, 0, back.CumulativeBranchBonus)
		require.EqualValues(t, 1, back.TransitionCounter)
		t.Logf("zero cumulatives transition round-trip OK")
	})
}
