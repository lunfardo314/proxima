package consolidator

import (
	"fmt"
	"math/rand"
	"slices"
	"sort"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/smallkv"
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
// A placement that cannot be made this tick (the target of a frozen delegation
// inactive, the amount under its minimum top-up, its pinned share above what
// the target leaves, the amount under the minimum inflatable) falls through to
// the next in that order, so the payouts of a wallet never wait on one
// delegation it cannot reach.
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
//    leaves it, has no room under its max_frozen bound, not the pinned one), one on hold, or one
//    its target has left unfrozen for freezeTimeoutSlots after it could freeze it -> re-delegated;
//    one too small to stand on its own is folded into the largest consumable one instead, or,
//    when there is none, ended and its balance returned to the wallet to be swept with the rest.
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
func (k *Consolidator) delegate(p *plan, dels []*ownDelegation) []base.OutputID {
	if p.moved <= k.tagAlongFee {
		return nil
	}
	market, err := k.activeSequencers()
	if err != nil {
		k.logf("delegation deferred: %v", err)
		return nil
	}
	k.classify(dels, market, k.nowSlot())

	for _, d := range placementOrder(dels, k.cfg.TargetDelegations, k.cfg.TargetSize) {
		var consumed []base.OutputID
		switch {
		case d == nil:
			consumed = k.createDelegation(p, p.moved-k.tagAlongFee, market)
		case d.consumable:
			consumed = k.topUpDelegation(d, p, p.moved-k.tagAlongFee, market)
		default:
			consumed = k.requestTopUp(d, p, market)
		}
		if consumed != nil {
			return consumed
		}
	}
	return nil
}

// manageDelegations is the tidying pass that opens every tick. Returns the
// consumed IDs, or nil when nothing was done. outs are the wallet's own outputs,
// which pay the fee of a stop request.
func (k *Consolidator) manageDelegations(dels []*ownDelegation, outs []*ledger.OutputWithID) []base.OutputID {
	if len(dels) == 0 {
		return nil
	}
	market, err := k.activeSequencers()
	if err != nil {
		k.verbosef("delegations not checked: %v", err)
		return nil
	}
	k.classify(dels, market, k.nowSlot())
	into, kill, retarget := pickManagement(dels, k.cfg.TargetDelegations)
	switch {
	case into != nil:
		return k.mergeDelegations(into, kill, market)
	case retarget != nil:
		return k.retargetDelegation(retarget, dels, market)
	}
	return k.askStopUnderMinimum(dels, outs, market)
}

// askStopUnderMinimum asks the target of a frozen delegation to stop it early when that
// target holds less than the ledger's minimum sequencer balance: it can never freeze the
// delegation again, and asking works only while it still runs. The target puts the
// delegation on hold, and the stale rule then re-delegates it. The unearned part of the
// advance it prepaid goes back to it as an allowance from the delegation; the tag-along fee
// comes from the wallet. One request per tick; none is repeated within the tag-along window.
func (k *Consolidator) askStopUnderMinimum(dels []*ownDelegation, outs []*ledger.OutputWithID, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	slot := k.nowSlot()
	if k.askedStop == nil {
		k.askedStop = make(map[base.ChainID]uint32)
	}
	for id, at := range k.askedStop {
		if slot > at+k.consts.TagAlongSlots {
			delete(k.askedStop, id)
		}
	}
	if d := askStopCandidate(dels, market, k.askedStop, slot, k.consts); d != nil {
		t := market[d.view.Target]
		unfreeze := d.view.UnfreezeSlot(k.consts)
		k.askedStop[d.view.ChainID] = slot
		fee, err := k.requiredTagAlongFee(t.ID)
		if err != nil {
			k.logf("stop request for %s deferred: %v", d.view.ChainID.StringShort(), err)
			return nil
		}
		projection, err := k.c.EvalU64(0, fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)", d.balance, slot, unfreeze-slot+1))
		if err != nil {
			k.logf("stop request for %s deferred: %v", d.view.ChainID.StringShort(), err)
			return nil
		}
		var allowance uint64
		if compensation := projection * uint64(d.view.AdvanceShare) / 1000; compensation > fee {
			allowance = compensation - fee
		}
		inputs, total := feeInputs(outs, fee, k.floor)
		if inputs == nil {
			k.logf("delegation %s is frozen at sequencer %s, which holds less than the minimum sequencer balance and cannot freeze it again; "+
				"stopping it early needs %s of the wallet's own tokens for the fee, the wallet has %s: it waits for its freeze to end in slot %d",
				d.view.ChainID.StringShort(), t.ID.StringShort(), util.Th(fee+k.floor), util.Th(sumBalance(outs)), unfreeze)
			return nil
		}
		txb := txbuildercore.New(k.consts.UpgradeIndex)
		consumed, newest, err := consumeInputs(txb, inputs, 0)
		if err != nil {
			k.logf("stop request build failed: %v", err)
			return nil
		}
		extra, err := k.lib.NewEnsureStopDelegationConstraint(d.view.ChainID, allowance)
		if err != nil {
			k.logf("stop request build failed: %v", err)
			return nil
		}
		params := smallkv.New()
		params.Set(txbuilder_seq.FieldRevokeDelegationID, d.view.ChainID[:])
		req, err := k.lib.NewSequencerRequestOutput(fee, t.ID, k.holderID, txbuilder_seq.RequestCodeAskStopDelegation, &params, extra)
		if err != nil {
			k.logf("stop request build failed: %v", err)
			return nil
		}
		txb.ProduceOutput(req.Bytes())
		if err = k.produceKept(txb, total-fee); err != nil {
			k.logf("stop request build failed: %v", err)
			return nil
		}
		txid := k.finish(txb, k.timestamp(newest))
		if err = k.submit(txb.Bytes(), consumed...); err != nil {
			k.logf("stop request submit failed: %v", err)
			return nil
		}
		k.logf("asked sequencer %s, under the minimum sequencer balance, to stop delegation %s holding %s early (fee %s, allowance %s) -> %s (submitted, not awaited)",
			t.ID.StringShort(), d.view.ChainID.StringShort(), util.Th(d.balance), util.Th(fee), util.Th(allowance), txid.StringShort())
		ret := make([]base.OutputID, len(inputs))
		for i, o := range inputs {
			ret[i] = o.ID
		}
		return ret
	}
	return nil
}

// askStopCandidate is the first frozen delegation whose target is active but holds less
// than the minimum sequencer balance, not asked already and not about to unfreeze.
func askStopCandidate(dels []*ownDelegation, market map[base.ChainID]txbuildercore.SequencerCandidate, asked map[base.ChainID]uint32, slot uint32, consts *txbuildercore.Constants) *ownDelegation {
	for _, d := range dels {
		if d.consumable || d.view.IsMarkedOnHold() {
			continue
		}
		t, active := market[d.view.Target]
		if !active || t.CanFreeze() {
			continue // an inactive target cannot take the request: the freeze runs out instead
		}
		if _, ok := asked[d.view.ChainID]; ok {
			continue
		}
		if d.view.UnfreezeSlot(consts) <= slot+6 {
			continue // about to unfreeze: the stale rule takes it then
		}
		return d
	}
	return nil
}

// feeInputs picks the wallet outputs, largest first, that pay fee and leave either nothing
// or at least floor as change. Returns nil when the wallet cannot.
func feeInputs(outs []*ledger.OutputWithID, fee, floor uint64) ([]*ledger.OutputWithID, uint64) {
	sorted := make([]*ledger.OutputWithID, len(outs))
	copy(sorted, outs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Output.TokenBalance() > sorted[j].Output.TokenBalance() })
	var total uint64
	for i, o := range sorted {
		total += o.Output.TokenBalance()
		if total == fee || total >= fee+floor {
			return sorted[:i+1], total
		}
	}
	return nil, 0
}

