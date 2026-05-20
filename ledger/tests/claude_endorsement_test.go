// Endorsement validation tests for Proxima ledger.
// These tests verify that endorsement rules are properly enforced:
//   - Only sequencer transactions can contain endorsements
//   - Maximum 8 endorsements per transaction
//   - No duplicate endorsements
//   - No cross-slot endorsements (must be same slot)
//   - Sequencer pace constraint between endorsed and endorsing transaction
//
// All tests assume inflation = 0.
//
// Endorsement validation happens in two stages:
//   Go-side (scanEndorsements in parse.go): cross-slot rejection, pace constraint
//   EasyFL (tx_integrity_validator.easyfl): sequencer-only, max count, no duplicates

package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/txcore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// Helpers for endorsement tests
// --------------------------------------------------------------------------

// endorsementTestEnv holds state for endorsement tests.
type endorsementTestEnv struct {
	u       *utxodb.UTXODB
	privKey ed25519.PrivateKey
	addr    ledger.SigLock
}

func newEndorsementTestEnv(t *testing.T) *endorsementTestEnv {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey, _, addr := u.GenerateAddress(1)
	err := u.TokensFromFaucet(addr, 10_000_000_000)
	require.NoError(t, err)
	return &endorsementTestEnv{u: u, privKey: privKey, addr: addr}
}

// setupSequencerChain creates a chain origin with a sequencer constraint and settles
// it in the UTXODB. The sequencer chain origin requires at least one endorsement
// (EasyFL: "sequencer chain origin must endorse another sequencer transaction"),
// so a dummy endorsement is included.
// Returns the chain output from state, derived chain ID, and a valid successor
// timestamp. The successor is placed in the next slot at tick 20 with enough room
// for endorsement timing.
func (e *endorsementTestEnv) setupSequencerChain(t *testing.T) (
	chainIn *ledger.OutputWithID,
	chainID base.ChainID,
	succTs base.LedgerTime,
) {
	t.Helper()

	outs := getSourceOutputs(t, e.u, e.addr)

	// Place origin in next slot at tick 20, with room for a dummy endorsement
	// at tick 15 (5-tick gap > TransactionPaceSequencer in tests = 3)
	originTs := base.T(outs[0].ID.Slot()+1, 20)

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

	// Chain origin output with sequencer constraint:
	//   index 0: amount
	//   index 1: lock (sigLock)
	//   index 2: chain constraint (origin)
	//   index 3: sequencer constraint (pointing to chain at index 2)
	chainOriginOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total)).WithLock(e.addr)
		o.MustPushConstraint(ledger.NewChainOrigin(originTs.Slot).Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint().Bytes())
	})
	originIdx, err := txb.ProduceOutput(chainOriginOut)
	require.NoError(t, err)

	txb.SetSequencerData(originIdx, txcore.SequencerOutputIndexNone)
	txb.SetTimestamp(originTs)

	// Dummy endorsement required for sequencer chain origins
	dummyEnd := base.NewTransactionID(originTs.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyEnd)

	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	originBytes := txb.Bytes()
	err = e.u.AddTransaction(originBytes)
	require.NoError(t, err)

	// Derive chain ID = blake2b(originOutputID)
	originTx, err := transaction.Parse(originBytes)
	require.NoError(t, err)
	originOutputID, err := base.NewOutputID(originTx.ID(), originIdx)
	require.NoError(t, err)
	chainID = base.MakeOriginChainID(originOutputID)

	// Get chain output from state
	chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	chainIn, err = chs.Parse()
	require.NoError(t, err)

	// Successor in next slot at tick 50 — cross-slot from origin.
	// The sequencer's cross-slot predecessor case requires endorsements, branch,
	// or explicit baseline. The test endorsements satisfy this for positive tests;
	// for negative tests (cross-slot, pace), parse fails before the sequencer check.
	succTs = base.T(chainIn.ID.Slot()+1, 50)

	return
}

