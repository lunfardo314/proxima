package task

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
)

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
	// (branch and non-branch). The on-chain rule enforces strict increase within
	// a slot; the attacher cross-checks this exact value against its own
	// past-cone computation.
	//
	// Gate mirrors _enforceCoverageAdvance in def/sequencer.easyfl: the
	// milestone's coverageDelta must STRICTLY exceed the effective predecessor
	// coverage — the same-slot non-branch predecessor's coverageDelta, else 0.
	// A milestone (incl. a branch or slot-first milestone) that consolidates no
	// new coverage is invalid; abort here rather than build a tx the ledger
	// rejects. effectivePred is 0 for cross-slot / branch predecessors (baseline
	// reset), so those just require coverageDelta > 0.
	if ledger.L(ts.Slot).EnforceCoverageDeltaMonotonicity {
		var effectivePred uint64
		chainPredTs := p.ChainInput().ID.Timestamp()
		if chainPredTs.Slot == ts.Slot && !chainPredTs.IsSlotBoundary() {
			if predSeq, idx := p.ChainInput().Output.SequencerConstraint(); idx != 0xff {
				effectivePred = predSeq.CoverageDelta
			}
		}
		if coverageDelta <= effectivePred {
			return nil, fmt.Errorf("finalize[%s]: coverageDelta %d does not strictly exceed effective predecessor coverage %d — milestone consolidates no new coverage",
				source, coverageDelta, effectivePred)
		}
	}
	p.SetCoverageDelta(coverageDelta)

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
