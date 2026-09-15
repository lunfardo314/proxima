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

// The delegation mode: what is above the minimum goes into a delegation of
// this wallet, by the three-step rule of kb/archive/shipped/delegation_add_tokens.md:
//
//  1. a delegation the master can consume  -> add the amount to it
//  2. otherwise, below the cap             -> create a new delegation
//  3. otherwise                            -> askstop one; the next pass takes step 1
//
// Step 3 is what a wallet at the cap normally does. An askstop costs almost
// nothing: the unwind it returns is a prepayment the delegator has not earned,
// and the next freeze pays a fresh advance over a full new span, so what is
// actually lost is only the few slots the capital spends unfrozen.
//
// Each action consumes the whole set the plan handed it and returns what the
// wallet keeps as a single sigLock output, so a delegation doubles as a
// compaction.

// askstopPatienceSlots mirrors the sequencer's own refusal margin
// (patienceMargin in req_askstop.go): inside it the delegation is about to
// unfreeze anyway, so asking is pointless and the target would decline.
const askstopPatienceSlots = 6

// ownDelegation is one of this wallet's delegation outputs with its
// wallet-side view already parsed.
type ownDelegation struct {
	view    *txbuildercore.DelegationOutputView
	oid     base.OutputID
	bytes   []byte
	balance uint64
}

// delegate puts the amount above the minimum, less the tag-along fee, into a
// delegation. Returns the consumed IDs, or nil when no action was taken this
// tick and the outputs should be compacted instead.
func (k *consolidator) delegate(p *plan) []base.OutputID {
	if p.moved <= k.tagAlongFee {
		return nil
	}
	amount := p.moved - k.tagAlongFee
	dels, err := k.listOwnDelegations()
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return nil
	}
	slot := k.nowSlot()
	if d := pickTopUpTarget(dels, slot, k.consts); d != nil {
		return k.topUpDelegation(d, p, amount)
	}
	if len(dels) < k.cfg.maxDelegations {
		return k.createDelegation(p, amount)
	}
	d := pickAskstopTarget(dels, slot, k.consts)
	if d == nil {
		glb.Infof("   delegation deferred: at the cap of %d, none consumable and none frozen", k.cfg.maxDelegations)
		return nil
	}
	return k.askstopDelegation(d, p)
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