// buildSequencerSuccessor builds a sequencer chain successor transaction consuming
// the given chain output. The successor inherits the sequencer constraint from the
// chain origin via Clone. The caller provides the timestamp and endorsements.
// Returns the raw transaction bytes and the builder.
func (e *endorsementTestEnv) buildSequencerSuccessor(
	t *testing.T,
	chainIn *ledger.OutputWithID,
	chainID base.ChainID,
	succTs base.LedgerTime,
	endorsements []base.TransactionID,
) ([]byte, *txbuilder.TxBuilder) {
	t.Helper()

	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc, "output must have chain constraint")

	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	// Build successor with updated chain constraint; sequencer constraint is inherited via Clone
	nextCC := ledger.NewChainConstraint(chainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0)
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainSucc)
	require.NoError(t, err)

	txb.PutSignatureUnlock(predIdx)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain,
		ledger.NewChainUnlockParams(succIdx))
	txb.SetSequencerData(succIdx, txcore.SequencerOutputIndexNone)

	txb.PushEndorsements(endorsements...)

	txb.SetTimestamp(succTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	return txb.Bytes(), txb
}

// --------------------------------------------------------------------------
// TEST: Non-sequencer transaction with endorsements
// --------------------------------------------------------------------------

// TestEndorsementNonSequencerRejected verifies that a non-sequencer transaction
// cannot contain endorsements. Only sequencer transactions are allowed to endorse.
// Checked by EasyFL _validEndorsements in tx_integrity_validator.easyfl.
func TestEndorsementNonSequencerRejected(t *testing.T) {
	e := newEndorsementTestEnv(t)

	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddTicks(int(ledger.L(outs[0].ID.Slot()).TransactionPace))

	// Build a regular (non-sequencer) transfer with a dummy endorsement
	txb := txbuilder.New()
	total, _, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total)).WithLock(e.addr)
	})
	_, err = txb.ProduceOutput(out)
	require.NoError(t, err)

	// Add a dummy endorsement — should be rejected because tx is not a sequencer
	dummyEndorsement := base.NewTransactionID(ts.AddTicks(-2), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyEndorsement)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	txBytes := txb.Bytes()
	_, err = transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "non-sequencer transaction with endorsements must be rejected")
	require.NoError(t, util.MustErrorWith(err, "only sequencer transactions can endorse"))
	t.Logf("non-sequencer endorsement correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Cross-slot endorsement
// --------------------------------------------------------------------------

// TestEndorsementCrossSlotRejected verifies that endorsements referencing transactions
// in a different slot are rejected. This is enforced in scanEndorsements() at parse stage,
// before any EasyFL constraint evaluation.
func TestEndorsementCrossSlotRejected(t *testing.T) {
	e := newEndorsementTestEnv(t)

	chainIn, chainID, succTs := e.setupSequencerChain(t)

	// Endorsement from a DIFFERENT slot (successor slot - 1)
	crossSlotEnd := base.NewTransactionID(
		base.T(succTs.Slot-1, 10), base.TransactionIDShort{}, true,
	)

	txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs,
		[]base.TransactionID{crossSlotEnd})
	_, err := transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "cross-slot endorsement must be rejected")
	require.NoError(t, util.MustErrorWith(err, "cross-slot endorsements are not allowed"))
	t.Logf("cross-slot endorsement correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Endorsement monotonicity
// --------------------------------------------------------------------------

// TestEndorsementMonotonicityViolation verifies that an endorsement with the
// same timestamp as the endorsing transaction is rejected. Endorsements have
// no ledger pace constant — only strict monotonicity (≥1 tick).
// Enforced in scanEndorsements() at parse stage.
func TestEndorsementMonotonicityViolation(t *testing.T) {
	e := newEndorsementTestEnv(t)

	chainIn, chainID, succTs := e.setupSequencerChain(t)

	// Endorsement at the same timestamp as the endorsing tx — violates monotonicity
	sameTickEnd := base.NewTransactionID(succTs, base.TransactionIDShort{}, true)

	txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs,
		[]base.TransactionID{sameTickEnd})
	_, err := transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "same-tick endorsement must be rejected")
	require.NoError(t, util.MustErrorWith(err, "violates strict monotonicity"))
	t.Logf("same-tick endorsement correctly rejected: %v", err)
}

