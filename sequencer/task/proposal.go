package task

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/commands_old"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
)

type proposal struct {
	*proposer
	attacher *attacher.IncrementalAttacher
	txb      *txbuilder_seq.SeqTxBuilder
}

// newProposal takes initial incremental attacher only with endorsements
// and stem in it, and packages it with the transaction builder
// It is ready to be filled up with tag-along inputs and delegations
func (p *proposer) newProposal(a *attacher.IncrementalAttacher) (*proposal, error) {
	p.Assertf(!a.IsClosed(), "!a.IsClosed()")

	seqPredVID := a.Extending()
	seqPred, ok := seqPredVID.OutputWithChainID()
	p.Assertf(ok, "newProposal: inconsistency: must be a chain output")

	var stem *ledger.OutputWithID
	if stemWrapped := a.Stem(); stemWrapped.VID != nil {
		stem = stemWrapped.OutputWithID()
		p.Assertf(!a.TargetTs().IsSlotBoundary() || stem != nil, "newProposal: !a.TargetTs().IsSlotBoundary() || stem != nil")
	}
	txb, err := txbuilder_seq.New(a.TargetTs(), &seqPred, stem, p.ControllerPrivateKey(), a.BaselineSugaredStateReader())
	if err != nil {
		return nil, fmt.Errorf("newProposal: %w", err)
	}
	return &proposal{
		proposer: p,
		attacher: a,
		txb:      txb,
	}, nil
}

type _inputCandidate struct {
	o    *ledger.OutputWithID
	wOut vertex.WrappedOutput
}

func (p *proposal) insertTagAlongInputs(maxInputs int) {
	if p.txb.InputsAreFull() {
		return
	}
	outs := make([]*_inputCandidate, 0)

	p.Backlog().IterateOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidSequencerPace(wOut.Timestamp(), p.targetTs) {
			return true
		}
		if p.IsConsumedInThePastPath(wOut, p.attacher.Extending().VID) {
			return true
		}
		outs = append(outs, &_inputCandidate{
			wOut: wOut,
			o:    wOut.OutputWithID(),
		})
		return true
	})

	sort.Slice(outs, func(i, j int) bool {
		if outs[i].o.Output.TokenBalance() > outs[j].o.Output.TokenBalance() {
			return true
		}
		return outs[i].o.ID.Timestamp().Before(outs[j].o.ID.Timestamp())
	})

	for _, o := range outs {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		valid, err := p.attacher.InsertInput(o.wOut, func() (bool, error) {
			return p.txb.AddTagAlongInput(*o.o)
		})
		if !valid {
			p.Backlog().AddToBlacklist(o.wOut)
			p.Log().Warnf("output %s cannot be used as tag-along permanently. Reason = %v", o.o.ID.StringShort(), err)
		}
		if p.attacher.NumInputs() >= maxInputs {
			return
		}
	}
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
