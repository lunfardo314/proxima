package task

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/commands_old"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
)

type (
	proposal struct {
		attacher *attacher.IncrementalAttacher
		txb      *txbuilder_seq.SeqTxBuilder
	}
)

// newProposal takes initial incremental attacher only with endorsements
// and stem in it, and packages it with the transaction builder
// It is ready to be filled up with tag-along inputs and delegations
func (p *proposer) newProposal(a *attacher.IncrementalAttacher) (*proposal, error) {
	seqPredVID := a.Extending()
	seqPred, ok := seqPredVID.OutputWithChainID()
	p.Assertf(ok, "inconsistency: must be a chain output")

	// TODO stem
	txb, err := txbuilder_seq.New(a.TargetTs(), &seqPred, nil, p.ControllerPrivateKey(), a.BaselineSugaredStateReader())
	if err != nil {
		return nil, fmt.Errorf("newProposal: %w", err)
	}
	return &proposal{
		attacher: a,
		txb:      txb,
	}, nil
}

func (p *proposer) makeTxProposalOld(a *attacher.IncrementalAttacher) (*transaction.Transaction, string, error) {
	cmdParser := commands_old.NewCommandParser(ledger.AddressED25519FromPrivateKey(p.ControllerPrivateKey()))
	nm := p.environment.SequencerName() + "." + p.strategy.ShortName
	tx, err := a.MakeSequencerTransaction(nm, p.ControllerPrivateKey(), cmdParser)
	// attacher and references are not needed anymore, it should be released
	extEndorseString := a.ExtendEndorseLines().Join(", ")

	a.Close()
	return tx, extEndorseString, err
}
