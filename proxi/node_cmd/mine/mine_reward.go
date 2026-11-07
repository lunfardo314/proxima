package mine

import (
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
)

// The reward of an own transit must be taken, not only the transit
// (kb/library_upgrade6_mining.md). A transit can be confirmed while its reward
// tag-along is not consumed: it lands under the next transit, and its target, which can
// raise its minimum top-up, fill its freeze caps or ignore requests, never takes it. A
// tag-along goes back to its sender only in the sender's window, from TagAlongSlots, and
// to anyone from TagAlongReclaimSlots. So the miner watches the reward of each confirmed
// own transit, leaves a target that does not take it out of its choice, and reclaims it.

// watchedReward is the reward tag-along of a confirmed own transit.
type watchedReward struct {
	target    base.ChainID
	slot      uint32 // the transit's slot, from which the windows count
	excluded  bool   // its target was left out for not taking it
	reclaimed uint32 // slot a reclaim was last submitted at, 0 if none
}

const (
	// slots a target has, from the transit's slot, before a reward it has not taken
	// counts against it
	rewardTakeSlots = 3
	// how often watched rewards are looked up on the node
	rewardCheckPeriod = 20 * time.Second
	// slots before a reclaim that did not land is submitted again
	reclaimRetrySlots = 10
)

// watchReward starts watching the reward of a confirmed own transit, if its reward is a
// tag-along: the full reward to a sequencer, as a top-up request or not.
func (m *miner) watchReward(txid base.TransactionID, p *minePayout, slot uint32) {
	if p == nil || p.option != txbuildercore.MinePayoutFull {
		return
	}
	m.rewardsMu.Lock()
	defer m.rewardsMu.Unlock()
	if m.rewards == nil {
		m.rewards = make(map[base.OutputID]*watchedReward)
	}
	m.rewards[base.MustNewOutputID(txid, 1)] = &watchedReward{target: p.target, slot: slot}
}

// checkRewards looks up every watched reward: a consumed one is done, one left by its
// target past rewardTakeSlots puts the target out of the choice, and one past the
// sender's window is reclaimed.
func (m *miner) checkRewards() {
	m.checkRewardsAt(m.nowSlot())
}

// lookupOutput reads an output in the latest reliable state, nil if absent there.
func (m *miner) lookupOutput(oid *base.OutputID) ([]byte, error) {
	if m.outputData != nil {
		return m.outputData(oid)
	}
	return m.c.GetOutputData(oid)
}

func (m *miner) checkRewardsAt(now uint32) {
	consts := m.constsAt(now)
	m.rewardsMu.Lock()
	watched := make(map[base.OutputID]*watchedReward, len(m.rewards))
	for oid, w := range m.rewards {
		watched[oid] = w
	}
	m.rewardsMu.Unlock()

	for oid, w := range watched {
		data, err := m.lookupOutput(&oid)
		if err != nil {
			continue // transient: the next check retries
		}
		if data == nil { // the transit is confirmed, so its output was consumed
			m.rewardsMu.Lock()
			delete(m.rewards, oid)
			m.rewardsMu.Unlock()
			continue
		}
		age := now - w.slot
		if age >= rewardTakeSlots && !w.excluded {
			w.excluded = true
			m.mu.Lock()
			m.stalledTargets[w.target] = max(m.stalledTargets[w.target], now+consts.TagAlongReclaimSlots)
			m.mu.Unlock()
			glb.Infof("   sequencer %s has not taken our reward %s; left out of the targets until slot %d",
				w.target.StringShort(), oid.StringShort(), now+consts.TagAlongReclaimSlots)
		}
		if age >= consts.TagAlongSlots && age < consts.TagAlongReclaimSlots && (w.reclaimed == 0 || now >= w.reclaimed+reclaimRetrySlots) {
			if m.reclaimReward(oid, data) {
				w.reclaimed = now
			}
		}
	}
}

// reclaimReward takes a reward tag-along back in the sender's window: the reward to the
// miner's own address, less an inclusion fee as a tag-along, the wallet's way.
func (m *miner) reclaimReward(oid base.OutputID, data []byte) bool {
	txBytes, ok := m.buildReclaim(oid, data, m.consts.LedgerTimeFromClockTime(time.Now()))
	if !ok || !m.submit(txBytes, data) {
		return false
	}
	glb.Infof("   reclaimed reward %s not taken by its target", oid.StringShort())
	return true
}

// buildReclaim builds the reclaim of a reward tag-along at ts.
func (m *miner) buildReclaim(oid base.OutputID, data []byte, ts base.LedgerTime) ([]byte, bool) {
	consts := m.constsAt(ts.Slot)
	amount, err := txbuildercore.DecodeTokenBalance(data)
	if err != nil {
		return nil, false
	}
	active := m.targetCandidates(ts.Slot)
	target := m.openingPayout(active).target
	fee := m.fee
	if consts.MineWorkIsTxID() {
		fee = max(glb.GetTagAlongFee(), active[target].MinimumFee)
	}
	if amount <= fee {
		return nil, false
	}
	txb := txbuildercore.New(consts.UpgradeIndex)
	idx := txb.ConsumeOutput(data, oid)
	txb.PutSignatureUnlock(idx)
	out, err := txbuildercore.NewSigLockOutput(m.lib, amount-fee, m.holderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(out.Bytes())
	m.produceFee(txb, target, fee)
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(m.wallet.PrivateKey)
	return txb.Bytes(), true
}
