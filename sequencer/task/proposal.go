package task

import (
	"fmt"
	"math"
	"sort"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
)

const TraceTagProposal = "proposal"

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
	signatureType, privKey, pubKey := p.ControllerKeys()
	txb, err := txbuilder_seq.New(txbuilder_seq.Params{
		Timestamp:     a.TargetTs(),
		Predecessor:   &seqPred,
		Stem:          stem,
		SignatureType: signatureType,
		PrivateKey:    privKey,
		PublicKey:     pubKey,
		StateReader:   a.BaselineSugaredStateReader(),
	})
	if err != nil {
		a.Close() // FIX: close attacher on error
		return nil, fmt.Errorf("newProposal: %w", err)
	}
	txb.SetName(p.environment.SequencerName() + "." + p.strategy.ShortName)

	for _, vid := range a.Endorsing() {
		if err = txb.AddEndorsement(vid.ID()); err != nil {
			a.Close() // FIX: close attacher on error
			return nil, fmt.Errorf("newProposal: %w", err)
		}
	}

	txb.PutExplicitBaseline(a.ExplicitBaselineID())

	return &proposal{
		proposer:            p,
		IncrementalAttacher: a,
		SeqTxBuilder:        txb,
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
	p.Tracef(TraceTagProposal, "insertTagAlongInputs")
	if p.Library.IsPreBranchConsolidationTimestamp(p.proposer.targetTs) {
		return
	}
	if p.InputsAreFull() {
		return
	}

	outs := make([]*_inputCandidate, 0)

	p.Backlog().IterateOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidSequencerPace(wOut.Timestamp(), p.proposer.targetTs) {
			return true
		}
		outs = append(outs, &_inputCandidate{
			wOut: wOut,
			o:    wOut.OutputWithID(),
		})
		return true
	})

	tip := p.Extending().VID
	outs = util.PurgeSlice(outs, func(el *_inputCandidate) bool {
		// do not put into iteration to avoid deadlock
		return !p.IsConsumedInThePastPath(el.o.ID, tip, p.BaselineSugaredStateReader)
	})
	// sort by fee desc and ts
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
			if cmd, valid1, err1 = p.TxBuilderCommandFromOutput(*o.o); err1 != nil {
				return
			}
			// check if the attachment cost after the command will fit the budget
			attachmentCost := p.PastConeAttachmentCost() + p.SeqTxBuilder.AttachmentCost() + cmd.AttachmentCostDelta()
			if attachmentCost > p.Library.AttachmentCostBudget {
				return true, fmt.Errorf("attachment cost budget exceeded")
			}
			valid1, err1 = cmd.Apply(p.SeqTxBuilder)
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
				p.proposer.Log().Infof("TAG_ALONG: output %s has been added to '%s', cmd='%s'",
					o.o.ID.StringShort(), p.Name, cmd.Lines().Join(", "))
			}
		}
		if p.InputsAreFull() {
			return
		}
	}
}

func (p *proposal) insertDelegations() {
	p.Tracef(TraceTagProposal, "insertDelegations IN")
	defer p.Tracef(TraceTagProposal, "insertDelegations OUT")

	if p.Library.IsPreBranchConsolidationTimestamp(p.proposer.targetTs) {
		return
	}
	if p.InputsAreFull() {
		return
	}

	// make a list of potential delegations with optimal freeze periods
	outs := p.selectDelegationsToFreeze()
	// filter out those which are consumed in the past.
	// warning: do not put IsConsumedInThePastPath into the iteration closure because it causes deadlock
	tip := p.Extending().VID
	outs = util.PurgeSlice(outs, func(dOut _delegationToFreeze) bool {
		return !p.IsConsumedInThePastPath(dOut.ID, tip, p.BaselineSugaredStateReader)
	})
	if len(outs) == 0 {
		return
	}

	p.Tracef(TraceTagProposal, "insertDelegations end IterateDelegatedOutputs")
	// sort by frozen amount descending
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
			// adding one more delegation means +1 input and +1 output, 2 cost units of the transaction attachment cost more.
			// Checking if the updated proposal will still fit the attachment budget
			attachmentCost := p.PastConeAttachmentCost() + p.PastConeAttachmentCost() + p.SeqTxBuilder.AttachmentCost() + 2
			if attachmentCost > p.Library.AttachmentCostBudget {
				return true, fmt.Errorf("attachment budget exceeded")
			}
			_, valid, err1 := p.FreezeDelegation(o.DelegationOutput, o.freezeUntilEpoch)
			return valid, err1
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
			p.proposer.Log().Infof("FREEZE delegation %s, oid = %s",
				o.ChainID.String(), o.ID.StringShort())
		}

		if p.InputsAreFull() {
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

	txBytes, _, txString, err := p.BytesWithValidation()
	if err != nil {
		return nil, txString, err
	}
	// TODO redundant parsing back and forth
	tx, err := transaction.Parse(txBytes, transaction.MainTxValidationOptions...)
	p.proposer.AssertNoError(err)
	return tx, txString, nil
}

type _delegationToFreeze struct {
	*ledger.DelegationOutput
	freezeUntilEpoch uint32
}

// selectDelegationsToFreeze selects all delegation outputs with can be frozen.
// Optimizes epoch to freeze so that achieve as even as possible distribution over delegation epochs
// This is needed for scalability and for minimization of coverage fluctuations in the consensus
func (p *proposal) selectDelegationsToFreeze() []_delegationToFreeze {
	ret := make([]_delegationToFreeze, 0)
	nDelegationsByUnfreezeEpochMap := make(map[uint32]int)

	txEpoch := p.EpochFromSlotDirect(p.SequencerID(), p.TransactionData.Timestamp.Slot)

	for e := txEpoch; e < txEpoch+p.MaxFrozenEpochs; e++ {
		nDelegationsByUnfreezeEpochMap[e] = 0
	}

	p.StateReader().IterateDelegatedOutputs(p.SequencerID(), func(o *ledger.DelegationOutput) bool {
		if p.Backlog().IsInBlacklist(o.ID) {
			return true
		}
		if o.IsUnlockableByTargetForFreezing(p.proposer.targetTs.Slot) {
			ret = append(ret, _delegationToFreeze{o, 0})
		}
		if o.IsInFrozenSlot(p.proposer.targetTs.Slot) {
			nDelegationsByUnfreezeEpochMap[o.LastFrozenEpoch]++
		}
		return true
	})

	for i := range ret {
		ret[i].freezeUntilEpoch = optimalFreezeEpoch(ret[i].FreezeUntilMax(p.TransactionData.Timestamp), nDelegationsByUnfreezeEpochMap)
		nDelegationsByUnfreezeEpochMap[ret[i].freezeUntilEpoch]++
	}
	return ret
}

// optimalFreezeEpoch finds epoch with minimum delegation unfreezing in it.
// Returns minimum of it and maximum possible by the delegation constraint
func optimalFreezeEpoch(maxPossible uint32, distribution map[uint32]int) uint32 {
	util.Assertf(len(distribution) > 0, "len(distribution)>0")

	// find what the lowest number of delegations
	loN := math.MaxInt
	for _, n := range distribution {
		if n < loN {
			loN = n
		}
	}
	var epoch uint32
	// choose latest epoch among those that has the lowest number of delegations
	for e, n := range distribution {
		if n == loN && e > epoch {
			epoch = e
		}
	}
	util.Assertf(epoch != 0, "epoch!=0")
	return min(epoch, maxPossible)
}
