package consolidate

import (
	"fmt"
	"math/rand"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
)

// The delegation mode, driven by two numbers: the target number of
// delegations and the target size of one. Delegations first grow to the
// target size one at a time, then their number grows to the target, then the
// existing ones are topped up. The wallet is a price taker: a delegation
// requires exactly the cut its target leaves, and a random target is drawn by
// the delegation rating (kb/sequencer_rating.md) among the active sequencers
// leaving anything, so a sequencer keeping more gets fewer delegations rather
// than none.
//
// Placing an amount from the wallet (kb/consolidate.md):
//
//  1. a delegation below the target size           -> add the amount to the smallest such one
//  2. otherwise, fewer delegations than the target -> create a new delegation
//  3. otherwise                                    -> add the amount to the smallest one
//
// How the amount is added depends on who can spend the delegation now. One the
// master can consume (on hold, never frozen, or inside its safe revocation
// window) is topped up and re-delegated by the wallet itself, for the
// tag-along fee. A frozen one is topped up through its target with a top-up
// request (kb/delegation_topup.md): a tag-along to the target carrying the
// whole amount, which the target adds to the delegation in place, frozen span
// and share unchanged, prepaying the advance on it. No fee, but the target's
// minimum top-up applies, so a smaller amount waits.
//
// Before anything is placed, the consumable delegations are tidied, one action
// per tick, with the fee taken out of the delegation itself:
//
//  - more delegations than the target -> the smallest consumable one is folded into the largest;
//  - a delegation whose target no longer serves it (inactive, keeps more than the delegation
//    leaves it, not the pinned one) or that has sat unfrozen for longer than an epoch -> re-delegated.
//
// That is what brings a delegation set built under other rules - by an earlier
// version, by `proxi node mine`, at another cut, on a sequencer that has since
// raised its cut - into line without anybody touching it.

// ownDelegation is one of this wallet's delegation outputs with its
// wallet-side view already parsed, and what the rules need to know about it in
// the current slot.
type ownDelegation struct {
	view    *txbuildercore.DelegationOutputView
	oid     base.OutputID
	bytes   []byte
	balance uint64

	consumable bool   // the master can spend it in this slot
	stale      string // why it should be re-delegated; empty when its target serves it
}

// delegate places the amount above the minimum into a delegation, less the
// tag-along fee where the wallet builds the transition itself. Returns the
// consumed IDs, or nil when no action was taken this tick and the outputs
// should be compacted instead.
func (k *consolidator) delegate(p *plan, dels []*ownDelegation) []base.OutputID {
	if p.moved <= k.tagAlongFee {
		return nil
	}
	market, err := k.activeSequencers()
	if err != nil {
		logf("delegation deferred: %v", err)
		return nil
	}
	k.classify(dels, market, k.nowSlot())

	d, create := pickPlacement(dels, k.cfg.targetDelegations, k.cfg.targetSize)
	switch {
	case d != nil && d.consumable:
		return k.topUpDelegation(d, p, p.moved-k.tagAlongFee, market)
	case d != nil:
		return k.requestTopUp(d, p, market)
	case create:
		return k.createDelegation(p, p.moved-k.tagAlongFee, market)
	}
	return nil
}

// manageDelegations is the tidying pass that opens every tick. Returns the
// consumed IDs, or nil when nothing was done.
func (k *consolidator) manageDelegations(dels []*ownDelegation) []base.OutputID {
	if len(dels) == 0 {
		return nil
	}
	market, err := k.activeSequencers()
	if err != nil {
		verbosef("delegations not checked: %v", err)
		return nil
	}
	k.classify(dels, market, k.nowSlot())
	into, kill, retarget := pickManagement(dels, k.cfg.targetDelegations)
	switch {
	case into != nil:
		return k.mergeDelegations(into, kill, market)
	case retarget != nil:
		return k.retargetDelegation(retarget, market)
	}
	return nil
}

