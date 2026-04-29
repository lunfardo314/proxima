// Sequencer transaction specific validation tests for Proxima ledger.
// These tests verify rules that apply exclusively to sequencer transactions:
//   - Minimum amount on sequencer output (>= 1B tokens)
//   - Post-branch consolidation ticks (non-branch sequencer tick >= 12)
//   - Pre-branch consolidation ticks (multi-input sequencer blocked in last 25 ticks)
//   - Slot boundary restricted to branch transactions
//   - Sequencer input pace constraint (2 ticks, tighter than non-sequencer's 12)
//   - Same-slot predecessor must be sequencer or tx must have endorsements
//   - Cross-slot predecessor requires endorsements, branch, or explicit baseline
//
// All tests assume inflation = 0.
// Endorsement-specific rules are tested in endorsement_test.go.
// Chain constraint rules are tested in chain_test.go.

package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// Helpers for sequencer tests
// --------------------------------------------------------------------------

type sequencerTestEnv struct {
	u       *utxodb.UTXODB
	privKey ed25519.PrivateKey
	addr    ledger.SigLock
}

func newSequencerTestEnv(t *testing.T, fundAmount uint64) *sequencerTestEnv {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	err := u.TokensFromFaucet(addr, fundAmount)
	require.NoError(t, err)
	return &sequencerTestEnv{u: u, privKey: privKey, addr: addr}
}

// buildSequencerOrigin builds a sequencer chain origin transaction at the given
// timestamp with the full funded amount. Includes a dummy endorsement (required
// for sequencer chain origins). Returns the raw bytes.
func (e *sequencerTestEnv) buildSequencerOrigin(
	t *testing.T,
	originTs base.LedgerTime,
) []byte {
	t.Helper()

	outs := getSourceOutputs(t, e.u, e.addr)

	txb := txbuilder.New()
	total, _, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			require.NoError(t, err)
		}
	}

	chainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total)).WithLock(e.addr)
		o.MustPushConstraint(ledger.NewChainOrigin(originTs.Slot).Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint().Bytes())
	})
	originIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	txb.TransactionData.SequencerOutputIndex = originIdx
	txb.TransactionData.Timestamp = originTs

	// Dummy endorsement required for sequencer chain origins
	dummyEnd := base.NewTransactionID(originTs.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyEnd)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.privKey)

	return txb.TransactionData.Bytes()
}

// settleSequencerOrigin builds and settles a sequencer chain origin, returning
// the chain output from state and derived chain ID.
func (e *sequencerTestEnv) settleSequencerOrigin(
	t *testing.T,
	originTs base.LedgerTime,
) (*ledger.OutputWithID, base.ChainID) {
	t.Helper()

	originBytes := e.buildSequencerOrigin(t, originTs)
	err := e.u.AddTransaction(originBytes)
	require.NoError(t, err)

	originTx, err := transaction.Parse(originBytes)
	require.NoError(t, err)
	originOutputID, err := base.NewOutputID(originTx.ID(), 0)
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(originOutputID)

	chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	chainIn, err := chs.Parse()
	require.NoError(t, err)

	return chainIn, chainID
}

// buildSequencerSuccessor builds a sequencer chain successor consuming the given
// chain output. Returns raw bytes and builder.
func (e *sequencerTestEnv) buildSequencerSuccessor(
	t *testing.T,
	chainIn *ledger.OutputWithID,
	chainID base.ChainID,
	succTs base.LedgerTime,
	endorsements []base.TransactionID,
) ([]byte, *txbuilder.TxBuilder) {
	t.Helper()

	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)

	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	nextCC := ledger.NewChainConstraint(chainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0)
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainSucc)
	require.NoError(t, err)

	txb.PutSignatureUnlock(predIdx)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain,
		ledger.NewChainUnlockParams(succIdx))
	txb.TransactionData.SequencerOutputIndex = succIdx

	txb.PushEndorsements(endorsements...)

	txb.TransactionData.Timestamp = succTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.privKey)

	return txb.TransactionData.Bytes(), txb
}

// --------------------------------------------------------------------------
// TEST: Post-branch consolidation ticks
// --------------------------------------------------------------------------

