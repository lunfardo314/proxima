package task

import (
	"fmt"
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

	// Bound the attacher's past-cone descent by the build budget (task.ctx deadline), so tag-along
	// insertion cannot overrun the budget even under slow I/O.
	if dl, ok := t.ctx.Deadline(); ok {
		a.SetBuildDeadline(dl)
	}

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
	if maxTagAlongs <= 0 {
		// max_tag_along_inputs == 0: the sequencer accepts no tag-along inputs
		return
	}
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

	// Self-throttle the tag-along phase to a fraction of the hard budget (scaled by sequencer pressure:
	// 2=full, 1=reduced, 0=none). The shared attacher enforces this reduced budget during descent — no
	// separate budget arithmetic here. Delegations later run against the full budget (see insertDelegations).
	budgetNumerator := p.TagAlongBudgetNumerator()
	tagAlongBudget := budgetNumerator * p.Library.AttachmentCostBudget / tagAlongBudgetFraction.Denominator
	p.SetEffectiveCostBudget(tagAlongBudget)

	for _, o := range outs {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		// Parse the command up front (pure, lock-free) so seqTxCost can be set before the descent.
		cmd, cmdValid, cmdErr := p.TxBuilderCommandFromOutput(*o.o)
		if cmdErr != nil {
			if !cmdValid {
				p.Backlog().AddToBlacklist(o.wOut)
				p.taskData.WarnTopicf("tag_along", 0, "TAG_ALONG: output cannot be consumed PERMANENTLY, reason = '%v'\n%s",
					cmdErr, o.o.LinesSource("     ").String())
				p.taskData.LogTx(time.Now(), fmt.Sprintf("tag-along[%s]: output %s PERMANENTLY rejected, reason = '%v'", p.Name, o.o.ID.StringShort(), cmdErr), o.o.ID.TransactionID())
			} else {
				p.taskData.WarnTopicf("tag_along", 1, "TAG_ALONG: output %s cannot be consumed as tag-along, reason = '%v'", o.o.ID.StringShort(), cmdErr)
				p.taskData.LogTx(time.Now(), fmt.Sprintf("tag-along[%s]: output %s temporarily skipped, reason = '%v'", p.Name, o.o.ID.StringShort(), cmdErr), o.o.ID.TransactionID())
			}
			continue
		}
		// seqTxCost = builder cost after applying this command; the shared budget check (past cone + seqTxCost)
		// then runs during the input's descent, exceeding-early with ErrAttachmentBudgetExceeded if it doesn't fit.
		seqTxCost := p.SeqTxBuilder.AttachmentCost() + cmd.AttachmentCostDelta()
		valid, err := p.InsertInput(o.wOut, seqTxCost, func() (bool, error) {
			return cmd.Apply(p.SeqTxBuilder)
		})
		if !valid {
			p.Backlog().AddToBlacklist(o.wOut)
			p.taskData.WarnTopicf("tag_along", 0, "TAG_ALONG: output cannot be consumed PERMANENTLY, reason = '%v'\n%s",
				err, o.o.LinesSource("     ").String())
			p.taskData.LogTx(time.Now(), fmt.Sprintf("tag-along[%s]: output %s PERMANENTLY rejected, reason = '%v'", p.Name, o.o.ID.StringShort(), err), o.o.ID.TransactionID())
		} else {
			if err != nil {
				if strings.Contains(err.Error(), "already consumed") {
					p.Backlog().RemoveOutput(o.wOut)
				}
				p.taskData.WarnTopicf("tag_along", 1, "TAG_ALONG: output %s cannot be consumed as tag-along, reason = '%v'", o.o.ID.StringShort(), err)
				p.taskData.LogTx(time.Now(), fmt.Sprintf("tag-along[%s]: output %s temporarily skipped, reason = '%v'", p.Name, o.o.ID.StringShort(), err), o.o.ID.TransactionID())
			} else {
				p.taskData.Assertf(cmd != nil, "cmd != nil")
				p.taskData.LogTopicf("tag_along", 1, "TAG_ALONG: output %s has been added to '%s', cmd='%s'",
					o.o.ID.StringShort(), p.Name, cmd.Lines().Join(", "))
				p.taskData.LogTx(time.Now(), fmt.Sprintf("tag-along[%s]: output %s consumed, cmd='%s'", p.Name, o.o.ID.StringShort(), cmd.Lines().Join(", ")), o.o.ID.TransactionID())
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

	// Delegations run against the full hard budget (they use whatever the tag-along phase left).
	p.SetEffectiveCostBudget(p.Library.AttachmentCostBudget)

	// candidates with assigned optimal freeze epochs, from the in-memory pool (sorted, largest first)
	toFreeze := p.selectDelegationsToFreeze()
	tip := p.Extending().VID
	for _, d := range toFreeze {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		if p.Backlog().IsInBlacklist(d.outputID) {
			continue
		}
		// already frozen in this milestone chain (or frozen only by an orphaned
		// sibling not on this tip's past cone) -> skip.
		// warning: IsConsumedInThePastPath must not be called inside InsertInput's
		// closure (lock-ordering deadlock), hence the pre-check here.
		if p.IsConsumedInThePastPath(d.outputID, tip, p.BaselineSugaredStateReader) {
			continue
		}
		// MANDATORY objective read: the pool cannot know whether the master reclaimed
		// during the safe-revocation window. Fetch the current output; if gone or no
		// longer a delegation targeting us, skip (caught lazily here, no scan needed).
		owid, err := p.StateReader().GetOutputWithID(d.outputID)
		if err != nil || owid == nil {
			continue
		}
		dOut, ok := ledger.AsDelegationOutput(owid.Output, owid.ID)
		if !ok || dOut.Target != p.SequencerID() {
			continue
		}
		wOut := attacher.AttachOutputWithID(*owid, p.taskData)
		freezeUntilEpoch := d.freezeUntilEpoch
		// FreezeDelegation adds +1 input and +1 output → +2 cost units. seqTxCost is the builder cost after
		// applying it; the shared budget check (past cone + seqTxCost) runs during the input's descent.
		seqTxCost := p.SeqTxBuilder.AttachmentCost() + 2
		valid, err := p.InsertInput(wOut, seqTxCost, func() (bool, error) {
			_, valid1, err1 := p.FreezeDelegation(&dOut, freezeUntilEpoch)
			return valid1, err1
		})
		if err != nil {
			if valid {
				if strings.Contains(err.Error(), "already consumed") {
					p.Backlog().RemoveOutput(wOut)
				}
				p.taskData.WarnTopicf("tag_along", 1, "FREEZE failed, id = %s, oid = %s, reason = '%v'",
					d.chainID.String(), d.outputID.StringShort(), err)
			} else {
				p.Backlog().AddToBlacklist(wOut)
				p.taskData.WarnTopicf("tag_along", 0, "FREEZE failed PERMANENTLY, id = %s, oid = %s, reason = '%v'",
					d.chainID.String(), d.outputID.StringShort(), err)
			}
		} else {
			p.taskData.LogTopicf("freeze_delegation", 1, "FREEZE delegation %s, oid = %s",
				d.chainID.String(), d.outputID.StringShort())
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

	txBytes, loader, err := p.BytesWithInputLoader()
	if err != nil {
		return nil, "", err
	}
	tx, err := transaction.ParseAndValidate(txBytes, loader)
	if err != nil {
		if tx != nil {
			return tx, tx.String(), err
		}
		return nil, "", err
	}
	return tx, tx.String(), nil
}

type _delegationToFreeze struct {
	chainID          base.ChainID
	outputID         base.OutputID
	amount           uint64
	freezeUntilEpoch uint32
}

// selectDelegationsToFreeze reads the freezable candidates and the amount-weighted
// per-epoch frozen load from the in-memory delegation pool (no per-proposal trie
// scan), then assigns each candidate an optimal freeze epoch so the unfrozen
// amount spreads as evenly as possible across the reachable epochs. This minimizes
// coverage fluctuation and scales to thousands of delegations.
// See claude/delegation_freeze_distribution.md.
func (p *proposal) selectDelegationsToFreeze() []_delegationToFreeze {
	// Epoch params from this chain's sequencer constraint (immutable, asserted
	// non-zero in SeqTxBuilder.New): epochSlots and N = maxFrozenEpochs.
	// 0 = the sequencer accepts (freezes) no delegations
	maxFrozenPerEpoch := uint64(p.MaxFrozenDelegations())
	if maxFrozenPerEpoch == 0 {
		return nil
	}

	chainEpochSlots, chainMaxFrozenEpochs := p.SeqTxBuilder.ChainDelegationParams()
	slot := p.TxData.Timestamp.Slot
	txEpoch := p.EpochFromSlotDirect(p.SequencerID(), slot, chainEpochSlots)
	N := uint32(chainMaxFrozenEpochs)

	candidates, load, count := p.DelegationPoolSnapshot(slot)
	if len(candidates) == 0 {
		return nil
	}
	// amount-weighted load D and frozen-count C over the reachable window [txEpoch, txEpoch+N-1]
	D := make([]uint64, N)
	C := make([]uint64, N)
	for e, amt := range load {
		if e >= txEpoch && e < txEpoch+N {
			D[e-txEpoch] += amt
		}
	}
	for e, cnt := range count {
		if e >= txEpoch && e < txEpoch+N {
			C[e-txEpoch] += cnt
		}
	}
	// freeze the largest delegations first (biggest coverage impact); ts tiebreak
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Amount != candidates[j].Amount {
			return candidates[i].Amount > candidates[j].Amount
		}
		return candidates[i].OutputID.Timestamp().Before(candidates[j].OutputID.Timestamp())
	})
	ret := make([]_delegationToFreeze, 0, len(candidates))
	for _, c := range candidates {
		reach := uint32(c.MaxFrozenEpochs) // relative indices [0, reach-1]
		if reach == 0 || reach > N {
			reach = N
		}
		// Longest freeze that does not concentrate: the latest least-loaded epoch within
		// the delegation's cap (restricted before selection, never clamped after; later
		// index wins ties). Applied to every freeze — first-time AND continuation. A
		// continuation must rebalance, not re-freeze to the fixed maximum: anchoring the
		// re-freeze to txEpoch discards the phase set at first freeze, so delegations that
		// unfreeze together (e.g. all of them after a network outage) collapse onto one
		// epoch and, being D-blind, never separate again. Rebalancing on every freeze
		// keeps D even and self-heals such concentration, while latestArgmin still hands
		// each delegation the longest freeze the load allows.
		i, ok := latestArgminUnderCap(D, C, reach, maxFrozenPerEpoch)
		if !ok {
			// every reachable epoch is at the per-epoch frozen cap: refuse this freeze for
			// now (the delegation stays unfrozen and is retried in a later milestone).
			continue
		}
		D[i] += c.Amount // credit so later placements in this pass still spread
		C[i]++            // count toward the per-epoch cap for later placements in this pass
		ret = append(ret, _delegationToFreeze{
			chainID:          c.ChainID,
			outputID:         c.OutputID,
			amount:           c.Amount,
			freezeUntilEpoch: txEpoch + i,
		})
	}
	return ret
}

// latestArgminUnderCap returns the largest index in [0,reach) that holds the minimum load
// among epochs whose frozen count is still below cap. ok is false when every reachable
// epoch is already at the cap. Later index wins load ties (a longer freeze is preferred).
func latestArgminUnderCap(D, C []uint64, reach uint32, capPerEpoch uint64) (uint32, bool) {
	found := false
	var best uint32
	var minLoad uint64
	for i := uint32(0); i < reach; i++ {
		if C[i] >= capPerEpoch {
			continue
		}
		if !found || D[i] <= minLoad {
			minLoad = D[i]
			best = i
			found = true
		}
	}
	return best, found
}

// latestArgmin returns the largest index in [0,reach) holding the minimum value.
// Later index wins ties: a longer freeze is economically preferred.
func latestArgmin(D []uint64, reach uint32) uint32 {
	best := uint32(0)
	minLoad := D[0]
	for i := uint32(1); i < reach; i++ {
		if D[i] <= minLoad {
			minLoad = D[i]
			best = i
		}
	}
	return best
}