// placementOrder applies the placement rule as an order of attempts: the
// delegations below the target size, smallest first; then a new delegation
// (nil) when there are fewer than the target; then the rest, smallest first.
func placementOrder(dels []*ownDelegation, targetDelegations int, targetSize uint64) []*ownDelegation {
	sorted := make([]*ownDelegation, len(dels))
	copy(sorted, dels)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].balance < sorted[j].balance })
	ret := make([]*ownDelegation, 0, len(sorted)+1)
	i := 0
	for ; i < len(sorted) && sorted[i].balance < targetSize; i++ {
		ret = append(ret, sorted[i])
	}
	if len(dels) < targetDelegations {
		ret = append(ret, nil)
	}
	return append(ret, sorted[i:]...)
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
func (k *Consolidator) classify(dels []*ownDelegation, market map[base.ChainID]txbuildercore.SequencerCandidate, slot uint32) {
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
		case !t.HasRoomFor(d.balance):
			d.stale = fmt.Sprintf("sequencer %s has no room for it under its max_frozen bound", d.view.Target.StringShort())
		case !t.CanFreeze():
			d.stale = fmt.Sprintf("sequencer %s holds less than the minimum sequencer balance and cannot freeze it", d.view.Target.StringShort())
		case k.cfg.DelegateTo != nil && d.view.Target != *k.cfg.DelegateTo:
			d.stale = fmt.Sprintf("not on the configured sequencer %s", k.cfg.DelegateTo.StringShort())
		case d.view.IsMarkedOnHold():
			d.stale = "on hold, its target can no longer freeze it"
		default:
			if from := freezableFromSlot(d.view, d.oid.Slot(), k.consts); slot > from+freezeTimeoutSlots {
				d.stale = fmt.Sprintf("not frozen by its target in the %d slots since it could be", slot-from)
			}
		}
	}
}

