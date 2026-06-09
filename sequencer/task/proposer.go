package task

import (
	"errors"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
)

// logFinalizeFailure logs a proposer's finalize() failure. A non-advancing
// milestone (coverageDelta does not strictly increase within the slot) is an
// expected "nothing new to consolidate this tick" condition wrapped in
// ErrNotGoodEnough — logged quietly via Tracef. Everything else is a real
// failure worth a WARN.
func (t *taskData) logFinalizeFailure(proposer string, err error) {
	if errors.Is(err, ErrNotGoodEnough) {
		t.Tracef(TraceTagBaseProposer, "%s: no coverage-advancing proposal: %v", proposer, err)
		return
	}
	t.Log().Warnf("%s: finalize failed: %v", proposer, err)
}

// finalize computes coverage, builds the transaction, and returns a finalProposal.
// The proposal's attacher is closed after building the transaction.
func (p *proposal) finalize(source string) (*finalProposal, error) {
	start := time.Now()
	ts := p.targetTs
	if p.effectiveTs != base.NilLedgerTime {
		ts = p.effectiveTs
	}

	if err := p.ctx.Err(); err != nil {
		p.Log().Warnf("finalize[%s]: FAIL_AT_ENTRY target=%s err=%v", source, p.targetTs.String(), err)
		return nil, err
	}
	covStart := time.Now()
	coverageDelta, err := p.CoverageDeltaWithContext(p.ctx)
	if err != nil {
		p.Log().Warnf("finalize[%s]: FAIL_AT_COVERAGE target=%s covElapsed=%v totalElapsed=%v err=%v",
			source, p.targetTs.String(), time.Since(covStart), time.Since(start), err)
		return nil, err
	}
	ledgerCoverage := p.FinalLedgerCoverage(ts, coverageDelta)
	slotInflation := p.SlotInflation()

	// coverageDelta is written onto the sequencer constraint of EVERY milestone
	// (branch and non-branch). The on-chain _enforceCoverageAdvance rule enforces
	// strict increase within a slot; the attacher cross-checks the declared value.
	//
	// Pre-makeTx gate mirroring _enforceCoverageAdvance: skip building a milestone
	// the ledger would reject (coverageDelta must STRICTLY exceed the effective
	// predecessor coverage — the same-slot non-branch predecessor's coverageDelta,
	// else 0). Without this gate makeTx builds + validates the tx and the sequencer
	// constraint rejects it with an alarming SCRIPT-FAIL trace, once per target
	// tick (same motivation as the branch-health gate below). The rejection is an
	// expected "nothing new to consolidate this tick" condition, not an error, so
	// it is wrapped in ErrNotGoodEnough and logged quietly by the proposers
	// (logFinalizeFailure).
	if ledger.L(ts.Slot).EnforceCoverageDeltaMonotonicity {
		var effectivePred uint64
		chainPredTs := p.ChainInput().ID.Timestamp()
		if chainPredTs.Slot == ts.Slot && !chainPredTs.IsSlotBoundary() {
			if predSeq, idx := p.ChainInput().Output.SequencerConstraint(); idx != 0xff {
				effectivePred = predSeq.CoverageDelta
			}
		}
		if coverageDelta <= effectivePred {
			// makeTx (not reached) is what normally closes the attacher; close it
			// here so the early return does not leak the incremental attacher.
			p.Close()
			return nil, fmt.Errorf("finalize[%s]: coverageDelta %d does not strictly exceed effective predecessor coverage %d: %w",
				source, coverageDelta, effectivePred, ErrNotGoodEnough)
		}
	}
	p.SetCoverageDelta(coverageDelta)

	// Refuse to build an unhealthy branch before paying for makeTx + validation.
	// Mirrors the stemLock health check (bootstrap chain exempt): a transiently
	// network-partitioned sequencer accumulates too little coverage delta, and
	// building the branch only to have stemLock reject it at make-tx wastes work
	// and emits an alarming FAIL_AT_MAKETX panic trace. The successor supply is
	// predStem.TotalSupply + slotInflation per the on-chain recurrence; the
	// branch's own inflation (added inside makeTx, ~1e7 vs ~1e15 supply) is
	// omitted here, keeping this gate marginally more lenient than the constraint
	// so it never skips a branch the ledger would have accepted.
	if p.IsBranchTarget() && p.SequencerID() != base.BoostrapSequencerID {
		supply := p.PredecessorStemTotalSupply() + slotInflation
		if !p.Library.IsHealthyCoverageDelta(coverageDelta, supply) {
			// makeTx (not reached) is what normally closes the attacher; close it
			// here so the early return does not leak the incremental attacher.
			p.Close()
			return nil, fmt.Errorf("finalize[%s]: branch unhealthy — coverageDelta %d below health threshold for supply %d (likely transient network partition / coverage starvation)",
				source, coverageDelta, supply)
		}
	}

	pastConeAttachmentCost := p.PastConeAttachmentCost()

	// For branch transactions, plumb the past-cone-aware aggregates into the
	// stem the builder is about to produce (Phase B of metadata-refactor).
	// Non-branch txs don't produce a stem, so this is a no-op for them.
	// TotalSupply / TotalCoverage are NOT passed — the txbuilder applies the
	// on-chain recurrence using the predecessor stem to derive both.
	// SlotInflation / NumConfirmedTransactions are PAST CONE only — buildStemLock
	// adds the branch tx's own inflation and +1 to match the attacher view.
	if p.IsBranchTarget() {
		// Predecessor branch's trie root (24 bytes). For pending baselines,
		// trigger the commit first so bd.Root is populated. The proposal
		// already pulls the baseline reader at construction time, but Get()
		// reads a snapshot of the cache and may race that init.
		var baselineRoot []byte
		if baselineID := p.BaselineBranch(); baselineID != nil {
			_ = p.Branches().GetStateReaderForTheBranch(*baselineID)
			if bd := p.Branches().Get(*baselineID); bd != nil && bd.Root != nil {
				baselineRoot = bd.Root.Bytes()
			}
		}

		// Seed the distinct-sequencer set with our own sequencer ID: the branch
		// tx (not yet in the past cone) is a sequencer tx of this sequencer, and
		// the verifying attacher's cone will include it. numSeq is therefore the
		// FINAL value; numSeqTransactions is the past-cone delta (+1 in builder).
		numTx, numSeqTx, numSeq := p.NumNewTransactionStatsInPastCone(p.SequencerID())
		p.SetStemAggregates(txbuilder_seq.StemAggregates{
			CoverageDelta:            coverageDelta,
			FrozenCoverageDelta:      p.SequencerFrozenCoverageDelta(),
			BaselineFrozenCoverage:   p.BaselineFrozenCoverage(),
			SlotInflation:            slotInflation,
			NumConfirmedTransactions: uint32(numTx),
			NumSeqTransactions:       uint32(numSeqTx),
			NumSeq:                   uint32(numSeq),
			BaselineRoot:             baselineRoot,
		})
	}

	mkStart := time.Now()
	tx, hrString, err := p.makeTx() // closes the attacher
	if err != nil {
		p.Log().Warnf("finalize[%s]: FAIL_AT_MAKETX target=%s mkElapsed=%v totalElapsed=%v err=%v",
			source, p.targetTs.String(), time.Since(mkStart), time.Since(start), err)
		return nil, err
	}

	slotInflation += tx.InflationAmount()

	// extract predecessor timestamp from the built transaction
	var predTs base.LedgerTime
	if seqData := tx.SequencerTransactionData(); seqData != nil {
		predOID := tx.MustInputAt(seqData.SequencerOutputData.ChainConstraint.PredecessorInputIndex)
		predTs = predOID.Timestamp()
	}

	return &finalProposal{
		tx:     tx,
		txSize: len(tx.Bytes()),
		txMetadata: &txmetadata.TransactionMetadata{
			SourceTypeNonPersistent: txmetadata.SourceTypeSequencer,
		},
		hrString:       hrString,
		coverageDelta:  coverageDelta,
		ledgerCoverage: ledgerCoverage,
		inflation:      tx.InflationAmount(),
		attacherName:   p.IncrementalAttacher.Name(),
		source:         source,
		predecessorTs:  predTs,
		attachmentCost: pastConeAttachmentCost,
	}, nil
}
