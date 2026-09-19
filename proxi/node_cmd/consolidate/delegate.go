package consolidate

import (
	"fmt"
	"math/rand"
	"sort"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/smallkv"
)

// The delegation mode, driven by two numbers: the target number of
// delegations and the target size of one. Delegations first grow to the
// target size one at a time, then their number grows to the target, then the
// existing ones are topped up. The wallet is a price taker: a delegation
// requires exactly the cut its target leaves, and a random target is drawn in
// proportion to what it leaves, so a sequencer keeping more gets fewer
// delegations rather than none.
//
// Placing an amount from the wallet (kb/consolidate.md):
//
//  1. a consumable delegation below the target size   -> add the amount to the smallest such one
//  2. otherwise, fewer delegations than the target    -> create a new delegation
//  3. otherwise, a consumable delegation              -> add the amount to the smallest one
//  4. otherwise                                       -> askstop one; a later pass takes step 1
//
// Consumable means the master can spend it in this slot: on hold, never
// frozen, or inside its safe revocation window. A frozen delegation is left to
// its target.
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

// askstopPatienceSlots mirrors the sequencer's own refusal margin
// (patienceMargin in req_askstop.go): inside it the delegation is about to
// unfreeze anyway, so asking is pointless and the target would decline.
const askstopPatienceSlots = 6

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

// delegationTarget is one candidate sequencer reduced to what target
// selection needs: how much of the inflation it leaves a delegator.
type delegationTarget struct {
	id        base.ChainID
	tolerance uint16 // 1000 minus the sequencer's own cut, in promille
}

// delegate places the amount above the minimum, less the tag-along fee, into
// a delegation. Returns the consumed IDs, or nil when no action was taken this
// tick and the outputs should be compacted instead.
func (k *consolidator) delegate(p *plan, dels []*ownDelegation) []base.OutputID {
	if p.moved <= k.tagAlongFee {
		return nil
	}
	amount := p.moved - k.tagAlongFee
	market, err := k.delegationMarket()
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return nil
	}
	slot := k.nowSlot()
	k.classify(dels, market, slot)

	if d, create := pickPlacement(dels, k.cfg.targetDelegations, k.cfg.targetSize); d != nil {
		return k.topUpDelegation(d, p, amount, market)
	} else if create {
		return k.createDelegation(p, amount, market)
	}
	d := pickAskstopTarget(dels, slot, k.consts)
	if d == nil {
		glb.Infof("   delegation deferred: %d delegations, none consumable and none frozen", len(dels))
		return nil
	}
	return k.askstopDelegation(d, p)
}

