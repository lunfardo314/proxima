package task

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
)

const TraceTagProposal = "proposal"

// tagAlongBudgetFraction: tag-alongs may use up to this fraction of AttachmentCostBudget.
// Delegation freezes then use whatever remains of the full budget.
var tagAlongBudgetFraction = global.Fraction23

// newProposal takes initial incremental attacher only with endorsements
// and stem in it, and packages it with the transaction builder.
// It is ready to be filled up with tag-along inputs and delegations.
func (t *taskData) newProposal(a *attacher.IncrementalAttacher) (*proposal, error) {
	return t.newProposalWithTimestamp(a, t.targetTs)
}

func (t *taskData) newProposalWithTimestamp(a *attacher.IncrementalAttacher, ts base.LedgerTime) (*proposal, error) {
	t.Assertf(!a.IsClosed(), "!a.IsClosed()")

	seqPredVID := a.Extending()
	seqPred, ok := seqPredVID.OutputWithChainID()
	t.Assertf(ok, "newProposal: inconsistency: must be a chain output")

	var stem *ledger.OutputWithID
	if stemWrapped := a.Stem(); stemWrapped.VID != nil {
		stem = stemWrapped.OutputWithID()
		t.Assertf(!a.IsBranchTarget() || stem != nil, "newProposal: !a.IsBranchTarget() || stem != nil")
	}
	signatureType, privKey, pubKey := t.ControllerKeys()
	txb, err := txbuilder_seq.New(txbuilder_seq.Params{
		Timestamp:     ts,
		Predecessor:   &seqPred,
		Stem:          stem,
		SignatureType: signatureType,
		PrivateKey:    privKey,
		PublicKey:     pubKey,
		StateReader:   a.BaselineSugaredStateReader(),
	})
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("newProposal: %w", err)
	}
	// resolve effective name: on-chain name (from predecessor) takes priority, then config name
	if txb.EffectiveName() == "" {
		txb.SetName(t.environment.SequencerName())
	}
	for _, vid := range a.Endorsing() {
		if err = txb.AddEndorsement(vid.ID()); err != nil {
			a.Close()
			return nil, fmt.Errorf("newProposal: %w", err)
		}
	}

	txb.PutExplicitBaseline(a.ExplicitBaselineID())

	return &proposal{
		taskData:            t,
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
	p.taskData.Tracef(TraceTagProposal, "insertTagAlongInputs")
	if p.Library.IsPreBranchConsolidationTimestamp(p.taskData.targetTs) {
		return
	}
	if p.InputsAreFull() {
		return
	}
	maxTagAlongs := p.taskData.MaxTagAlongInputs()
	tagAlongsInserted := 0

	outs := make([]*_inputCandidate, 0)

	p.Backlog().IterateOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidSequencerPace(wOut.Timestamp(), p.taskData.targetTs) {
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
			// check if the attachment cost after the command will fit the tag-along sub-budget.
			// Budget numerator is scaled by sequencer pressure (2=full, 1=reduced, 0=none).
			attachmentCost := p.PastConeAttachmentCost() + p.SeqTxBuilder.AttachmentCost() + cmd.AttachmentCostDelta()
			budgetNumerator := p.TagAlongBudgetNumerator()
			tagAlongBudget := budgetNumerator * p.Library.AttachmentCostBudget / tagAlongBudgetFraction.Denominator
			if attachmentCost > tagAlongBudget {
				return true, fmt.Errorf("tag-along budget exceeded")
			}
			valid1, err1 = cmd.Apply(p.SeqTxBuilder)
			return
		})
		if !valid {
			p.Backlog().AddToBlacklist(o.wOut)
			p.taskData.WarnTopicf("tag_along", 0, "TAG_ALONG: output cannot be consumed PERMANENTLY, reason = '%v'\n%s",
				err, o.o.LinesSource("     ").String())
			p.taskData.LogTx(time.Now(), fmt.Sprintf("tag-along[%s]: PERMANENTLY rejected, reason: %v", p.Name, err), o.o.ID.TransactionID())
		} else {
			if err != nil {
				if strings.Contains(err.Error(), "already consumed") {
					p.Backlog().RemoveOutput(o.wOut)
				}
				p.taskData.WarnTopicf("tag_along", 1, "TAG_ALONG: output %s cannot be consumed as tag-along, reason = '%v'", o.o.ID.StringShort(), err)
				p.taskData.LogTx(time.Now(), fmt.Sprintf("tag-along[%s]: temporarily skipped, reason: %v", p.Name, err), o.o.ID.TransactionID())
			} else {
				p.taskData.Assertf(cmd != nil, "cmd != nil")
				p.taskData.LogTopicf("tag_along", 1, "TAG_ALONG: output %s has been added to '%s', cmd='%s'",
					o.o.ID.StringShort(), p.Name, cmd.Lines().Join(", "))
				p.taskData.LogTx(time.Now(), fmt.Sprintf("tag-along[%s]: consumed, cmd=%s", p.Name, cmd.Lines().Join(", ")), o.o.ID.TransactionID())
				tagAlongsInserted++
			}
		}
		if p.InputsAreFull() || tagAlongsInserted >= maxTagAlongs {
			return
		}
	}
}

