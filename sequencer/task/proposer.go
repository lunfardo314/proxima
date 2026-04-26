package task

import (
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
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
	coverageDelta, frozen, err := p.CoverageDeltaWithContext(p.ctx)
	if err != nil {
		p.Log().Warnf("finalize[%s]: FAIL_AT_COVERAGE target=%s covElapsed=%v totalElapsed=%v err=%v",
			source, p.targetTs.String(), time.Since(covStart), time.Since(start), err)
		return nil, err
	}
	ledgerCoverage := p.FinalLedgerCoverage(ts, coverageDelta)
	slotInflation := p.SlotInflation()
	baselineSupply := p.BaselineSupply()

	pastConeAttachmentCost := p.PastConeAttachmentCost()

	mkStart := time.Now()
	tx, hrString, err := p.makeTx() // closes the attacher
	if err != nil {
		p.Log().Warnf("finalize[%s]: FAIL_AT_MAKETX target=%s mkElapsed=%v totalElapsed=%v err=%v",
			source, p.targetTs.String(), time.Since(mkStart), time.Since(start), err)
		return nil, err
	}

	slotInflation += tx.InflationAmount()
	supply := baselineSupply + slotInflation

	var frozenP *uint64
	if frozen > 0 {
		frozenP = util.Ref(frozen)
	}
	// extract predecessor timestamp from the built transaction
	var predTs base.LedgerTime
	if seqData := tx.SequencerTransactionData(); seqData != nil {
		predOID := tx.MustInputAt(seqData.SequencerOutputData.ChainConstraint.PredecessorInputIndex)
		predTs = predOID.Timestamp()
	}

	fp := &finalProposal{
		tx:     tx,
		txSize: len(tx.Bytes()),
		txMetadata: &txmetadata.TransactionMetadata{
			SourceTypeNonPersistent: txmetadata.SourceTypeSequencer,
			CoverageDelta:           util.Ref(coverageDelta),
			FrozenCoverage:          frozenP,
			LedgerCoverage:          util.Ref(ledgerCoverage),
		},
		hrString:       hrString,
		coverageDelta:  coverageDelta,
		ledgerCoverage: ledgerCoverage,
		inflation:      tx.InflationAmount(),
		attacherName:   p.IncrementalAttacher.Name(),
		source:         source,
		predecessorTs:  predTs,
		attachmentCost: pastConeAttachmentCost,
	}

	if tx.IsBranchTransaction() {
		fp.txMetadata.LedgerCoverage = util.Ref(ledgerCoverage)
		fp.txMetadata.Supply = util.Ref(supply)
		fp.txMetadata.SlotInflation = util.Ref(slotInflation)
	}
	return fp, nil
}