// TestSequencerPostBranchConsolidation verifies that a non-branch sequencer
// transaction at tick < PostBranchConsolidationTicks (12) is rejected.
// The zeroTickOnBranchOnly check fires first for tick 0, but for ticks 1-11
// the post-branch consolidation check catches them.
func TestSequencerPostBranchConsolidation(t *testing.T) {
	e := newSequencerTestEnv(t, 10_000_000_000)

	// Tick 5 is below PostBranchConsolidationTicks (12) and above 0 (not slot boundary)
	originTs := base.T(getSourceOutputs(t, e.u, e.addr)[0].ID.Slot()+1, 5)
	originBytes := e.buildSequencerOrigin(t, originTs)

	err := e.u.AddTransaction(originBytes)
	require.Error(t, err, "sequencer tx at tick 5 must violate post-branch consolidation")
	require.NoError(t, util.MustErrorWith(err, "sequencer transaction violates post branch consolidation ticks constraint"))
	t.Logf("correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Pre-branch consolidation ticks
// --------------------------------------------------------------------------

// TestSequencerPreBranchConsolidation verifies that multi-input sequencer
// transactions are blocked in the last 25 ticks of a slot (ticks 103-127).
// Single-input sequencer transactions at the same tick are allowed.
func TestSequencerPreBranchConsolidation(t *testing.T) {
	// Fund with enough for chain origin + change output
	e := newSequencerTestEnv(t, 20_000_000_000)

	outs := getSourceOutputs(t, e.u, e.addr)
	originTs := base.T(outs[0].ID.Slot()+1, 20)

	// Build chain origin that produces chain output (10B) + change output (10B)
	txb1 := txbuilder.New()
	total, _, err := txb1.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb1.PutSignatureUnlock(0)

	chainAmount := uint64(10_000_000_000)
	chainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(chainAmount)).WithLock(e.addr)
		o.MustPushConstraint(ledger.NewChainOrigin(originTs.Slot).Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint().Bytes())
	})
	chainIdx, err := txb1.ProduceOutput(chainOut)
	require.NoError(t, err)

	changeAmount := total - chainAmount
	changeOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(changeAmount)).WithLock(e.addr)
	})
	_, err = txb1.ProduceOutput(changeOut)
	require.NoError(t, err)

	txb1.TransactionData.SequencerOutputIndex = chainIdx
	txb1.TransactionData.Timestamp = originTs

	dummyEnd := base.NewTransactionID(originTs.AddTicks(-5), base.TransactionIDShort{}, true)
	txb1.PushEndorsements(dummyEnd)

	txb1.TransactionData.InputCommitment = ledger.HashOutputs(txb1.ConsumedOutputs...)
	txb1.SignED25519(e.privKey)

	err = e.u.AddTransaction(txb1.TransactionData.Bytes())
	require.NoError(t, err)

	// Get chain output via chain ID
	originTx, err := transaction.Parse(txb1.TransactionData.Bytes())
	require.NoError(t, err)
	originOutputID, err := base.NewOutputID(originTx.ID(), chainIdx)
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(originOutputID)
	chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	chainIn, err := chs.Parse()
	require.NoError(t, err)

	// Find the change output (all outputs for address, excluding chain output)
	allOuts := getSourceOutputs(t, e.u, e.addr)
	var changeIn *ledger.OutputWithID
	for _, o := range allOuts {
		if o.ID != chainIn.ID {
			changeIn = o
			break
		}
	}
	require.NotNil(t, changeIn, "change output must be in state")

	t.Run("multi_input_rejected", func(t *testing.T) {
		// Successor consumes chain + change (2 inputs) at tick 110 (> 102)
		// Pre-branch consolidation: numInputs > 1 AND tick > MaxTick - PreBranchTicks
		// 110 > 127 - 25 = 102 → FAIL
		succTs := base.T(chainIn.ID.Slot(), 110)

		cc := chainIn.Output.ChainConstraint()
		txb := txbuilder.New()
		predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
		require.NoError(t, err)
		_, err = txb.ConsumeOutput(changeIn.Output, changeIn.ID)
		require.NoError(t, err)

		// Chain successor with both amounts consolidated
		nextCC := ledger.NewChainConstraint(chainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0)
		chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
			out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
			out.WithAmounts(int64(chainAmount + changeAmount))
		})
		succIdx, err := txb.ProduceOutput(chainSucc)
		require.NoError(t, err)

		txb.PutSignatureUnlock(predIdx)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain,
			ledger.NewChainUnlockParams(succIdx))
		err = txb.PutUnlockReference(1, ledger.ConstraintIndexLock, 0)
		require.NoError(t, err)
		txb.TransactionData.SequencerOutputIndex = succIdx
		txb.TransactionData.Timestamp = succTs
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(e.privKey)

		err = e.u.AddTransaction(txb.TransactionData.Bytes())
		require.Error(t, err, "2-input sequencer tx at tick 110 must violate pre-branch consolidation")
		require.NoError(t, util.MustErrorWith(err, "sequencer transaction violates pre-branch consolidation ticks constraint"))
		t.Logf("multi-input at tick 110 correctly rejected: %v", err)
	})

	t.Run("single_input_accepted", func(t *testing.T) {
		// Successor consumes only chain output (1 input) at tick 110
		// Pre-branch consolidation skipped when numInputs == 1
		succTs := base.T(chainIn.ID.Slot(), 110)

		cc := chainIn.Output.ChainConstraint()
		txb := txbuilder.New()
		predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
		require.NoError(t, err)

		nextCC := ledger.NewChainConstraint(chainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0)
		chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
			out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
		})
		succIdx, err := txb.ProduceOutput(chainSucc)
		require.NoError(t, err)

		txb.PutSignatureUnlock(predIdx)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain,
			ledger.NewChainUnlockParams(succIdx))
		txb.TransactionData.SequencerOutputIndex = succIdx
		txb.TransactionData.Timestamp = succTs
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(e.privKey)

		err = e.u.AddTransaction(txb.TransactionData.Bytes())
		require.NoError(t, err, "1-input sequencer tx at tick 110 must pass (pre-branch skipped)")
		t.Logf("single-input at tick 110 accepted")
	})
}

