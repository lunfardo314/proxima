package node_cmd

import (
	"fmt"
	"sort"
	"time"

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

// Delegation top-up for the miner, per claude/delegation_add_tokens.md.
//
// The rule has three steps and no threshold:
//
//	1. a delegation the master can consume  -> add the payouts to it
//	2. otherwise, below the cap             -> create a new delegation
//	3. otherwise                            -> askstop one; the next pass takes step 1
//
// Step 3 is what the miner normally does. An askstop costs almost nothing: the
// unwind it returns is a prepayment the delegator has not earned, and the next
// freeze pays a fresh advance over a full new span, so what is actually lost is
// only the few slots the capital spends unfrozen. Waiting for a natural window
// instead would idle the payouts for hours.

// ownDelegation is one of this miner's delegation outputs with its wallet-side
// view already parsed.
type ownDelegation struct {
	view    *txbuildercore.DelegationOutputView
	oid     base.OutputID
	bytes   []byte
	balance uint64
}

// consumable reports whether the master can spend it in the given slot: on
// hold, never frozen, or inside the safe revocation window.
func (d *ownDelegation) consumable(slot uint32, c *txbuildercore.Constants) bool {
	return !d.view.IsInFrozenSlot(slot, c)
}

// inWindow distinguishes the natural revocation window - still marked frozen,
// but past the end of its span - from on-hold and never-frozen. It is the only
// state in which the target is locked out, which makes it the owner's way past
// a sequencer that declines to process askstop requests.
func (d *ownDelegation) inWindow(slot uint32, c *txbuildercore.Constants) bool {
	return d.view.IsMarkedFrozen() && !d.view.IsInFrozenSlot(slot, c)
}

// listOwnDelegations returns every delegation chain output mastered by this
// miner's wallet.
func (m *miner) listOwnDelegations() ([]*ownDelegation, error) {
	res, err := retryCall("list own delegations", 3, func() (*client.GetOutputsResult, error) {
		return m.c.GetOutputsForControllerID(m.wallet.Account.ControllerID(), client.GetOutputsParams{
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
		view, ok, err := m.lib.ParseDelegationOutput(o.Output.Output, o.ID)
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

// pickTopUpTarget returns the smallest consumable delegation, so balances even
// out across the miner's delegations rather than one growing to dominate.
// Delegations inside a safe revocation window are skipped under
// --no-revocation-windows: taking those leaves the owner no way past a
// sequencer that refuses askstop.
func (m *miner) pickTopUpTarget(dels []*ownDelegation, slot uint32) *ownDelegation {
	var best *ownDelegation
	for _, d := range dels {
		if !d.consumable(slot, m.consts) {
			continue
		}
		if d.inWindow(slot, m.consts) && !m.useRevocationWindows {
			continue
		}
		if best == nil || d.balance < best.balance {
			best = d
		}
	}
	return best
}

// pickAskstopTarget returns the frozen delegation nearest its natural window.
// That is the cheapest one to stop - the unwind is proportional to the freeze
// time left - and the one whose window the miner would otherwise wait for.
func (m *miner) pickAskstopTarget(dels []*ownDelegation, slot uint32) *ownDelegation {
	frozen := make([]*ownDelegation, 0, len(dels))
	for _, d := range dels {
		if d.view.IsMarkedFrozen() && d.view.IsInFrozenSlot(slot, m.consts) {
			frozen = append(frozen, d)
		}
	}
	if len(frozen) == 0 {
		return nil
	}
	sort.Slice(frozen, func(i, j int) bool {
		return frozen[i].view.UnfreezeSlot(m.consts) < frozen[j].view.UnfreezeSlot(m.consts)
	})
	return frozen[0]
}

// topUpDelegation adds the accumulated payouts to an existing delegation and
// re-delegates it in one transaction. The target is re-chosen: on the master
// path the constraint does not pin the index-value tuple, so a top-up is also a
// retarget, and re-rolling keeps delegations spread over sequencers and routes
// around any that is at its per-epoch cap.
func (m *miner) topUpDelegation(d *ownDelegation, outs []*ledger.OutputWithID, total uint64) bool {
	if total <= m.actionFee {
		return false
	}
	added := total - m.actionFee

	seqID, err := m.chooseRandomAliveSequencer()
	if err != nil {
		glb.Infof("   top-up deferred: %v", err)
		return false
	}

	txb := txbuildercore.New(0)
	consumed := make([][]byte, 0, len(outs)+1)

	// input 0 is the delegation itself, unlocked on the master path (0xff)
	txb.ConsumeOutput(d.bytes, d.oid)
	consumed = append(consumed, d.bytes)
	txb.PutSignatureUnlock(0, ledger.DelegationUnlockedByMaster)
	txb.PutUnlockParams(0, txbuildercore.ConstraintIndexChain, txbuildercore.ChainUnlockParams(0))

	// Wallet payouts follow. They are sigLock outputs, so they cannot reference
	// input 0's unlock - that is a delegateLock, and reference unlock only holds
	// within one lock kind. The first payout carries its own signature unlock and
	// the rest reference it.
	maxInputTs := d.oid.Timestamp()
	for i, in := range outs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		maxInputTs = base.MaximumTime(maxInputTs, in.Timestamp())
		idx := byte(i + 1)
		if i == 0 {
			txb.PutSignatureUnlock(idx)
			continue
		}
		if err = txb.PutUnlockReference(idx, txbuildercore.ConstraintIndexLock, 1); err != nil {
			glb.Infof("   top-up build failed: %v", err)
			return false
		}
	}

	ts := m.consts.LedgerTimeFromClockTime(time.Now())
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	ts = base.MaximumTime(ts, maxInputTs.AddTicks(int(m.consts.TransactionPace)))

	inflation, err := m.projectedOneSlotInflation(d.balance, d.oid.Slot())
	if err != nil {
		glb.Infof("   top-up deferred: %v", err)
		return false
	}
	newAmount := d.balance + inflation + added

	succ, err := m.composeDelegationSuccessor(d, seqID, newAmount, inflation)
	if err != nil {
		glb.Infof("   top-up build failed: %v", err)
		return false
	}
	succIdx := txb.ProduceOutput(succ)
	if succIdx != 0 {
		glb.Infof("   top-up build failed: delegation successor must be output 0, got %d", succIdx)
		return false
	}

	tagAlongOut, err := txbuildercore.NewTagAlongOutput(m.lib, m.actionFee, m.tagAlongSeqID, m.holderID)
	if err != nil {
		glb.Infof("   top-up build failed: %v", err)
		return false
	}
	txb.ProduceOutput(tagAlongOut.Bytes())

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(m.wallet.PrivateKey)

	if err = glb.SubmitAndDisplay(txb.Bytes(), consumed...); err != nil {
		glb.Infof("   top-up submit failed: %v", err)
		return false
	}
	m.mu.Lock()
	m.st.delegations++
	m.mu.Unlock()
	glb.Infof("   topped up delegation %s with %s (now %s) -> sequencer %s (submitted, not awaited)",
		d.view.ChainID.StringShort(), util.Th(added), util.Th(newAmount), seqID.StringShort())
	return true
}

// composeDelegationSuccessor overlays the constraints delegation owns onto the
// predecessor's bytes, leaving anything else it carries untouched. Mirrors the
// `proxi node delegate chain` builder.
func (m *miner) composeDelegationSuccessor(d *ownDelegation, target base.ChainID, newAmount, inflation uint64) ([]byte, error) {
	lockBin, err := m.lib.NewDelegateLockBytecode(mineDelegationCut)
	if err != nil {
		return nil, err
	}
	chainBin, err := m.lib.NewChainTransition(
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
	stateBin, err := m.lib.NewDelegateLockState(0, 0, 0)
	if err != nil {
		return nil, err
	}
	ob, err := txbuildercore.OutputBuilderFromBytes(d.bytes)
	if err != nil {
		return nil, err
	}
	ob.PutConstraint(txbuildercore.EncodeAmounts(newAmount, inflation), txbuildercore.ConstraintIndexAmounts)
	ob.PutConstraint(txbuildercore.EncodeIndexValuesTuple([][]byte{m.holderID[:], target[:]}), txbuildercore.ConstraintIndexIndexValues)
	ob.PutConstraint(lockBin, txbuildercore.ConstraintIndexLock)
	ob.PutConstraint(chainBin, txbuildercore.ConstraintIndexChain)
	ob.PutConstraint(stateBin, byte(ob.NumConstraints()-1))
	return ob.Output().Bytes(), nil
}

// askstopDelegation asks the target to put a frozen delegation on hold, so the
// next pass can top it up. The compensation is the unearned part of the advance
// at the share pinned when it was frozen; the wallet covers what it can as the
// tag-along fee and authorises the rest as an allowance against the delegation.
func (m *miner) askstopDelegation(d *ownDelegation, outs []*ledger.OutputWithID) bool {
	slot := m.nowSlot()
	unfreeze := d.view.UnfreezeSlot(m.consts)
	if unfreeze <= slot+askstopPatienceSlots {
		glb.Infof("   askstop skipped: delegation %s unfreezes in %d slot(s), waiting is cheaper",
			d.view.ChainID.StringShort(), unfreeze-slot)
		return false
	}
	compensation, err := m.projectedCompensation(d, slot, unfreeze)
	if err != nil {
		glb.Infof("   askstop deferred: %v", err)
		return false
	}
	if len(outs) == 0 {
		glb.Infof("   askstop deferred: no wallet output to carry the request")
		return false
	}

	fee := m.actionFee
	allowance := uint64(0)
	if compensation > fee {
		allowance = compensation - fee
	}

	txb := txbuildercore.New(0)
	consumed := make([][]byte, 0, len(outs))
	var maxInputTs base.LedgerTime
	for i, in := range outs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		maxInputTs = base.MaximumTime(maxInputTs, in.Timestamp())
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			if err = txb.PutUnlockReference(byte(i), txbuildercore.ConstraintIndexLock, 0); err != nil {
				glb.Infof("   askstop build failed: %v", err)
				return false
			}
		}
	}

	extra, err := m.lib.NewEnsureStopDelegationConstraint(d.view.ChainID, allowance)
	if err != nil {
		glb.Infof("   askstop build failed: %v", err)
		return false
	}
	params := smallkv.New()
	params.Set(txbuilder_seq.FieldRevokeDelegationID, d.view.ChainID[:])
	reqOut, err := m.lib.NewSequencerRequestOutput(
		fee, d.view.Target, m.holderID, txbuilder_seq.RequestCodeAskStopDelegation, &params, extra)
	if err != nil {
		glb.Infof("   askstop build failed: %v", err)
		return false
	}
	txb.ProduceOutput(reqOut.Bytes())

	sumIn := uint64(0)
	for _, o := range outs {
		sumIn += o.Output.TokenBalance()
	}
	if sumIn < fee {
		glb.Infof("   askstop deferred: wallet holds %s, need %s for the request", util.Th(sumIn), util.Th(fee))
		return false
	}
	if remainder := sumIn - fee; remainder > 0 {
		remOut, err := txbuildercore.NewSigLockOutput(m.lib, remainder, m.holderID)
		if err != nil {
			glb.Infof("   askstop build failed: %v", err)
			return false
		}
		txb.ProduceOutput(remOut.Bytes())
	}

	ts := m.consts.LedgerTimeFromClockTime(time.Now())
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	ts = base.MaximumTime(ts, maxInputTs.AddTicks(int(m.consts.TransactionPace)))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(m.wallet.PrivateKey)

	if err = glb.SubmitAndDisplay(txb.Bytes(), consumed...); err != nil {
		glb.Infof("   askstop submit failed: %v", err)
		return false
	}
	glb.Infof("   asked sequencer %s to stop delegation %s (compensation %s, allowance %s); will top it up when on hold",
		d.view.Target.StringShort(), d.view.ChainID.StringShort(), util.Th(compensation), util.Th(allowance))
	return true
}

// askstopPatienceSlots mirrors the sequencer's own refusal margin
// (patienceMargin in req_askstop.go): inside it the delegation is about to
// unfreeze anyway, so asking is pointless and the target would decline.
const askstopPatienceSlots = 6

// projectedOneSlotInflation is the chain inflation the delegation earns in the
// transiting slot. Evaluated node-side, like the rest of the wallet's inflation
// arithmetic.
func (m *miner) projectedOneSlotInflation(balance uint64, fromSlot uint32) (uint64, error) {
	return retryCall("eval one-slot inflation", 3, func() (uint64, error) {
		return m.c.EvalU64(0, fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/1)", balance, fromSlot))
	})
}

// projectedCompensation is what stopping the delegation now returns: the
// unearned part of the advance, at the share pinned when it was frozen.
// Mirrors _projectedCompensation in ensure.easyfl, which anchors the projection
// on the delegation output's own slot so wallet and constraint agree.
func (m *miner) projectedCompensation(d *ownDelegation, _ uint32, unfreeze uint32) (uint64, error) {
	if unfreeze <= d.oid.Slot() {
		return 0, nil
	}
	uncut, err := retryCall("eval projected compensation", 3, func() (uint64, error) {
		return m.c.EvalU64(0, fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)",
			d.balance, d.oid.Slot(), unfreeze-d.oid.Slot()))
	})
	if err != nil {
		return 0, fmt.Errorf("projected compensation: %w", err)
	}
	return uncut * uint64(d.view.AdvanceShare) / 1000, nil
}