// pickPlacement applies the placement rule: the delegation to top up, or none
// and whether a new one should be created instead.
func pickPlacement(dels []*ownDelegation, targetDelegations int, targetSize uint64) (topUp *ownDelegation, create bool) {
	var smallest *ownDelegation
	for _, d := range dels {
		if smallest == nil || d.balance < smallest.balance {
			smallest = d
		}
	}
	if smallest != nil && smallest.balance < targetSize {
		return smallest, false
	}
	if len(dels) < targetDelegations {
		return nil, true
	}
	return smallest, false
}

// pickManagement applies the tidying rule: with more delegations than the
// target, the smallest consumable one is folded into the largest consumable
// one; otherwise the first stale consumable one is re-delegated.
func pickManagement(dels []*ownDelegation, targetDelegations int) (into, kill, retarget *ownDelegation) {
	if len(dels) > targetDelegations {
		for _, d := range dels {
			if !d.consumable {
				continue
			}
			if into == nil || d.balance > into.balance {
				into = d
			}
			if kill == nil || d.balance < kill.balance {
				kill = d
			}
		}
		if into != nil && into != kill {
			return into, kill, nil
		}
	}
	for _, d := range dels {
		if d.consumable && d.stale != "" {
			return nil, nil, d
		}
	}
	return nil, nil, nil
}

// classify fills what the rules read off each delegation in the current slot.
func (k *consolidator) classify(dels []*ownDelegation, market map[base.ChainID]txbuildercore.SequencerCandidate, slot uint32) {
	for _, d := range dels {
		d.consumable = !d.view.IsInFrozenSlot(slot, k.consts)
		d.stale = ""
		if !d.consumable {
			continue
		}
		t, active := market[d.view.Target]
		switch {
		case !active:
			d.stale = fmt.Sprintf("sequencer %s is not active", d.view.Target.StringShort())
		case t.ShareLeft < d.view.RequiredInflationCut:
			d.stale = fmt.Sprintf("sequencer %s leaves %d promille, the delegation requires %d",
				d.view.Target.StringShort(), t.ShareLeft, d.view.RequiredInflationCut)
		case k.cfg.delegateTo != nil && d.view.Target != *k.cfg.delegateTo:
			d.stale = fmt.Sprintf("not on the configured sequencer %s", k.cfg.delegateTo.StringShort())
		case slot > d.oid.Slot()+k.consts.DelegationEpochSlots:
			d.stale = fmt.Sprintf("unfrozen for %d slots, longer than an epoch", slot-d.oid.Slot())
		}
	}
}

// listOwnDelegations returns every delegation chain output mastered by this
// wallet.
func (k *consolidator) listOwnDelegations() ([]*ownDelegation, error) {
	res, err := retry("list own delegations", 3, func() (*client.GetOutputsResult, error) {
		return k.c.GetOutputsForControllerID(k.wallet.Account.ControllerID(), client.GetOutputsParams{
			LockType:   api.GetOutputsLockTypeDelegateMaster,
			Chained:    client.ChainedOnly(),
			MaxOutputs: api.GetOutputsIterationCap,
		})
	})
	if err != nil {
		return nil, err
	}
	ret := make([]*ownDelegation, 0, len(res.Outputs))
	for _, o := range res.Outputs {
		view, ok, err := k.lib.ParseDelegationOutput(o.Output.Output, o.ID)
		if err != nil || !ok {
			continue
		}
		ret = append(ret, &ownDelegation{
			view:    view,
			oid:     o.ID,
			bytes:   o.Output.Bytes(),
			balance: o.Output.TokenBalance(),
		})
	}
	return ret, nil
}

// chooseDelegationTarget picks the sequencer a delegation goes to: the
// configured one, or one drawn by the delegation rating. The delegation then
// requires exactly what it leaves.
func (k *consolidator) chooseDelegationTarget(market map[base.ChainID]txbuildercore.SequencerCandidate) (txbuildercore.SequencerCandidate, error) {
	return selectDelegationTarget(candidateList(market), k.cfg.delegateTo, rand.Intn)
}