// --------------------------------------------------------------------------
// TEST: Slot boundary restricted to branch transactions
// --------------------------------------------------------------------------

// TestSequencerSlotBoundaryNonBranch verifies that a sequencer transaction at
// tick 0 (slot boundary) is rejected if it is not a branch transaction.
// The zeroTickOnBranchOnly check fires in the sequencer constraint.
func TestSequencerSlotBoundaryNonBranch(t *testing.T) {
	e := newSequencerTestEnv(t, 10_000_000_000)

	// Settle a chain origin first (at tick 20, safe zone)
	outs := getSourceOutputs(t, e.u, e.addr)
	originTs := base.T(outs[0].ID.Slot()+1, 20)
	chainIn, chainID := e.settleSequencerOrigin(t, originTs)

	// Build successor at tick 0 (slot boundary) in the next slot — NOT a branch
	succTs := base.T(chainIn.ID.Slot()+1, 0)

	// At tick 0, the parser sees a sequencer tx on slot boundary and assumes it's a
	// branch transaction. It then tries to find the stem output at StemOutputIndex (0xff).
	// Since no stem output exists, parsing fails before the EasyFL zeroTickOnBranchOnly
	// check is reached. This is defense in depth — Go parsing catches the missing stem,
	// while EasyFL would catch the tick/branch mismatch if parsing somehow passed.
	txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs, nil)

	err := e.u.AddTransaction(txBytes)
	require.Error(t, err, "non-branch sequencer tx at tick 0 must be rejected")
	require.NoError(t, util.MustErrorWith(err, "ParseSequencerData stem"))
	t.Logf("correctly rejected at parse stage: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Sequencer input pace constraint
// --------------------------------------------------------------------------

// TestSequencerInputPace verifies the sequencer-specific input pace constraint
// (TransactionPaceSequencer = 2 ticks). Inputs must be at least 2 ticks before
// the transaction timestamp. This is enforced in scanInputs() at parse stage.
func TestSequencerInputPace(t *testing.T) {
	e := newSequencerTestEnv(t, 10_000_000_000)

	// Settle chain origin at tick 20
	outs := getSourceOutputs(t, e.u, e.addr)
	originTs := base.T(outs[0].ID.Slot()+1, 20)
	chainIn, chainID := e.settleSequencerOrigin(t, originTs)

	t.Run("one_tick_gap_rejected", func(t *testing.T) {
		// Successor at tick 21 — gap = 1 tick < TransactionPaceSequencer (2)
		succTs := base.T(chainIn.ID.Slot(), chainIn.ID.Timestamp().Tick+1)
		txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs, nil)

		_, err := transaction.ParseWithPartialValidation(txBytes)
		require.Error(t, err, "1-tick gap must violate sequencer input pace")
		require.NoError(t, util.MustErrorWith(err, "violates sequencer time pace constraint"))
		t.Logf("1-tick gap correctly rejected: %v", err)
	})

	t.Run("two_tick_gap_accepted", func(t *testing.T) {
		// Successor at tick 22 — gap = 2 ticks = TransactionPaceSequencer
		succTs := base.T(chainIn.ID.Slot(), chainIn.ID.Timestamp().Tick+2)
		txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs, nil)

		// ParseWithPartialValidation should pass (pace OK)
		_, err := transaction.ParseWithPartialValidation(txBytes)
		require.NoError(t, err, "2-tick gap must pass parse + partial validation")

		// Full validation should also pass (same-slot sequencer predecessor)
		err = e.u.AddTransaction(txBytes)
		require.NoError(t, err, "2-tick gap must pass full validation")
		t.Logf("2-tick gap accepted")
	})
}

