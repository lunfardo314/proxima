package node_cmd

import (
	"context"
	"sort"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
)

// The miner's treasury loop: what happens to the payouts once they are
// confirmed.
//
// Compaction is unconditional. Every confirmed transit leaves one payout output
// behind, plus, whenever the target sequencer declines to collect it, the
// tag-along fee of that transit — and every output is permanent state the whole
// network carries for good. So the miner sweeps its claimable outputs into a
// single one as soon as P of them have accumulated, instead of leaving the
// holder to run `proxi node compact` by hand. Everything the shared classifier
// calls simply claimable goes in, not just sigLock payouts.
//
// Delegation is optional and sized, not opportunistic: it moves D into a
// delegation once the claimable balance covers D plus a reserve W that always
// stays liquid. Delegated capital is frozen for a full span, so a miner that
// delegated its whole balance could not pay the tag-along fee of its own next
// compaction.
//
// Both actions consume the same outputs, so they share one goroutine and take
// at most one action per tick — two transactions built from the same snapshot
// would double-spend each other. For the same reason the loop stands still
// while an action is in flight: the LRB snapshot keeps returning an output
// until the transaction spending it settles.

const (
	// how often the treasury loop re-reads the miner's account. A transit takes
	// several slots, so there is nothing to gain from polling faster.
	treasuryPeriod = 10 * time.Second

	// how long to wait for a submitted action before assuming it was dropped
	// (never picked up by the tag-along sequencer, or orphaned) and rebuilding
	// it from a fresh snapshot.
	treasuryPendingTimeout = 3 * time.Minute

	// upper bound on the inputs of one treasury transaction. The transaction's
	// own attachment cost is inputs + outputs, and it shares the network's
	// budget with the past cone of every input not yet in a branch state.
	treasuryMaxInputs = 100
)

// runTreasury compacts, and optionally delegates, the miner's payouts. Every
// step is best-effort: a failure only defers the action to the next tick, it
// never disturbs mining.
func (m *miner) runTreasury(ctx context.Context) {
	var (
		pending      []base.OutputID // outputs consumed by the last submitted action
		pendingSince time.Time
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(treasuryPeriod):
		}

		outs, err := m.claimableOutputs()
		if err != nil {
			glb.Verbosef("   treasury: cannot read the miner account: %v", err)
			continue
		}

		if len(pending) > 0 {
			switch {
			case !anyPresent(outs, pending):
				pending = nil // the last action settled
			case time.Since(pendingSince) > treasuryPendingTimeout:
				glb.Infof("   treasury: the last action has not settled in %v — rebuilding it", treasuryPendingTimeout)
				pending = nil
			default:
				continue // still in flight; its inputs are not ours to spend twice
			}
		}

		// Report the whole account, then act on at most one transaction's worth
		// of it — the totals line should not shrink to the size of the cap.
		total := sumBalance(outs)
		m.held.Store(total)
		m.heldCount.Store(int64(len(outs)))
		if len(outs) > treasuryMaxInputs {
			outs = largestOutputs(outs, treasuryMaxInputs)
			total = sumBalance(outs)
		}

		// A set worth no more than the fee it would cost to sweep is left alone,
		// so a wallet holding nothing but dust does not retry forever.
		switch {
		case len(outs) >= m.compactAt && total > m.actionFee:
			pending = m.compactPayouts(outs, total)
		case m.delegate:
			pending = m.delegatePayouts(outs, total)
		}
		if len(pending) > 0 {
			pendingSince = time.Now()
		}
	}
}

// claimableOutputs is everything in the miner's account a plain sweep can take
// right now: payouts, tag-along fees the target sequencer never collected, and
// anything else the shared spendable classifier calls simply claimable. Outputs
// needing a return receipt, and ones of unrecognized structure, are left alone —
// same rule as `proxi node compact`.
func (m *miner) claimableOutputs() ([]*ledger.OutputWithID, error) {
	slot := m.nowSlot()
	outs, err := retryCall("read miner account", 3, func() ([]*ledger.OutputWithID, error) {
		o, _, _, err := m.c.GetSpendableOutputs(m.wallet.Account, client.SpendableOutputsParams{
			IncludeConditionalLocks: true,
			TargetSlot:              slot,
		})
		return o, err
	})
	if err != nil {
		return nil, err
	}
	ret := make([]*ledger.OutputWithID, 0, len(outs))
	for _, o := range outs {
		cls, err := txbuildercore.ClassifySpendable(m.lib, o.Output.Bytes(), o.ID.Slot(), m.holderID, slot, m.consts.TagAlongSlots)
		if err != nil {
			glb.Verbosef("   treasury: skipping %s: %v", o.ID.StringShort(), err)
			continue
		}
		if cls == txbuildercore.SpendSimple {
			ret = append(ret, o)
		}
	}
	return ret, nil
}

