package task

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
)

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
	txb.SetName(p.environment.SequencerName() + "." + p.strategy.ShortName)

	for _, vid := range a.Endorsing() {
		if err = txb.AddEndorsement(vid.ID()); err != nil {
			return nil, fmt.Errorf("newProposal: %w", err)
		}
	}

	txb.PutExplicitBaseline(a.ExplicitBaselineID())

	return &proposal{
		proposer:            p,
		IncrementalAttacher: a,
		txb:                 txb,
	}, nil
}

func (p *proposal) Close() {
	if p != nil {
		p.IncrementalAttacher.Close()
	}
}

type _inputCandidate struct {
	o    *ledger.OutputWithID
	wOut vertex.WrappedOutput
}

func (p *proposal) insertTagAlongInputs() {
	if ledger.Const.IsPreBranchConsolidationTimestamp(p.proposer.targetTs) {
		return
	}
	if p.txb.InputsAreFull() {
		return
	}
	outs := make([]*_inputCandidate, 0)

	p.Backlog().IterateOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidSequencerPace(wOut.Timestamp(), p.proposer.targetTs) {
			return true
		}
		oid := wOut.DecodeID()
		if p.IsConsumedInThePastPath(oid, p.Extending().VID, p.BaselineSugaredStateReader) {
			return true
		}
		outs = append(outs, &_inputCandidate{
			wOut: wOut,
			o: &ledger.OutputWithID{
				ID:     oid,
				Output: wOut.Output(),
			},
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
		var cmd txbuilder_seq.TxBuilderCommand

		valid, err := p.InsertInput(o.wOut, func() (valid1 bool, err1 error) {
			cmd, valid1, err1 = p.txb.AddTagAlongInput(*o.o)
			return
		})
		if !valid {
			p.Backlog().AddToBlacklist(o.wOut)
			p.proposer.Log().Warnf("TAG_ALONG: output cannot be consumed PERMANENTLY, reason = '%v'\n%s",
				err, o.o.LinesSource("     ").String())
		} else {
			if err != nil {
				p.proposer.Log().Warnf("TAG_ALONG: output %s cannot be consumed as tag-along, reason = '%v'", o.o.ID.StringShort(), err)
			} else {
				p.proposer.Assertf(cmd != nil, "cmd != nil")
				p.proposer.Log().Infof("TAG_ALONG: output %s has been added, amount: %s, cmd='%s'",
					o.o.ID.StringShort(), util.Th(o.o.Output.TokenBalance()), cmd.String())
			}
		}
		if p.txb.InputsAreFull() {
			return
		}
	}
}

func (p *proposal) insertDelegations() {
	if ledger.Const.IsPreBranchConsolidationTimestamp(p.proposer.targetTs) {
		return
	}
	if p.txb.InputsAreFull() {
		return
	}

	outs := make([]*ledger.DelegationOutput, 0)
	p.txb.StateReader().IterateDelegatedOutputs(p.SequencerID(), func(o *ledger.DelegationOutput) bool {
		if p.Backlog().IsInBlacklist(o.ID) {
			return true
		}
		if p.IsConsumedInThePastPath(o.ID, p.Extending().VID, p.BaselineSugaredStateReader) {
			return true
		}
		if o.IsUnlockableByTargetForFreezing(uint32(p.proposer.targetTs.Slot)) {
			outs = append(outs, o)
		}
		return true
	})
	sort.Slice(outs, func(i, j int) bool {
		if outs[i].Output.TokenBalance() > outs[j].Output.TokenBalance() {
			return true
		}
		return outs[i].ID.Timestamp().Before(outs[j].ID.Timestamp())
	})
	for _, o := range outs {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		wOut := attacher.AttachOutputWithID(o.OutputWithID, p.proposer)
		// just skip if freezing failed for any reason
		valid, err := p.InsertInput(wOut, func() (bool, error) {
			_, err1 := p.txb.FreezeDelegation(o)
			return true, err1
		})
		if err != nil {
			if valid {
				p.proposer.Log().Warnf("FREEZE failed, id = %s, oid = %s, reason = '%v'",
					o.ChainID.String(), o.ID.StringShort(), err)
			} else {
				p.Backlog().AddToBlacklist(wOut)
				p.proposer.Log().Errorf("FREEZE failed PERMANENTLY, id = %s, oid = %s, reason = '%v'",
					o.ChainID.String(), o.ID.StringShort(), err)
			}
		} else {
			p.proposer.Log().Infof("FREEZE delegation has been frozen, id = %s, oid = %s",
				o.ChainID.String(), o.ID.StringShort())
		}

		if p.txb.InputsAreFull() {
			return
		}
	}
}

func (p *proposal) insertInputs() {
	p.insertDelegations()
	p.insertTagAlongInputs()
}

func (p *proposal) makeTx() (*transaction.Transaction, string, error) {
	p.Close()
	txBytes, _, txString, err := p.txb.BytesWithValidation()
	if err != nil {
		return nil, txString, err
	}
	// TODO redundant parsing back and forth
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	p.proposer.AssertNoError(err)
	return tx, txString, nil
}