// --------------------------------------------------------------------------
// TEST: Same-slot non-sequencer predecessor
// --------------------------------------------------------------------------

// TestSequencerSameSlotNonSeqPredecessor verifies that a sequencer transaction
// with a same-slot non-sequencer chain predecessor and no endorsements is rejected.
// The _sameSlotPredecessorCase requires either the predecessor to be a sequencer
// transaction or the successor to have endorsements.
func TestSequencerSameSlotNonSeqPredecessor(t *testing.T) {
	e := newSequencerTestEnv(t, 10_000_000_000)

	outs := getSourceOutputs(t, e.u, e.addr)

	// Create a non-sequencer chain origin at tick 15 in the next slot
	chainOriginTs := base.T(outs[0].ID.Slot()+1, 15)

	txb := txbuilder.New()
	total, _, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	// Chain origin WITHOUT sequencer constraint — output: [amount, sigLock, chain]
	chainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total)).WithLock(e.addr)
		o.MustPushConstraint(ledger.NewChainOrigin(chainOriginTs.Slot).Bytes())
	})
	_, err = txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	// NOT a sequencer tx — don't set SequencerOutputIndex
	txb.TransactionData.Timestamp = chainOriginTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(e.privKey)

	err = e.u.AddTransaction(txb.TransactionData.Bytes())
	require.NoError(t, err, "non-sequencer chain origin must settle")

	// Get chain output from state
	originTx, err := transaction.Parse(txb.TransactionData.Bytes())
	require.NoError(t, err)
	originOutputID, err := base.NewOutputID(originTx.ID(), 0)
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(originOutputID)

	chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	chainIn, err := chs.Parse()
	require.NoError(t, err)

	// Build successor WITH sequencer constraint at same slot, tick 17 (gap 2, pace OK)
	// The predecessor (non-sequencer) is same-slot, and no endorsements → must fail
	succTs := base.T(chainIn.ID.Slot(), 17)

	cc := chainIn.Output.ChainConstraint()

	txb2 := txbuilder.New()
	predIdx, err := txb2.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	nextCC := ledger.NewChainConstraint(chainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0)
	// Clone chain output and ADD sequencer constraint
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
		out.MustPushConstraint(ledger.NewSequencerConstraint().Bytes())
	})
	succIdx, err := txb2.ProduceOutput(chainSucc)
	require.NoError(t, err)

	txb2.PutSignatureUnlock(predIdx)
	txb2.PutUnlockParams(predIdx, ledger.ConstraintIndexChain,
		ledger.NewChainUnlockParams(succIdx))
	txb2.TransactionData.SequencerOutputIndex = succIdx
	// No endorsements — this should trigger the same-slot predecessor rejection
	txb2.TransactionData.Timestamp = succTs
	txb2.TransactionData.InputCommitment = ledger.HashOutputs(txb2.ConsumedOutputs...)
	txb2.SignED25519(e.privKey)

	err = e.u.AddTransaction(txb2.TransactionData.Bytes())
	require.Error(t, err, "same-slot non-sequencer predecessor without endorsements must be rejected")
	require.NoError(t, util.MustErrorWith(err,
		"sequencer chain predecessor on the same slot must be either a sequencer tx too or endorse another sequencer tx"))
	t.Logf("correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Cross-slot predecessor without endorsements
// --------------------------------------------------------------------------

// TestSequencerCrossSlotNoEndorsements verifies that a sequencer transaction with
// a cross-slot chain predecessor is rejected when it has no endorsements, is not
// a branch transaction, and has no explicit baseline.
// The _crossSlotPredecessorCase requires at least one of these three.
func TestSequencerCrossSlotNoEndorsements(t *testing.T) {
	e := newSequencerTestEnv(t, 10_000_000_000)

	// Settle chain origin at tick 20 in next slot
	outs := getSourceOutputs(t, e.u, e.addr)
	originTs := base.T(outs[0].ID.Slot()+1, 20)
	chainIn, chainID := e.settleSequencerOrigin(t, originTs)

	// Build successor in NEXT slot (cross-slot) with no endorsements
	succTs := base.T(chainIn.ID.Slot()+1, 50)
	txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs, nil)

	err := e.u.AddTransaction(txBytes)
	require.Error(t, err, "cross-slot successor without endorsements/branch/baseline must be rejected")
	require.NoError(t, util.MustErrorWith(err,
		"sequencer tx has incorrect cross slot chain predecessor or does not have any endorsements"))
	t.Logf("correctly rejected: %v", err)
}