// compactPayouts sweeps the claimable outputs into one sigLock output back to
// the miner. Fire-and-forget: the returned IDs are what the next ticks hold off.
func (m *miner) compactPayouts(outs []*ledger.OutputWithID, total uint64) []base.OutputID {
	txBytes, txid, consumed, err := txbuildercore.MakeCompactTransaction(m.lib, m.consts, txbuildercore.CompactParams{
		Inputs:           compactInputs(outs),
		WalletPrivateKey: m.wallet.PrivateKey,
		TagAlongSeqID:    m.tagAlongSeqID,
		TagAlongFee:      m.actionFee,
		TargetSlot:       m.nowSlot(),
	})
	if err != nil {
		glb.Infof("   compaction build failed: %v", err)
		return nil
	}
	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		glb.Infof("   compaction submit failed: %v", err)
		return nil
	}
	m.mu.Lock()
	m.st.compactions++
	m.mu.Unlock()
	glb.Infof("   compacted %d UTXO(s) holding %s into one -> %s (submitted, not awaited)",
		len(consumed), util.Th(total), txid.StringShort())
	return outputIDs(outs)
}

// delegatePayouts puts D to work once the claimable balance covers D plus the
// reserve. Three steps, per claude/delegation_add_tokens.md:
//
//  1. a delegation the master can consume  -> add D to it
//  2. otherwise, below the cap             -> create a new delegation of D
//  3. otherwise                            -> askstop one; the next pass takes step 1
//
// Step 3 is what a miner at the cap normally does. An askstop costs almost
// nothing: the unwind it returns is a prepayment the delegator has not earned,
// and the next freeze pays a fresh advance over a full new span, so what is
// actually lost is only the few slots the capital spends unfrozen. Waiting for a
// natural window instead would idle the payouts for hours.
func (m *miner) delegatePayouts(outs []*ledger.OutputWithID, total uint64) []base.OutputID {
	amount := m.delegateAmountNow()
	if total < amount+m.reserve {
		return nil
	}
	dels, err := m.listOwnDelegations()
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return nil
	}
	slot := m.nowSlot()

	if d := m.pickTopUpTarget(dels, slot); d != nil {
		return m.topUpDelegation(d, outs, total, amount)
	}
	if len(dels) < m.maxDelegations {
		return m.createDelegation(outs, total, amount)
	}
	d := m.pickAskstopTarget(dels, slot)
	if d == nil {
		glb.Infof("   delegation deferred: at the cap of %d, none consumable and none frozen", m.maxDelegations)
		return nil
	}
	return m.askstopDelegation(d, outs, total)
}

// delegateAmountNow is D: the configured amount, or ten mine rewards at the
// current slot. A is read afresh because it grows with the slot once the ramp
// starts, and D is meant to stay ten transits' worth.
func (m *miner) delegateAmountNow() uint64 {
	if m.delegateAmount > 0 {
		return m.delegateAmount
	}
	return defaultDelegateTransits * m.currentA()
}

// largestOutputs caps a UTXO set to n, keeping the largest. What is left over
// is swept by a later tick — the count only ever falls after an action, so
// nothing is stranded.
func largestOutputs(outs []*ledger.OutputWithID, n int) []*ledger.OutputWithID {
	sort.Slice(outs, func(i, j int) bool {
		return outs[i].Output.TokenBalance() > outs[j].Output.TokenBalance()
	})
	if len(outs) > n {
		outs = outs[:n]
	}
	return outs
}

func sumBalance(outs []*ledger.OutputWithID) uint64 {
	ret := uint64(0)
	for _, o := range outs {
		ret += o.Output.TokenBalance()
	}
	return ret
}

func outputIDs(outs []*ledger.OutputWithID) []base.OutputID {
	ret := make([]base.OutputID, len(outs))
	for i, o := range outs {
		ret[i] = o.ID
	}
	return ret
}

// anyPresent reports whether any of the ids is still in the snapshot, which is
// how the loop tells an unsettled action from a settled one.
func anyPresent(outs []*ledger.OutputWithID, ids []base.OutputID) bool {
	present := make(map[base.OutputID]struct{}, len(outs))
	for _, o := range outs {
		present[o.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := present[id]; ok {
			return true
		}
	}
	return false
}