// TestEndorsementOneTickGapAccepted verifies that an endorsement exactly one
// tick before the endorsing tx is accepted — the lower bound of monotonicity.
// This case used to be rejected under the old ValidSequencerPace rule.
func TestEndorsementOneTickGapAccepted(t *testing.T) {
	e := newEndorsementTestEnv(t)

	chainIn, chainID, succTs := e.setupSequencerChain(t)

	// 1-tick gap satisfies strict monotonicity
	oneTickBack := base.NewTransactionID(
		succTs.AddTicks(-1), base.TransactionIDShort{}, true,
	)

	txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs,
		[]base.TransactionID{oneTickBack})
	_, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err, "1-tick endorsement gap must be accepted under monotonicity")
	t.Logf("1-tick endorsement gap correctly accepted")
}

// --------------------------------------------------------------------------
// TEST: Too many endorsements
// --------------------------------------------------------------------------

// TestEndorsementTooMany verifies that a transaction with more than 8 endorsements
// (constMaxNumberOfEndorsements) is rejected by EasyFL _validEndorsements check.
func TestEndorsementTooMany(t *testing.T) {
	e := newEndorsementTestEnv(t)

	chainIn, chainID, succTs := e.setupSequencerChain(t)

	// Create 9 distinct endorsements — all valid timing, but count exceeds max (8)
	var endorsements []base.TransactionID
	for i := 0; i < 9; i++ {
		hash := base.TransactionIDShort{}
		hash[0] = byte(i + 1)
		endorsements = append(endorsements, base.NewTransactionID(
			succTs.AddTicks(-10), hash, true,
		))
	}

	txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs, endorsements)
	_, err := transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "9 endorsements must be rejected (max 8)")
	require.NoError(t, util.MustErrorWith(err, "number of endorsements too big"))
	t.Logf("too many endorsements correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Duplicate endorsements
// --------------------------------------------------------------------------

// TestEndorsementDuplicateRejected verifies that duplicate endorsements
// (same transaction endorsed twice) are rejected by EasyFL _validEndorsements check.
func TestEndorsementDuplicateRejected(t *testing.T) {
	e := newEndorsementTestEnv(t)

	chainIn, chainID, succTs := e.setupSequencerChain(t)

	// Same endorsement ID twice
	end := base.NewTransactionID(
		succTs.AddTicks(-10), base.TransactionIDShort{1}, true,
	)

	txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs,
		[]base.TransactionID{end, end})
	_, err := transaction.ParseWithPartialValidation(txBytes)
	require.Error(t, err, "duplicate endorsements must be rejected")
	require.NoError(t, util.MustErrorWith(err, "duplicated endorsements not allowed"))
	t.Logf("duplicate endorsements correctly rejected: %v", err)
}

// --------------------------------------------------------------------------
// TEST: Valid single endorsement
// --------------------------------------------------------------------------

// TestEndorsementValidSingle verifies that a sequencer transaction with a single
// valid endorsement (same slot, valid pace) passes validation through parse and
// partial context stages.
func TestEndorsementValidSingle(t *testing.T) {
	e := newEndorsementTestEnv(t)

	chainIn, chainID, succTs := e.setupSequencerChain(t)

	// Valid endorsement: same slot, 10-tick gap > TransactionPaceSequencer (2)
	end := base.NewTransactionID(
		succTs.AddTicks(-10), base.TransactionIDShort{1}, true,
	)

	txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs,
		[]base.TransactionID{end})
	_, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err, "valid endorsement must pass partial validation")
	t.Logf("valid endorsement passed partial validation")
}

// --------------------------------------------------------------------------
// TEST: Maximum endorsements accepted
// --------------------------------------------------------------------------

// TestEndorsementMaxAccepted verifies that exactly 8 endorsements (the maximum
// constMaxNumberOfEndorsements) are accepted when all are valid.
func TestEndorsementMaxAccepted(t *testing.T) {
	e := newEndorsementTestEnv(t)

	chainIn, chainID, succTs := e.setupSequencerChain(t)

	// Create 8 distinct valid endorsements
	var endorsements []base.TransactionID
	for i := 0; i < 8; i++ {
		hash := base.TransactionIDShort{}
		hash[0] = byte(i + 1)
		endorsements = append(endorsements, base.NewTransactionID(
			succTs.AddTicks(-10), hash, true,
		))
	}

	txBytes, _ := e.buildSequencerSuccessor(t, chainIn, chainID, succTs, endorsements)
	_, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err, "8 endorsements (maximum) must be accepted")
	t.Logf("8 endorsements (maximum) accepted")
}