func (p *proposal) insertDelegations() {
	p.taskData.Tracef(TraceTagProposal, "insertDelegations IN")
	defer p.taskData.Tracef(TraceTagProposal, "insertDelegations OUT")

	if p.Library.IsPreBranchConsolidationTimestamp(p.taskData.targetTs) {
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

	p.taskData.Tracef(TraceTagProposal, "insertDelegations end IterateDelegatedOutputs")
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
		wOut := attacher.AttachOutputWithID(o.OutputWithID, p.taskData)
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
				if strings.Contains(err.Error(), "already consumed") {
					p.Backlog().RemoveOutput(wOut)
				}
				p.taskData.WarnTopicf("tag_along", 1, "FREEZE failed, id = %s, oid = %s, reason = '%v'",
					o.ChainID.String(), o.ID.StringShort(), err)
			} else {
				p.Backlog().AddToBlacklist(wOut)
				p.taskData.WarnTopicf("tag_along", 0, "FREEZE failed PERMANENTLY, id = %s, oid = %s, reason = '%v'",
					o.ChainID.String(), o.ID.StringShort(), err)
			}
		} else {
			p.taskData.LogTopicf("freeze_delegation", 1, "FREEZE delegation %s, oid = %s",
				o.ChainID.String(), o.ID.StringShort())
		}

		if p.InputsAreFull() {
			return
		}
	}
}

func (p *proposal) insertInputs() {
	// tag-alongs first: they use up to tagAlongBudgetFraction of the attachment cost budget.
	// delegations second: they use whatever remains of the full budget.
	p.insertTagAlongInputs()
	p.insertDelegations()
}

func (p *proposal) makeTx() (*transaction.Transaction, string, error) {
	p.Close()

	tx, err := p.BuildTransactionWithValidation()
	if err != nil {
		if tx != nil {
			return tx, tx.String(), err
		}
		return nil, "", err
	}
	return tx, tx.String(), nil
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

	// Collect all delegation outputs under the Readable lock, then filter by blacklist
	// outside to avoid holding the Readable lock while accessing the backlog lock
	type _delegationCandidate struct {
		delegation *ledger.DelegationOutput
		frozen     bool
	}
	var candidates []_delegationCandidate

	p.StateReader().IterateDelegatedOutputs(p.SequencerID(), func(o *ledger.DelegationOutput) bool {
		c := _delegationCandidate{delegation: o}
		if o.IsInFrozenSlot(p.taskData.targetTs.Slot) {
			c.frozen = true
		}
		candidates = append(candidates, c)
		return true
	})

	// Filter and classify outside the Readable lock
	for _, c := range candidates {
		if p.Backlog().IsInBlacklist(c.delegation.ID) {
			continue
		}
		if c.delegation.IsUnlockableByTargetForFreezing(p.taskData.targetTs.Slot) {
			ret = append(ret, _delegationToFreeze{c.delegation, 0})
		}
		if c.frozen {
			nDelegationsByUnfreezeEpochMap[c.delegation.LastFrozenEpoch]++
		}
	}

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