// selectDelegationTarget applies the rule of chooseDelegationTarget to the
// active candidates: a pinned target must leave something; otherwise the
// candidates that do are rated and one is drawn, draw(n) returning a number
// in [0, n). Split out from the fetch so the rule can be exercised directly.
func selectDelegationTarget(active []txbuildercore.SequencerCandidate, pinned *base.ChainID, draw func(n int) int) (txbuildercore.SequencerCandidate, error) {
	if pinned != nil {
		for _, c := range active {
			if c.ID != *pinned {
				continue
			}
			if c.ShareLeft == 0 {
				return txbuildercore.SequencerCandidate{}, fmt.Errorf("sequencer %s leaves delegators nothing", pinned.StringShort())
			}
			return c, nil
		}
		return txbuildercore.SequencerCandidate{}, fmt.Errorf("sequencer %s has no settled milestone in the last %d slots", pinned.StringShort(), txbuildercore.ActiveSequencerSlots)
	}
	if len(active) == 0 {
		return txbuildercore.SequencerCandidate{}, fmt.Errorf("no sequencer has been active in the last %d slots", txbuildercore.ActiveSequencerSlots)
	}
	eligible := txbuildercore.DelegationCandidates(active, 0)
	if len(eligible) == 0 {
		return txbuildercore.SequencerCandidate{}, fmt.Errorf("none of the %d active sequencers leaves delegators anything", len(active))
	}
	rated := txbuildercore.RateSequencers(eligible, txbuildercore.DelegationCriteria)
	return txbuildercore.DrawSequencer(rated, draw).SequencerCandidate, nil
}

