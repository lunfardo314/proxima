// Tests for the per-milestone coverageDelta within-slot strict-increase rule
// (def/sequencer.easyfl _enforceCoverageAdvance). coverageDelta now lives on the
// sequencer constraint of every milestone; a same-slot, non-branch chain
// predecessor forces the successor's coverageDelta to strictly exceed the
// predecessor's (anti-spam: a same-slot milestone must consolidate real
// coverage). These tests validate the EasyFL rule directly via utxodb
// settlement (the milestone attacher's computed-vs-declared cross-check does not
// run under utxodb, so we drive arbitrary declared coverageDelta values here).

package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// addSeqSuccessorWithCoverage builds a same-slot sequencer chain successor
// carrying an explicit coverageDelta on its sequencer constraint (epochSlots /
// maxFrozenEpochs are re-emitted from the predecessor so the immutability check
// passes) and settles it against utxodb. Returns the AddTransaction error so the
// caller can assert acceptance / rejection of the within-slot strict-increase
// rule.
func (e *sequencerTestEnv) addSeqSuccessorWithCoverage(
	t *testing.T,
	chainIn *ledger.OutputWithID,
	chainID base.ChainID,
	succTs base.LedgerTime,
	coverageDelta uint64,
) error {
	t.Helper()

	cc := chainIn.Output.ChainConstraint()
	require.NotNil(t, cc)
	predSeq, idx := chainIn.Output.SequencerConstraint()
	require.NotEqual(t, byte(0xff), idx, "predecessor must be a sequencer chain")
	succSeq := ledger.NewSequencerConstraint(predSeq.EpochSlots, predSeq.MaxFrozenEpochs, coverageDelta)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIn.Output, chainIn.ID)
	require.NoError(t, err)

	nextCC := ledger.NewChainConstraint(chainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0)
	chainSucc := chainIn.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(nextCC.Bytes(), ledger.ConstraintIndexChain)
		out.PutConstraint(succSeq.Bytes(), ledger.SequencerConstraintFixedIndex)
	})
	succIdx, err := txb.ProduceOutput(chainSucc)
	require.NoError(t, err)

	txb.PutSignatureUnlock(predIdx)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	// same-slot non-branch milestone; the predecessor (origin) is itself a
	// sequencer tx, so no endorsement is required for the structural same-slot check.
	txb.SetSequencerData(succIdx, txbuildercore.SequencerOutputIndexNone)
	txb.SetTimestamp(succTs)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	return e.u.AddTransaction(txb.Bytes())
}

// TestCoverageDeltaMonotonicity verifies that within a slot a sequencer
// milestone's coverageDelta must STRICTLY increase over its (non-branch)
// chain predecessor's. The bootstrap sequencer origin carries coverageDelta 0,
// so a same-slot successor with coverageDelta 0 is rejected (not strictly
// greater) while coverageDelta 1 is accepted.
func TestCoverageDeltaMonotonicity(t *testing.T) {
	t.Run("same-slot equal rejected", func(t *testing.T) {
		e := newSequencerTestEnv(t, 10_000_000_000)
		originTs := base.T(getSourceOutputs(t, e.u, e.addr)[0].ID.Slot()+1, 20)
		chainIn, chainID := e.settleSequencerOrigin(t, originTs)

		succTs := chainIn.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		// origin coverageDelta == 0; declaring 0 again is not strictly greater
		err := e.addSeqSuccessorWithCoverage(t, chainIn, chainID, succTs, 0)
		require.Error(t, err)
		require.NoError(t, util.MustErrorWith(err, "coverage delta must strictly increase"))
	})

	t.Run("same-slot strictly greater accepted", func(t *testing.T) {
		e := newSequencerTestEnv(t, 10_000_000_000)
		originTs := base.T(getSourceOutputs(t, e.u, e.addr)[0].ID.Slot()+1, 20)
		chainIn, chainID := e.settleSequencerOrigin(t, originTs)

		succTs := chainIn.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		// coverageDelta 1 > origin's 0 — accepted
		err := e.addSeqSuccessorWithCoverage(t, chainIn, chainID, succTs, 1)
		require.NoError(t, err)
	})
}
