package task

import (
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// finalize computes coverage, builds the transaction, and returns a finalProposal.
// The proposal's attacher is closed after building the transaction.
func (p *proposal) finalize(source string) (*finalProposal, error) {
	ts := p.targetTs
	if p.effectiveTs != base.NilLedgerTime {
		ts = p.effectiveTs
	}

	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	coverageDelta, frozen, err := p.CoverageDeltaWithContext(p.ctx)
	if err != nil {
		return nil, err
	}
	ledgerCoverage := p.FinalLedgerCoverage(ts, coverageDelta)
	slotInflation := p.SlotInflation()
	baselineSupply := p.BaselineSupply()

	tx, hrString, err := p.makeTx() // closes the attacher
	if err != nil {
		return nil, err
	}

	slotInflation += tx.InflationAmount()
	supply := baselineSupply + slotInflation

	var frozenP *uint64
	if frozen > 0 {
		frozenP = util.Ref(frozen)
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
	}

	if tx.IsBranchTransaction() {
		fp.txMetadata.LedgerCoverage = util.Ref(ledgerCoverage)
		fp.txMetadata.Supply = util.Ref(supply)
		fp.txMetadata.SlotInflation = util.Ref(slotInflation)
	}
	return fp, nil
}