// topUpDelegation adds the amount to an existing delegation and re-delegates
// it in one transaction. The target is re-chosen: on the master path the
// constraint does not pin the index-value tuple, so a top-up is also a
// retarget, and re-rolling keeps delegations spread over sequencers and routes
// around any that is at its per-epoch cap.
func (k *consolidator) topUpDelegation(d *ownDelegation, p *plan, amount uint64, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	target, err := k.chooseDelegationTarget(market)
	if err != nil {
		logf("top-up deferred: %v", err)
		return nil
	}
	inflation, err := k.projectedOneSlotInflation(d.balance, d.oid.Slot())
	if err != nil {
		logf("top-up deferred: %v", err)
		return nil
	}
	newAmount := d.balance + inflation + amount

	txb := txbuildercore.New(0)
	k.consumeDelegation(txb, d, 0, true)
	consumed, newest, err := consumeInputs(txb, p.inputs, 1)
	if err != nil {
		logf("top-up build failed: %v", err)
		return nil
	}
	consumed = append([][]byte{d.bytes}, consumed...)
	newest = base.MaximumTime(newest, d.oid.Timestamp())

	if err = k.produceDelegationSuccessor(txb, d, target, newAmount, inflation); err != nil {
		logf("top-up build failed: %v", err)
		return nil
	}
	if err = k.produceTagAlongAndKept(txb, p.kept); err != nil {
		logf("top-up build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(newest))
	if err = glb.SubmitAndDisplay(txb.Bytes(), consumed...); err != nil {
		logf("top-up submit failed: %v", err)
		return nil
	}
	logf("consolidated %d output(s) holding %s: topped up delegation %s with %s (now %s) -> sequencer %s leaving %d promille, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), d.view.ChainID.StringShort(), util.Th(amount), util.Th(newAmount),
		target.ID.StringShort(), target.ShareLeft, util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// retargetDelegation re-delegates a stale delegation as it is, the tag-along
// fee coming out of its balance.
func (k *consolidator) retargetDelegation(d *ownDelegation, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	target, err := k.chooseDelegationTarget(market)
	if err != nil {
		logf("re-delegation of %s deferred: %v", d.view.ChainID.StringShort(), err)
		return nil
	}
	inflation, err := k.projectedOneSlotInflation(d.balance, d.oid.Slot())
	if err != nil {
		logf("re-delegation of %s deferred: %v", d.view.ChainID.StringShort(), err)
		return nil
	}
	if d.balance+inflation <= k.tagAlongFee {
		logf("re-delegation of %s deferred: %s does not cover the tag-along fee %s", d.view.ChainID.StringShort(), util.Th(d.balance), util.Th(k.tagAlongFee))
		return nil
	}
	newAmount := d.balance + inflation - k.tagAlongFee
	minAmt, err := k.minDelegationAmount()
	if err != nil {
		logf("re-delegation of %s deferred: %v", d.view.ChainID.StringShort(), err)
		return nil
	}
	if newAmount < minAmt {
		logf("re-delegation of %s deferred: %s is below the minimum inflatable %s", d.view.ChainID.StringShort(), util.Th(newAmount), util.Th(minAmt))
		return nil
	}

	txb := txbuildercore.New(0)
	k.consumeDelegation(txb, d, 0, true)
	if err = k.produceDelegationSuccessor(txb, d, target, newAmount, inflation); err != nil {
		logf("re-delegation build failed: %v", err)
		return nil
	}
	if err = k.produceTagAlongAndKept(txb, 0); err != nil {
		logf("re-delegation build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(d.oid.Timestamp()))
	if err = glb.SubmitAndDisplay(txb.Bytes(), d.bytes); err != nil {
		logf("re-delegation submit failed: %v", err)
		return nil
	}
	logf("re-delegated %s holding %s (%s) -> sequencer %s leaving %d promille, fee %s -> %s (submitted, not awaited)",
		d.view.ChainID.StringShort(), util.Th(newAmount), d.stale, target.ID.StringShort(), target.ShareLeft,
		util.Th(k.tagAlongFee), txid.StringShort())
	return []base.OutputID{d.oid}
}

// mergeDelegations folds one delegation into another: the smaller chain ends,
// its balance joins the larger one, which is re-delegated; the tag-along fee
// comes out of the combined balance.
func (k *consolidator) mergeDelegations(into, kill *ownDelegation, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	target, err := k.chooseDelegationTarget(market)
	if err != nil {
		logf("merge of %s into %s deferred: %v", kill.view.ChainID.StringShort(), into.view.ChainID.StringShort(), err)
		return nil
	}
	inflation, err := k.projectedOneSlotInflation(into.balance, into.oid.Slot())
	if err != nil {
		logf("merge deferred: %v", err)
		return nil
	}
	newAmount := into.balance + inflation + kill.balance - k.tagAlongFee

	txb := txbuildercore.New(0)
	k.consumeDelegation(txb, into, 0, true)
	k.consumeDelegation(txb, kill, 1, false)
	if err = k.produceDelegationSuccessor(txb, into, target, newAmount, inflation); err != nil {
		logf("merge build failed: %v", err)
		return nil
	}
	if err = k.produceTagAlongAndKept(txb, 0); err != nil {
		logf("merge build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(base.MaximumTime(into.oid.Timestamp(), kill.oid.Timestamp())))
	if err = glb.SubmitAndDisplay(txb.Bytes(), into.bytes, kill.bytes); err != nil {
		logf("merge submit failed: %v", err)
		return nil
	}
	logf("merged delegation %s holding %s into %s (now %s) -> sequencer %s leaving %d promille, fee %s -> %s (submitted, not awaited)",
		kill.view.ChainID.StringShort(), util.Th(kill.balance), into.view.ChainID.StringShort(), util.Th(newAmount),
		target.ID.StringShort(), target.ShareLeft, util.Th(k.tagAlongFee), txid.StringShort())
	return []base.OutputID{into.oid, kill.oid}
}

// consumeDelegation adds a delegation as input idx, unlocked on the master
// path. Each delegation input carries its own signature unlock: a reference
// unlock only holds within the plain sigLock. With continue the chain goes on
// to produced output 0, otherwise it ends here.
func (k *consolidator) consumeDelegation(txb *txbuildercore.TxBuilder, d *ownDelegation, idx byte, cont bool) {
	txb.ConsumeOutput(d.bytes, d.oid)
	txb.PutSignatureUnlock(idx, ledger.DelegationUnlockedByMaster)
	if cont {
		txb.PutUnlockParams(idx, txbuildercore.ConstraintIndexChain, txbuildercore.ChainUnlockParams(0))
	} else {
		txb.PutUnlockParams(idx, txbuildercore.ConstraintIndexChain, txbuildercore.FinishChainUnlockParams)
	}
}

// produceDelegationSuccessor appends the successor of d at output 0, on the
// chosen target and requiring exactly what it leaves.
func (k *consolidator) produceDelegationSuccessor(txb *txbuildercore.TxBuilder, d *ownDelegation, target txbuildercore.SequencerCandidate, newAmount, inflation uint64) error {
	succ, err := k.composeDelegationSuccessor(d, target, newAmount, inflation)
	if err != nil {
		return err
	}
	if idx := txb.ProduceOutput(succ); idx != 0 {
		return fmt.Errorf("delegation successor must be output 0, got %d", idx)
	}
	return nil
}

// composeDelegationSuccessor overlays the constraints delegation owns onto the
// predecessor's bytes, leaving anything else it carries untouched. Mirrors the
// `proxi node delegate chain` builder.
func (k *consolidator) composeDelegationSuccessor(d *ownDelegation, target txbuildercore.SequencerCandidate, newAmount, inflation uint64) ([]byte, error) {
	lockBin, err := k.lib.NewDelegateLockBytecode(target.ShareLeft)
	if err != nil {
		return nil, err
	}
	chainBin, err := k.lib.NewChainTransition(
		d.view.ChainID,
		0, // predecessor input index
		d.view.ChainOriginSlot,
		d.view.CumulativeChainInflation+inflation,
		d.view.CumulativeBranchBonus,
		d.view.TransitionCounter+1,
		d.view.BranchCounter,
	)
	if err != nil {
		return nil, err
	}
	// re-delegating resets the state: not frozen, no last epoch, no pinned
	// advance share. The target freezes it again and pins a fresh share.
	stateBin, err := k.lib.NewDelegateLockState(0, 0, 0)
	if err != nil {
		return nil, err
	}
	ob, err := txbuildercore.OutputBuilderFromBytes(d.bytes)
	if err != nil {
		return nil, err
	}
	ob.PutConstraint(txbuildercore.EncodeAmounts(newAmount, inflation), txbuildercore.ConstraintIndexAmounts)
	ob.PutConstraint(txbuildercore.EncodeIndexValuesTuple([][]byte{k.holderID[:], target.ID[:]}), txbuildercore.ConstraintIndexIndexValues)
	ob.PutConstraint(lockBin, txbuildercore.ConstraintIndexLock)
	ob.PutConstraint(chainBin, txbuildercore.ConstraintIndexChain)
	ob.PutConstraint(stateBin, byte(ob.NumConstraints()-1))
	return ob.Output().Bytes(), nil
}

// createDelegation puts the amount into a fresh delegation chain.
func (k *consolidator) createDelegation(p *plan, amount uint64, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	minAmt, err := k.minDelegationAmount()
	if err != nil {
		logf("delegation deferred: %v", err)
		return nil
	}
	if amount < minAmt {
		logf("delegation deferred: %s is below the minimum inflatable %s", util.Th(amount), util.Th(minAmt))
		return nil
	}
	target, err := k.chooseDelegationTarget(market)
	if err != nil {
		logf("delegation deferred: %v", err)
		return nil
	}

	txb := txbuildercore.New(0)
	consumed, newest, err := consumeInputs(txb, p.inputs, 0)
	if err != nil {
		logf("delegation build failed: %v", err)
		return nil
	}
	// the delegation's start slot is the transaction's slot
	ts := k.timestamp(newest)
	delegationOut, err := k.lib.NewDelegationInitOutput(txbuildercore.DelegationInitOutputParams{
		Amount:               amount,
		MasterID:             k.holderID,
		Target:               target.ID,
		RequiredInflationCut: target.ShareLeft,
		StartSlot:            ts.Slot,
	})
	if err != nil {
		logf("delegation build failed: %v", err)
		return nil
	}
	delegationIdx := txb.ProduceOutput(delegationOut.Bytes())
	if err = k.produceTagAlongAndKept(txb, p.kept); err != nil {
		logf("delegation build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, ts)
	delegationOid, err := base.NewOutputID(txid, delegationIdx)
	glb.AssertNoError(err)
	delegationID := base.MakeOriginChainID(delegationOid)

	if err = glb.SubmitAndDisplay(txb.Bytes(), consumed...); err != nil {
		logf("delegation submit failed: %v", err)
		return nil
	}
	logf("consolidated %d output(s) holding %s: delegated %s to sequencer %s leaving %d promille as delegation %s, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), util.Th(amount), target.ID.StringShort(), target.ShareLeft, delegationID.StringShort(),
		util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// requestTopUp adds the amount to a frozen delegation through its target: one
// tag-along to the target carrying the whole amount and an
// ensureTopUpDelegation naming the delegation. The target adds it in place
// and prepays the advance; the wallet pays nothing. The request is the
// transaction's only tag-along, so a refusal leaves the inputs unspent and the
// next tick plans again. What is kept returns to the wallet as one output.
func (k *consolidator) requestTopUp(d *ownDelegation, p *plan, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	target, active := market[d.view.Target]
	if !active {
		logf("top-up of %s deferred: sequencer %s is not active", d.view.ChainID.StringShort(), d.view.Target.StringShort())
		return nil
	}
	if p.moved < target.MinimumTopUp {
		logf("top-up of %s deferred: %s is below the minimum top-up %s of sequencer %s",
			d.view.ChainID.StringShort(), util.Th(p.moved), util.Th(target.MinimumTopUp), target.ID.StringShort())
		return nil
	}
	if d.view.AdvanceShare > target.ShareLeft {
		logf("top-up of %s deferred: its pinned share %d is above what sequencer %s now leaves (%d)",
			d.view.ChainID.StringShort(), d.view.AdvanceShare, target.ID.StringShort(), target.ShareLeft)
		return nil
	}
	txb := txbuildercore.New(0)
	consumed, newest, err := consumeInputs(txb, p.inputs, 0)
	if err != nil {
		logf("top-up build failed: %v", err)
		return nil
	}
	extra, err := k.lib.NewEnsureTopUpDelegationConstraint(d.view.ChainID)
	if err != nil {
		logf("top-up build failed: %v", err)
		return nil
	}
	reqOut, err := k.lib.NewSequencerRequestOutput(p.moved, d.view.Target, k.holderID, txbuilder_seq.RequestCodeTopUpDelegation, nil, extra)
	if err != nil {
		logf("top-up build failed: %v", err)
		return nil
	}
	txb.ProduceOutput(reqOut.Bytes())
	if err = k.produceKept(txb, p.kept); err != nil {
		logf("top-up build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(newest))
	if err = glb.SubmitAndDisplay(txb.Bytes(), consumed...); err != nil {
		logf("top-up submit failed: %v", err)
		return nil
	}
	logf("consolidated %d output(s) holding %s: asked sequencer %s to add %s to frozen delegation %s (now %s), kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), target.ID.StringShort(), util.Th(p.moved), d.view.ChainID.StringShort(),
		util.Th(d.balance), util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// produceTagAlongAndKept appends the wallet's tag-along fee output and the
// sigLock output it keeps, in that order.
func (k *consolidator) produceTagAlongAndKept(txb *txbuildercore.TxBuilder, kept uint64) error {
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(k.lib, k.tagAlongFee, k.tagAlongSeqID, k.holderID)
	if err != nil {
		return err
	}
	txb.ProduceOutput(tagAlongOut.Bytes())
	return k.produceKept(txb, kept)
}

// minDelegationAmount is the "minimum inflatable" floor for a fresh delegation
// output, projected over a wide slot horizon. Computed node-side via /eval so
// the wallet stays singleton-free (mirrors `proxi node dlg amount`).
func (k *consolidator) minDelegationAmount() (uint64, error) {
	slot := k.nowSlot()
	inflMin, err := retry("eval minimum inflatable", 3, func() (uint64, error) {
		return k.c.EvalU64(0, fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)",
			k.consts.MinimumInflatableAmount0, 0, slot+10000))
	})
	if err != nil {
		return 0, err
	}
	return k.consts.MinimumInflatableAmount0 + inflMin, nil
}

// projectedOneSlotInflation is the chain inflation the delegation earns in the
// transiting slot, evaluated node-side like the rest of the wallet's
// inflation arithmetic.
func (k *consolidator) projectedOneSlotInflation(balance uint64, fromSlot uint32) (uint64, error) {
	return retry("eval one-slot inflation", 3, func() (uint64, error) {
		return k.c.EvalU64(0, fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/1)", balance, fromSlot))
	})
}