// pickTopUpTarget returns the smallest delegation the master can spend in
// the slot (on hold, never frozen, or inside its safe revocation window), so
// balances even out across the wallet's delegations rather than one growing
// to dominate.
func pickTopUpTarget(dels []*ownDelegation, slot uint32, c *txbuildercore.Constants) *ownDelegation {
	var best *ownDelegation
	for _, d := range dels {
		if d.view.IsInFrozenSlot(slot, c) {
			continue
		}
		if best == nil || d.balance < best.balance {
			best = d
		}
	}
	return best
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

// delegationTarget is one candidate sequencer reduced to what target
// selection needs: the widest delegator cut it tolerates.
type delegationTarget struct {
	id        base.ChainID
	tolerance uint16
}

// chooseDelegationTarget picks the sequencer a delegation goes to: the
// configured one, or one drawn uniformly among those active within
// activeSequencerSlots. Either way the target must leave the delegator at
// least the required cut — a sequencer keeps its own cut, so what it can
// leave is 1000 minus that — or the delegation would be refused when frozen.
func (k *consolidator) chooseDelegationTarget() (base.ChainID, error) {
	outs, err := retry("list sequencers", 3, func() (map[base.ChainID]ledger.OutputWithSequencerData, error) {
		o, _, err := k.c.GetAllSequencerOutputs()
		return o, err
	})
	if err != nil {
		return base.ChainID{}, err
	}
	active, err := k.activeSequencers()
	if err != nil {
		return base.ChainID{}, err
	}
	candidates := make([]delegationTarget, 0, len(outs))
	for id, out := range outs {
		tolerance := uint16(1000)
		if sd := out.SequencerData; sd != nil {
			tolerance -= sd.InflationProfitMarginPromille()
		}
		if _, ok := active[id]; ok {
			candidates = append(candidates, delegationTarget{id: id, tolerance: tolerance})
		}
	}
	return selectDelegationTarget(candidates, k.cfg.delegateTo, k.cfg.cut)
}

// selectDelegationTarget applies the rule of chooseDelegationTarget to the
// already-fetched active candidates. Split out from the fetch so the rule can
// be exercised directly.
func selectDelegationTarget(active []delegationTarget, pinned *base.ChainID, requiredCut uint16) (base.ChainID, error) {
	if pinned != nil {
		for _, c := range active {
			if c.id != *pinned {
				continue
			}
			if c.tolerance < requiredCut {
				return base.ChainID{}, fmt.Errorf("sequencer %s leaves delegators %d promille, less than the required %d",
					pinned.StringShort(), c.tolerance, requiredCut)
			}
			return c.id, nil
		}
		return base.ChainID{}, fmt.Errorf("sequencer %s has no milestone in the last %d slots", pinned.StringShort(), activeSequencerSlots)
	}
	eligible := make([]base.ChainID, 0, len(active))
	bestTolerance := uint16(0)
	for _, c := range active {
		if c.tolerance > bestTolerance {
			bestTolerance = c.tolerance
		}
		if c.tolerance >= requiredCut {
			eligible = append(eligible, c.id)
		}
	}
	if len(eligible) > 0 {
		return eligible[rand.Intn(len(eligible))], nil
	}
	if len(active) > 0 {
		return base.ChainID{}, fmt.Errorf("none of the %d active sequencers leaves the required delegator cut of %d promille; the widest is %d (delegate.minimum_cut in the wallet profile)",
			len(active), requiredCut, bestTolerance)
	}
	return base.ChainID{}, fmt.Errorf("no sequencer has been active in the last %d slots", activeSequencerSlots)
}

// topUpDelegation adds the amount to an existing delegation and re-delegates
// it in one transaction. The target is re-chosen: on the master path the
// constraint does not pin the index-value tuple, so a top-up is also a
// retarget, and re-rolling keeps delegations spread over sequencers and routes
// around any that is at its per-epoch cap.
func (k *consolidator) topUpDelegation(d *ownDelegation, p *plan, amount uint64) []base.OutputID {
	seqID, err := k.chooseDelegationTarget()
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
	// input 0 is the delegation itself, unlocked on the master path (0xff).
	// The wallet outputs follow and cannot reference its unlock: reference
	// unlock only holds within the plain sigLock. So the first of them
	// carries its own signature unlock and the rest reference that one.
	txb.ConsumeOutput(d.bytes, d.oid)
	txb.PutSignatureUnlock(0, ledger.DelegationUnlockedByMaster)
	txb.PutUnlockParams(0, txbuildercore.ConstraintIndexChain, txbuildercore.ChainUnlockParams(0))
	consumed, newest, err := consumeInputs(txb, p.inputs, 1)
	if err != nil {
		glb.Infof("   top-up build failed: %v", err)
		return nil
	}
	consumed = append([][]byte{d.bytes}, consumed...)
	newest = base.MaximumTime(newest, d.oid.Timestamp())

	succ, err := k.composeDelegationSuccessor(d, seqID, newAmount, inflation)
	if err != nil {
		glb.Infof("   top-up build failed: %v", err)
		return nil
	}
	if idx := txb.ProduceOutput(succ); idx != 0 {
		glb.Infof("   top-up build failed: delegation successor must be output 0, got %d", idx)
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
	glb.Infof("   consolidated %d output(s) holding %s: topped up delegation %s with %s (now %s) -> sequencer %s, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), d.view.ChainID.StringShort(), util.Th(amount), util.Th(newAmount),
		seqID.StringShort(), util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// composeDelegationSuccessor overlays the constraints delegation owns onto the
// predecessor's bytes, leaving anything else it carries untouched. Mirrors the
// `proxi node delegate chain` builder.
func (k *consolidator) composeDelegationSuccessor(d *ownDelegation, target base.ChainID, newAmount, inflation uint64) ([]byte, error) {
	lockBin, err := k.lib.NewDelegateLockBytecode(k.cfg.cut)
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
	ob.PutConstraint(txbuildercore.EncodeIndexValuesTuple([][]byte{k.holderID[:], target[:]}), txbuildercore.ConstraintIndexIndexValues)
	ob.PutConstraint(lockBin, txbuildercore.ConstraintIndexLock)
	ob.PutConstraint(chainBin, txbuildercore.ConstraintIndexChain)
	ob.PutConstraint(stateBin, byte(ob.NumConstraints()-1))
	return ob.Output().Bytes(), nil
}

// createDelegation puts the amount into a fresh delegation chain.
func (k *consolidator) createDelegation(p *plan, amount uint64) []base.OutputID {
	minAmt, err := k.minDelegationAmount()
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return nil
	}
	if amount < minAmt {
		glb.Infof("   delegation deferred: %s is below the minimum inflatable %s", util.Th(amount), util.Th(minAmt))
		return nil
	}
	seqID, err := k.chooseDelegationTarget()
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
		Target:               seqID,
		RequiredInflationCut: k.cfg.cut,
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
	glb.Infof("   consolidated %d output(s) holding %s: delegated %s to sequencer %s as delegation %s, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), util.Th(amount), seqID.StringShort(), delegationID.StringShort(),
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