// freezeTimeoutSlots is how long the target has to freeze a delegation it can
// freeze before the wallet moves it elsewhere, about 10 minutes. A sequencer that
// takes delegations freezes them within a few slots; one that leaves a delegation
// unfrozen is earning its cut of nothing for the delegator.
const freezeTimeoutSlots = 60

// freezableFromSlot is the first slot in which the target can freeze the
// delegation output produced in outputSlot: right after its safe revocation
// window when a freeze has run out, which only the master may act in, and
// otherwise the slot after the output, the target's successor being at least
// one slot later.
func freezableFromSlot(v *txbuildercore.DelegationOutputView, outputSlot uint32, c *txbuildercore.Constants) uint32 {
	if v.IsMarkedFrozen() {
		return v.UnfreezeSlot(c) + c.SafeRevocationSlots
	}
	return outputSlot + 1
}

// listOwnDelegations returns every delegation chain output mastered by this
// wallet.
func (k *Consolidator) listOwnDelegations() ([]*ownDelegation, error) {
	res, err := retry(k.verbosef, "list own delegations", 3, func() (*client.GetOutputsResult, error) {
		return k.c.GetOutputsForControllerID(k.account.ControllerID(), client.GetOutputsParams{
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
// requires exactly what it leaves. amount is what the delegation will hold.
func (k *Consolidator) chooseDelegationTarget(market map[base.ChainID]txbuildercore.SequencerCandidate, amount uint64) (txbuildercore.SequencerCandidate, error) {
	return DrawDelegationTarget(candidateList(market), k.cfg.DelegateTo, amount, rand.Intn,
		FreezeCountRoom(k.c, k.lib, k.consts, k.nowSlot()))
}

// DrawDelegationTarget is SelectDelegationTarget followed by the per-epoch count
// cap check of the chosen target, which needs a query and so is made for that
// one only: a drawn target without room is dropped and the draw repeated, a
// pinned one is refused.
func DrawDelegationTarget(active []txbuildercore.SequencerCandidate, pinned *base.ChainID, amount uint64, draw func(n int) int,
	hasCountRoom func(c txbuildercore.SequencerCandidate) (bool, error)) (txbuildercore.SequencerCandidate, error) {
	for {
		t, err := SelectDelegationTarget(active, pinned, amount, draw)
		if err != nil {
			return t, err
		}
		ok, err := hasCountRoom(t)
		if err != nil || ok {
			return t, err
		}
		if pinned != nil {
			return txbuildercore.SequencerCandidate{}, fmt.Errorf("sequencer %s has every epoch it can freeze into at its max_frozen_delegations cap", pinned.StringShort())
		}
		active = slices.DeleteFunc(slices.Clone(active), func(c txbuildercore.SequencerCandidate) bool { return c.ID == t.ID })
	}
}

// FreezeCountRoom returns the per-epoch count cap check of a target at slot. It
// reads the delegations addressed to the target, one query, and only when the
// cap is low enough for the answer to hold them all: the cap times the frozen
// epochs within the query's limit. Above that the sequencer cannot be seen full
// and is taken as having room; its own check is the one that holds.
func FreezeCountRoom(clnt *client.APIClient, lib *txbuildercore.Library[any], consts *txbuildercore.Constants, slot uint32) func(c txbuildercore.SequencerCandidate) (bool, error) {
	return func(c txbuildercore.SequencerCandidate) (bool, error) {
		if c.MaxFrozenDelegations == nil {
			return true, nil
		}
		capPerEpoch := *c.MaxFrozenDelegations
		if uint64(capPerEpoch)*uint64(consts.DelegationMaxFrozenEpochs) >= api.GetOutputsIterationCap {
			return true, nil
		}
		res, err := clnt.GetOutputsForControllerID(c.ID[:], client.GetOutputsParams{
			LockType:   api.GetOutputsLockTypeDelegateTarget,
			Chained:    client.ChainedOnly(),
			MaxOutputs: api.GetOutputsIterationCap,
		})
		if err != nil {
			return false, err
		}
		views := make([]*txbuildercore.DelegationOutputView, 0, len(res.Outputs))
		for _, o := range res.Outputs {
			if v, ok, err := lib.ParseDelegationOutput(o.Output.Output, o.ID); err == nil && ok {
				views = append(views, v)
			}
		}
		return !consts.FreezeEpochsFull(c.ID, views, capPerEpoch, slot), nil
	}
}

// SelectDelegationTarget applies the rule of chooseDelegationTarget to the
// active candidates: a pinned target must leave something and have room for
// amount under its max_frozen bound; otherwise the candidates that do are
// rated and one is drawn, draw(n) returning a number in [0, n). Split out from
// the fetch so the rule can be exercised directly, and shared with the miner,
// which places its reward by the same rule.
func SelectDelegationTarget(active []txbuildercore.SequencerCandidate, pinned *base.ChainID, amount uint64, draw func(n int) int) (txbuildercore.SequencerCandidate, error) {
	if pinned != nil {
		for _, c := range active {
			if c.ID != *pinned {
				continue
			}
			if c.ShareLeft == 0 {
				return txbuildercore.SequencerCandidate{}, fmt.Errorf("sequencer %s leaves delegators nothing", pinned.StringShort())
			}
			if !c.CanFreeze() {
				return txbuildercore.SequencerCandidate{}, fmt.Errorf("sequencer %s holds less than the minimum sequencer balance and cannot freeze", pinned.StringShort())
			}
			if !c.HasRoomFor(amount) {
				return txbuildercore.SequencerCandidate{}, fmt.Errorf("sequencer %s has no room for %s under its max_frozen bound", pinned.StringShort(), util.Th(amount))
			}
			return c, nil
		}
		return txbuildercore.SequencerCandidate{}, fmt.Errorf("sequencer %s has no settled milestone in the last %d slots", pinned.StringShort(), txbuildercore.ActiveSequencerSlots)
	}
	if len(active) == 0 {
		return txbuildercore.SequencerCandidate{}, fmt.Errorf("no sequencer has been active in the last %d slots", txbuildercore.ActiveSequencerSlots)
	}
	eligible := txbuildercore.DelegationCandidates(active, 0, amount)
	if len(eligible) == 0 {
		return txbuildercore.SequencerCandidate{}, fmt.Errorf("none of the %d active sequencers leaves delegators anything and has room for %s", len(active), util.Th(amount))
	}
	rated := txbuildercore.RateSequencers(eligible, txbuildercore.DelegationCriteria)
	return txbuildercore.DrawSequencer(rated, draw).SequencerCandidate, nil
}

// topUpDelegation adds the amount to an existing delegation and re-delegates
// it in one transaction. The target is re-chosen: on the master path the
// constraint does not pin the index-value tuple, so a top-up is also a
// retarget, and re-rolling keeps delegations spread over sequencers and routes
// around any that is at its per-epoch cap.
func (k *Consolidator) topUpDelegation(d *ownDelegation, p *plan, amount uint64, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	target, err := k.chooseDelegationTarget(market, d.balance+amount)
	if err != nil {
		k.logf("top-up deferred: %v", err)
		return nil
	}
	inflation, err := k.projectedOneSlotInflation(d.balance, d.oid.Slot())
	if err != nil {
		k.logf("top-up deferred: %v", err)
		return nil
	}
	newAmount := d.balance + inflation + amount

	txb := txbuildercore.New(k.consts.UpgradeIndex)
	k.consumeDelegation(txb, d, 0, true)
	consumed, newest, err := consumeInputs(txb, p.inputs, 1)
	if err != nil {
		k.logf("top-up build failed: %v", err)
		return nil
	}
	consumed = append([][]byte{d.bytes}, consumed...)
	newest = base.MaximumTime(newest, d.oid.Timestamp())

	if err = k.produceDelegationSuccessor(txb, d, target, newAmount, inflation); err != nil {
		k.logf("top-up build failed: %v", err)
		return nil
	}
	if err = k.produceTagAlongAndKept(txb, p.kept); err != nil {
		k.logf("top-up build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(newest))
	if err = k.submit(txb.Bytes(), consumed...); err != nil {
		k.logf("top-up submit failed: %v", err)
		return nil
	}
	k.logf("consolidated %d output(s) holding %s: topped up delegation %s with %s (now %s) -> sequencer %s leaving %d promille, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), d.view.ChainID.StringShort(), util.Th(amount), util.Th(newAmount),
		target.ID.StringShort(), target.ShareLeft, util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// retargetDelegation re-delegates a stale delegation as it is, the tag-along
// fee coming out of its balance. One too small to stand on its own after the
// fee is folded into the largest other consumable delegation, or ended and
// returned to the wallet when there is none: left alone it would sit idle for
// good.
func (k *Consolidator) retargetDelegation(d *ownDelegation, dels []*ownDelegation, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	target, err := k.chooseDelegationTarget(market, d.balance)
	if err != nil {
		k.logf("re-delegation of %s deferred: %v", d.view.ChainID.StringShort(), err)
		return nil
	}
	inflation, err := k.projectedOneSlotInflation(d.balance, d.oid.Slot())
	if err != nil {
		k.logf("re-delegation of %s deferred: %v", d.view.ChainID.StringShort(), err)
		return nil
	}
	minAmt, err := k.minDelegationAmount()
	if err != nil {
		k.logf("re-delegation of %s deferred: %v", d.view.ChainID.StringShort(), err)
		return nil
	}
	if d.balance+inflation < minAmt+k.tagAlongFee {
		if into := largestConsumableOther(dels, d); into != nil {
			return k.mergeDelegations(into, d, market)
		}
		return k.releaseDelegation(d)
	}
	newAmount := d.balance + inflation - k.tagAlongFee

	txb := txbuildercore.New(k.consts.UpgradeIndex)
	k.consumeDelegation(txb, d, 0, true)
	if err = k.produceDelegationSuccessor(txb, d, target, newAmount, inflation); err != nil {
		k.logf("re-delegation build failed: %v", err)
		return nil
	}
	if err = k.produceTagAlongAndKept(txb, 0); err != nil {
		k.logf("re-delegation build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(d.oid.Timestamp()))
	if err = k.submit(txb.Bytes(), d.bytes); err != nil {
		k.logf("re-delegation submit failed: %v", err)
		return nil
	}
	k.logf("re-delegated %s holding %s (%s) -> sequencer %s leaving %d promille, fee %s -> %s (submitted, not awaited)",
		d.view.ChainID.StringShort(), util.Th(newAmount), d.stale, target.ID.StringShort(), target.ShareLeft,
		util.Th(k.tagAlongFee), txid.StringShort())
	return []base.OutputID{d.oid}
}

// largestConsumableOther is the consumable delegation other than d holding
// the most, or nil.
func largestConsumableOther(dels []*ownDelegation, d *ownDelegation) *ownDelegation {
	var ret *ownDelegation
	for _, o := range dels {
		if o != d && o.consumable && (ret == nil || o.balance > ret.balance) {
			ret = o
		}
	}
	return ret
}

// releaseDelegation ends a delegation chain and returns its balance, less the
// tag-along fee, to the wallet as one sigLock output, where the sweep picks
// it up with everything else.
func (k *Consolidator) releaseDelegation(d *ownDelegation) []base.OutputID {
	if d.balance < k.tagAlongFee+k.floor {
		k.logf("release of %s deferred: %s does not cover the tag-along fee %s plus the storage deposit %s",
			d.view.ChainID.StringShort(), util.Th(d.balance), util.Th(k.tagAlongFee), util.Th(k.floor))
		return nil
	}
	txb := txbuildercore.New(k.consts.UpgradeIndex)
	k.consumeDelegation(txb, d, 0, false)
	if err := k.produceTagAlongAndKept(txb, d.balance-k.tagAlongFee); err != nil {
		k.logf("release build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(d.oid.Timestamp()))
	if err := k.submit(txb.Bytes(), d.bytes); err != nil {
		k.logf("release submit failed: %v", err)
		return nil
	}
	k.logf("released delegation %s holding %s to the wallet (%s; too small to re-delegate), fee %s -> %s (submitted, not awaited)",
		d.view.ChainID.StringShort(), util.Th(d.balance), d.stale, util.Th(k.tagAlongFee), txid.StringShort())
	return []base.OutputID{d.oid}
}

// mergeDelegations folds one delegation into another: the smaller chain ends,
// its balance joins the larger one, which is re-delegated; the tag-along fee
// comes out of the combined balance.
func (k *Consolidator) mergeDelegations(into, kill *ownDelegation, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	target, err := k.chooseDelegationTarget(market, into.balance+kill.balance)
	if err != nil {
		k.logf("merge of %s into %s deferred: %v", kill.view.ChainID.StringShort(), into.view.ChainID.StringShort(), err)
		return nil
	}
	inflation, err := k.projectedOneSlotInflation(into.balance, into.oid.Slot())
	if err != nil {
		k.logf("merge deferred: %v", err)
		return nil
	}
	newAmount := into.balance + inflation + kill.balance - k.tagAlongFee

	txb := txbuildercore.New(k.consts.UpgradeIndex)
	k.consumeDelegation(txb, into, 0, true)
	k.consumeDelegation(txb, kill, 1, false)
	if err = k.produceDelegationSuccessor(txb, into, target, newAmount, inflation); err != nil {
		k.logf("merge build failed: %v", err)
		return nil
	}
	if err = k.produceTagAlongAndKept(txb, 0); err != nil {
		k.logf("merge build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(base.MaximumTime(into.oid.Timestamp(), kill.oid.Timestamp())))
	if err = k.submit(txb.Bytes(), into.bytes, kill.bytes); err != nil {
		k.logf("merge submit failed: %v", err)
		return nil
	}
	k.logf("merged delegation %s holding %s into %s (now %s) -> sequencer %s leaving %d promille, fee %s -> %s (submitted, not awaited)",
		kill.view.ChainID.StringShort(), util.Th(kill.balance), into.view.ChainID.StringShort(), util.Th(newAmount),
		target.ID.StringShort(), target.ShareLeft, util.Th(k.tagAlongFee), txid.StringShort())
	return []base.OutputID{into.oid, kill.oid}
}

// consumeDelegation adds a delegation as input idx, unlocked on the master
// path. Each delegation input carries its own signature unlock: a reference
// unlock only holds within the plain sigLock. With continue the chain goes on
// to produced output 0, otherwise it ends here.
func (k *Consolidator) consumeDelegation(txb *txbuildercore.TxBuilder, d *ownDelegation, idx byte, cont bool) {
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
func (k *Consolidator) produceDelegationSuccessor(txb *txbuildercore.TxBuilder, d *ownDelegation, target txbuildercore.SequencerCandidate, newAmount, inflation uint64) error {
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
func (k *Consolidator) composeDelegationSuccessor(d *ownDelegation, target txbuildercore.SequencerCandidate, newAmount, inflation uint64) ([]byte, error) {
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
func (k *Consolidator) createDelegation(p *plan, amount uint64, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	minAmt, err := k.minDelegationAmount()
	if err != nil {
		k.logf("delegation deferred: %v", err)
		return nil
	}
	if amount < minAmt {
		k.logf("delegation deferred: %s is below the minimum inflatable %s", util.Th(amount), util.Th(minAmt))
		return nil
	}
	target, err := k.chooseDelegationTarget(market, amount)
	if err != nil {
		k.logf("delegation deferred: %v", err)
		return nil
	}

	txb := txbuildercore.New(k.consts.UpgradeIndex)
	consumed, newest, err := consumeInputs(txb, p.inputs, 0)
	if err != nil {
		k.logf("delegation build failed: %v", err)
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
		k.logf("delegation build failed: %v", err)
		return nil
	}
	delegationIdx := txb.ProduceOutput(delegationOut.Bytes())
	if err = k.produceTagAlongAndKept(txb, p.kept); err != nil {
		k.logf("delegation build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, ts)
	delegationOid, err := base.NewOutputID(txid, delegationIdx)
	util.AssertNoError(err)
	delegationID := base.MakeOriginChainID(delegationOid)

	if err = k.submit(txb.Bytes(), consumed...); err != nil {
		k.logf("delegation submit failed: %v", err)
		return nil
	}
	k.logf("consolidated %d output(s) holding %s: delegated %s to sequencer %s leaving %d promille as delegation %s, kept %s -> %s (submitted, not awaited)",
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
func (k *Consolidator) requestTopUp(d *ownDelegation, p *plan, market map[base.ChainID]txbuildercore.SequencerCandidate) []base.OutputID {
	target, active := market[d.view.Target]
	if !active {
		k.logf("top-up of %s deferred: sequencer %s is not active", d.view.ChainID.StringShort(), d.view.Target.StringShort())
		return nil
	}
	if !target.CanFreeze() {
		// the ledger lets it top up a frozen delegation, but it is to be stopped
		// (askStopUnderMinimum): tokens go to a target that can freeze instead
		return nil
	}
	if p.moved < target.MinimumTopUp {
		k.logf("top-up of %s deferred: %s is below the minimum top-up %s of sequencer %s",
			d.view.ChainID.StringShort(), util.Th(p.moved), util.Th(target.MinimumTopUp), target.ID.StringShort())
		return nil
	}
	if d.view.AdvanceShare > target.ShareLeft {
		k.logf("top-up of %s deferred: its pinned share %d is above what sequencer %s now leaves (%d)",
			d.view.ChainID.StringShort(), d.view.AdvanceShare, target.ID.StringShort(), target.ShareLeft)
		return nil
	}
	txb := txbuildercore.New(k.consts.UpgradeIndex)
	consumed, newest, err := consumeInputs(txb, p.inputs, 0)
	if err != nil {
		k.logf("top-up build failed: %v", err)
		return nil
	}
	extra, err := k.lib.NewEnsureTopUpDelegationConstraint(d.view.ChainID)
	if err != nil {
		k.logf("top-up build failed: %v", err)
		return nil
	}
	reqOut, err := k.lib.NewSequencerRequestOutput(p.moved, d.view.Target, k.holderID, txbuilder_seq.RequestCodeTopUpDelegation, nil, extra)
	if err != nil {
		k.logf("top-up build failed: %v", err)
		return nil
	}
	txb.ProduceOutput(reqOut.Bytes())
	if err = k.produceKept(txb, p.kept); err != nil {
		k.logf("top-up build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(newest))
	if err = k.submit(txb.Bytes(), consumed...); err != nil {
		k.logf("top-up submit failed: %v", err)
		return nil
	}
	k.logf("consolidated %d output(s) holding %s: asked sequencer %s to add %s to frozen delegation %s (now %s), kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), target.ID.StringShort(), util.Th(p.moved), d.view.ChainID.StringShort(),
		util.Th(d.balance), util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// produceTagAlongAndKept appends the wallet's tag-along fee output and the
// sigLock output it keeps, in that order.
func (k *Consolidator) produceTagAlongAndKept(txb *txbuildercore.TxBuilder, kept uint64) error {
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
func (k *Consolidator) minDelegationAmount() (uint64, error) {
	slot := k.nowSlot()
	inflMin, err := retry(k.verbosef, "eval minimum inflatable", 3, func() (uint64, error) {
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
func (k *Consolidator) projectedOneSlotInflation(balance uint64, fromSlot uint32) (uint64, error) {
	return retry(k.verbosef, "eval one-slot inflation", 3, func() (uint64, error) {
		return k.c.EvalU64(0, fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/1)", balance, fromSlot))
	})
}