// manageDelegations is the tidying pass that opens every tick. Returns the
// consumed IDs, or nil when nothing was done.
func (k *consolidator) manageDelegations(dels []*ownDelegation) []base.OutputID {
	if len(dels) == 0 {
		return nil
	}
	market, err := k.delegationMarket()
	if err != nil {
		glb.Verbosef("   delegations not checked: %v", err)
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

// pickPlacement applies steps 1-3 of the placement rule: the delegation to top
// up, or none and whether a new one should be created instead.
func pickPlacement(dels []*ownDelegation, targetDelegations int, targetSize uint64) (topUp *ownDelegation, create bool) {
	var smallest *ownDelegation
	for _, d := range dels {
		if !d.consumable {
			continue
		}
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
func (k *consolidator) classify(dels []*ownDelegation, market map[base.ChainID]delegationTarget, slot uint32) {
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
		case t.tolerance < d.view.RequiredInflationCut:
			d.stale = fmt.Sprintf("sequencer %s leaves %d promille, the delegation requires %d",
				d.view.Target.StringShort(), t.tolerance, d.view.RequiredInflationCut)
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

// pickAskstopTarget returns the frozen delegation nearest its natural window:
// the cheapest to stop, since the unwind is proportional to the freeze time
// left, and the one whose window would otherwise be waited for.
func pickAskstopTarget(dels []*ownDelegation, slot uint32, c *txbuildercore.Constants) *ownDelegation {
	frozen := make([]*ownDelegation, 0, len(dels))
	for _, d := range dels {
		if d.view.IsMarkedFrozen() && d.view.IsInFrozenSlot(slot, c) {
			frozen = append(frozen, d)
		}
	}
	if len(frozen) == 0 {
		return nil
	}
	sort.Slice(frozen, func(i, j int) bool {
		return frozen[i].view.UnfreezeSlot(c) < frozen[j].view.UnfreezeSlot(c)
	})
	return frozen[0]
}

// delegationMarket is the sequencers active within activeSequencerSlots and
// what each leaves a delegator: a sequencer keeps its own cut, so what it can
// leave is 1000 minus that.
func (k *consolidator) delegationMarket() (map[base.ChainID]delegationTarget, error) {
	outs, err := retry("list sequencers", 3, func() (map[base.ChainID]ledger.OutputWithSequencerData, error) {
		o, _, err := k.c.GetAllSequencerOutputs()
		return o, err
	})
	if err != nil {
		return nil, err
	}
	active, err := k.activeSequencers()
	if err != nil {
		return nil, err
	}
	market := make(map[base.ChainID]delegationTarget, len(active))
	for id, out := range outs {
		if _, ok := active[id]; !ok {
			continue
		}
		tolerance := uint16(1000)
		if sd := out.SequencerData; sd != nil {
			tolerance -= sd.InflationProfitMarginPromille()
		}
		market[id] = delegationTarget{id: id, tolerance: tolerance}
	}
	return market, nil
}

// chooseDelegationTarget picks the sequencer a delegation goes to: the
// configured one, or one drawn from the market in proportion to what it
// leaves. The delegation then requires exactly that.
func (k *consolidator) chooseDelegationTarget(market map[base.ChainID]delegationTarget) (delegationTarget, error) {
	candidates := make([]delegationTarget, 0, len(market))
	for _, t := range market {
		candidates = append(candidates, t)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].id.String() < candidates[j].id.String() })
	return selectDelegationTarget(candidates, k.cfg.delegateTo, rand.Intn)
}

// selectDelegationTarget applies the rule of chooseDelegationTarget to the
// active candidates. A random draw is weighted by tolerance, so a sequencer
// leaving nothing is never drawn; draw(n) returns a number in [0, n). Split
// out from the fetch so the rule can be exercised directly.
func selectDelegationTarget(active []delegationTarget, pinned *base.ChainID, draw func(n int) int) (delegationTarget, error) {
	if pinned != nil {
		for _, c := range active {
			if c.id != *pinned {
				continue
			}
			if c.tolerance == 0 {
				return delegationTarget{}, fmt.Errorf("sequencer %s leaves delegators nothing", pinned.StringShort())
			}
			return c, nil
		}
		return delegationTarget{}, fmt.Errorf("sequencer %s has no milestone in the last %d slots", pinned.StringShort(), activeSequencerSlots)
	}
	total := 0
	for _, c := range active {
		total += int(c.tolerance)
	}
	if total == 0 {
		if len(active) > 0 {
			return delegationTarget{}, fmt.Errorf("none of the %d active sequencers leaves delegators anything", len(active))
		}
		return delegationTarget{}, fmt.Errorf("no sequencer has been active in the last %d slots", activeSequencerSlots)
	}
	r := draw(total)
	for _, c := range active {
		if r < int(c.tolerance) {
			return c, nil
		}
		r -= int(c.tolerance)
	}
	return active[len(active)-1], nil
}

// topUpDelegation adds the amount to an existing delegation and re-delegates
// it in one transaction. The target is re-chosen: on the master path the
// constraint does not pin the index-value tuple, so a top-up is also a
// retarget, and re-rolling keeps delegations spread over sequencers and routes
// around any that is at its per-epoch cap.
func (k *consolidator) topUpDelegation(d *ownDelegation, p *plan, amount uint64, market map[base.ChainID]delegationTarget) []base.OutputID {
	target, err := k.chooseDelegationTarget(market)
	if err != nil {
		glb.Infof("   top-up deferred: %v", err)
		return nil
	}
	inflation, err := k.projectedOneSlotInflation(d.balance, d.oid.Slot())
	if err != nil {
		glb.Infof("   top-up deferred: %v", err)
		return nil
	}
	newAmount := d.balance + inflation + amount

	txb := txbuildercore.New(0)
	k.consumeDelegation(txb, d, 0, true)
	consumed, newest, err := consumeInputs(txb, p.inputs, 1)
	if err != nil {
		glb.Infof("   top-up build failed: %v", err)
		return nil
	}
	consumed = append([][]byte{d.bytes}, consumed...)
	newest = base.MaximumTime(newest, d.oid.Timestamp())

	if err = k.produceDelegationSuccessor(txb, d, target, newAmount, inflation); err != nil {
		glb.Infof("   top-up build failed: %v", err)
		return nil
	}
	if err = k.produceTagAlongAndKept(txb, p.kept); err != nil {
		glb.Infof("   top-up build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(newest))
	if err = glb.SubmitAndDisplay(txb.Bytes(), consumed...); err != nil {
		glb.Infof("   top-up submit failed: %v", err)
		return nil
	}
	glb.Infof("   consolidated %d output(s) holding %s: topped up delegation %s with %s (now %s) -> sequencer %s leaving %d promille, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), d.view.ChainID.StringShort(), util.Th(amount), util.Th(newAmount),
		target.id.StringShort(), target.tolerance, util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// retargetDelegation re-delegates a stale delegation as it is, the tag-along
// fee coming out of its balance.
func (k *consolidator) retargetDelegation(d *ownDelegation, market map[base.ChainID]delegationTarget) []base.OutputID {
	target, err := k.chooseDelegationTarget(market)
	if err != nil {
		glb.Infof("   re-delegation of %s deferred: %v", d.view.ChainID.StringShort(), err)
		return nil
	}
	inflation, err := k.projectedOneSlotInflation(d.balance, d.oid.Slot())
	if err != nil {
		glb.Infof("   re-delegation of %s deferred: %v", d.view.ChainID.StringShort(), err)
		return nil
	}
	if d.balance+inflation <= k.tagAlongFee {
		glb.Infof("   re-delegation of %s deferred: %s does not cover the tag-along fee %s", d.view.ChainID.StringShort(), util.Th(d.balance), util.Th(k.tagAlongFee))
		return nil
	}
	newAmount := d.balance + inflation - k.tagAlongFee
	minAmt, err := k.minDelegationAmount()
	if err != nil {
		glb.Infof("   re-delegation of %s deferred: %v", d.view.ChainID.StringShort(), err)
		return nil
	}
	if newAmount < minAmt {
		glb.Infof("   re-delegation of %s deferred: %s is below the minimum inflatable %s", d.view.ChainID.StringShort(), util.Th(newAmount), util.Th(minAmt))
		return nil
	}

	txb := txbuildercore.New(0)
	k.consumeDelegation(txb, d, 0, true)
	if err = k.produceDelegationSuccessor(txb, d, target, newAmount, inflation); err != nil {
		glb.Infof("   re-delegation build failed: %v", err)
		return nil
	}
	if err = k.produceTagAlongAndKept(txb, 0); err != nil {
		glb.Infof("   re-delegation build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(d.oid.Timestamp()))
	if err = glb.SubmitAndDisplay(txb.Bytes(), d.bytes); err != nil {
		glb.Infof("   re-delegation submit failed: %v", err)
		return nil
	}
	glb.Infof("   re-delegated %s holding %s (%s) -> sequencer %s leaving %d promille, fee %s -> %s (submitted, not awaited)",
		d.view.ChainID.StringShort(), util.Th(newAmount), d.stale, target.id.StringShort(), target.tolerance,
		util.Th(k.tagAlongFee), txid.StringShort())
	return []base.OutputID{d.oid}
}

// mergeDelegations folds one delegation into another: the smaller chain ends,
// its balance joins the larger one, which is re-delegated; the tag-along fee
// comes out of the combined balance.
func (k *consolidator) mergeDelegations(into, kill *ownDelegation, market map[base.ChainID]delegationTarget) []base.OutputID {
	target, err := k.chooseDelegationTarget(market)
	if err != nil {
		glb.Infof("   merge of %s into %s deferred: %v", kill.view.ChainID.StringShort(), into.view.ChainID.StringShort(), err)
		return nil
	}
	inflation, err := k.projectedOneSlotInflation(into.balance, into.oid.Slot())
	if err != nil {
		glb.Infof("   merge deferred: %v", err)
		return nil
	}
	newAmount := into.balance + inflation + kill.balance - k.tagAlongFee

	txb := txbuildercore.New(0)
	k.consumeDelegation(txb, into, 0, true)
	k.consumeDelegation(txb, kill, 1, false)
	if err = k.produceDelegationSuccessor(txb, into, target, newAmount, inflation); err != nil {
		glb.Infof("   merge build failed: %v", err)
		return nil
	}
	if err = k.produceTagAlongAndKept(txb, 0); err != nil {
		glb.Infof("   merge build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(base.MaximumTime(into.oid.Timestamp(), kill.oid.Timestamp())))
	if err = glb.SubmitAndDisplay(txb.Bytes(), into.bytes, kill.bytes); err != nil {
		glb.Infof("   merge submit failed: %v", err)
		return nil
	}
	glb.Infof("   merged delegation %s holding %s into %s (now %s) -> sequencer %s leaving %d promille, fee %s -> %s (submitted, not awaited)",
		kill.view.ChainID.StringShort(), util.Th(kill.balance), into.view.ChainID.StringShort(), util.Th(newAmount),
		target.id.StringShort(), target.tolerance, util.Th(k.tagAlongFee), txid.StringShort())
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
func (k *consolidator) produceDelegationSuccessor(txb *txbuildercore.TxBuilder, d *ownDelegation, target delegationTarget, newAmount, inflation uint64) error {
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
func (k *consolidator) composeDelegationSuccessor(d *ownDelegation, target delegationTarget, newAmount, inflation uint64) ([]byte, error) {
	lockBin, err := k.lib.NewDelegateLockBytecode(target.tolerance)
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
	ob.PutConstraint(txbuildercore.EncodeIndexValuesTuple([][]byte{k.holderID[:], target.id[:]}), txbuildercore.ConstraintIndexIndexValues)
	ob.PutConstraint(lockBin, txbuildercore.ConstraintIndexLock)
	ob.PutConstraint(chainBin, txbuildercore.ConstraintIndexChain)
	ob.PutConstraint(stateBin, byte(ob.NumConstraints()-1))
	return ob.Output().Bytes(), nil
}

// createDelegation puts the amount into a fresh delegation chain.
func (k *consolidator) createDelegation(p *plan, amount uint64, market map[base.ChainID]delegationTarget) []base.OutputID {
	minAmt, err := k.minDelegationAmount()
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return nil
	}
	if amount < minAmt {
		glb.Infof("   delegation deferred: %s is below the minimum inflatable %s", util.Th(amount), util.Th(minAmt))
		return nil
	}
	target, err := k.chooseDelegationTarget(market)
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return nil
	}

	txb := txbuildercore.New(0)
	consumed, newest, err := consumeInputs(txb, p.inputs, 0)
	if err != nil {
		glb.Infof("   delegation build failed: %v", err)
		return nil
	}
	// the delegation's start slot is the transaction's slot
	ts := k.timestamp(newest)
	delegationOut, err := k.lib.NewDelegationInitOutput(txbuildercore.DelegationInitOutputParams{
		Amount:               amount,
		MasterID:             k.holderID,
		Target:               target.id,
		RequiredInflationCut: target.tolerance,
		StartSlot:            ts.Slot,
	})
	if err != nil {
		glb.Infof("   delegation build failed: %v", err)
		return nil
	}
	delegationIdx := txb.ProduceOutput(delegationOut.Bytes())
	if err = k.produceTagAlongAndKept(txb, p.kept); err != nil {
		glb.Infof("   delegation build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, ts)
	delegationOid, err := base.NewOutputID(txid, delegationIdx)
	glb.AssertNoError(err)
	delegationID := base.MakeOriginChainID(delegationOid)

	if err = glb.SubmitAndDisplay(txb.Bytes(), consumed...); err != nil {
		glb.Infof("   delegation submit failed: %v", err)
		return nil
	}
	glb.Infof("   consolidated %d output(s) holding %s: delegated %s to sequencer %s leaving %d promille as delegation %s, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), util.Th(amount), target.id.StringShort(), target.tolerance, delegationID.StringShort(),
		util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// askstopDelegation asks the target to put a frozen delegation on hold, so
// the next pass can top it up. The compensation is the unearned part of the
// advance at the share pinned when it was frozen; the wallet covers what it
// can as the request's fee and authorises the rest as an allowance against the
// delegation. Everything consumed but the fee returns to the wallet as one
// output, so the tokens to delegate are still there when the hold lands.
func (k *consolidator) askstopDelegation(d *ownDelegation, p *plan) []base.OutputID {
	slot := k.nowSlot()
	unfreeze := d.view.UnfreezeSlot(k.consts)
	if unfreeze <= slot+askstopPatienceSlots {
		glb.Infof("   askstop skipped: delegation %s unfreezes in %d slot(s), waiting is cheaper",
			d.view.ChainID.StringShort(), unfreeze-slot)
		return nil
	}
	compensation, err := k.projectedCompensation(d, unfreeze)
	if err != nil {
		glb.Infof("   askstop deferred: %v", err)
		return nil
	}
	// The request has to reach the delegation's own target, which is not in
	// general the wallet's tag-along sequencer, and a request under that
	// sequencer's minimum fee is never picked up.
	fee, err := retry("required askstop fee", 3, func() (uint64, error) {
		return glb.GetRequiredTagAlongFee(d.view.Target)
	})
	if err != nil {
		glb.Infof("   askstop deferred: %v", err)
		return nil
	}
	if p.consumed <= fee {
		glb.Infof("   askstop deferred: %s does not cover the request fee %s", util.Th(p.consumed), util.Th(fee))
		return nil
	}
	allowance := uint64(0)
	if compensation > fee {
		allowance = compensation - fee
	}

	txb := txbuildercore.New(0)
	consumed, newest, err := consumeInputs(txb, p.inputs, 0)
	if err != nil {
		glb.Infof("   askstop build failed: %v", err)
		return nil
	}
	extra, err := k.lib.NewEnsureStopDelegationConstraint(d.view.ChainID, allowance)
	if err != nil {
		glb.Infof("   askstop build failed: %v", err)
		return nil
	}
	params := smallkv.New()
	params.Set(txbuilder_seq.FieldRevokeDelegationID, d.view.ChainID[:])
	reqOut, err := k.lib.NewSequencerRequestOutput(
		fee, d.view.Target, k.holderID, txbuilder_seq.RequestCodeAskStopDelegation, &params, extra)
	if err != nil {
		glb.Infof("   askstop build failed: %v", err)
		return nil
	}
	txb.ProduceOutput(reqOut.Bytes())
	if err = k.produceKept(txb, p.consumed-fee); err != nil {
		glb.Infof("   askstop build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(newest))
	if err = glb.SubmitAndDisplay(txb.Bytes(), consumed...); err != nil {
		glb.Infof("   askstop submit failed: %v", err)
		return nil
	}
	glb.Infof("   consolidated %d output(s) holding %s: asked sequencer %s to stop delegation %s (fee %s, compensation %s, allowance %s) -> %s; will top it up once on hold",
		len(p.inputs), util.Th(p.consumed), d.view.Target.StringShort(), d.view.ChainID.StringShort(),
		util.Th(fee), util.Th(compensation), util.Th(allowance), txid.StringShort())
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

// projectedCompensation is what stopping the delegation now returns: the
// unearned part of the advance, at the share pinned when it was frozen.
// Mirrors _projectedCompensation in ensure.easyfl, which anchors the
// projection on the delegation output's own slot so wallet and constraint
// agree.
func (k *consolidator) projectedCompensation(d *ownDelegation, unfreeze uint32) (uint64, error) {
	if unfreeze <= d.oid.Slot() {
		return 0, nil
	}
	uncut, err := retry("eval projected compensation", 3, func() (uint64, error) {
		return k.c.EvalU64(0, fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)",
			d.balance, d.oid.Slot(), unfreeze-d.oid.Slot()))
	})
	if err != nil {
		return 0, fmt.Errorf("projected compensation: %w", err)
	}
	return uncut * uint64(d.view.AdvanceShare) / 1000, nil
}
