package mine

import (
	"fmt"
	"math/rand"
	"sort"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/consolidator"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
)

// The reward of a transit is one of three layouts the mine covenant prices
// (kb/mine_payout_options.md): an opening payout to a plain output, a fresh
// delegation, or the full amount as one tag-along, the first two minting less
// than A. The miner picks the most profitable layout its account allows
// before every round, so putting the reward to work is the default and needs
// no second process. The placement rules are the consolidator's: grow a
// delegation to the target size, then the count to the target, then top up
// the smallest, the delegations at a pinned target first.

// minePayout is the reward layout of one transit: the option the successor
// declares, what it mints, and where the reward goes.
type minePayout struct {
	option       uint64
	minted       uint64
	target       base.ChainID // the tag-along target: the sequencer that includes the transit
	topUp        bool         // option 3 as a top-up request to delegationID, else a plain tag-along
	delegationID base.ChainID
	cut          uint16 // option 2: the cut the new delegation requires, what its target leaves
	why          string // one phrase for the round's log line
}

// ownDelegation is one of the miner's delegations as the choice reads it.
type ownDelegation struct {
	view    *txbuildercore.DelegationOutputView
	balance uint64
	frozen  bool
}

// choosePayout picks the layout of the transit stamped at succSlot; ok is
// false when there is no target to pay into this round, and the loop waits:
//
//  1. the account is not known on the ledger yet: the opening, which is how
//     it becomes known; only this option passes the node's unknown-sender gate;
//  2. an own sequencer is configured and active: the full amount to it;
//  3. otherwise the reward goes into the delegations by placeReward.
//
// A known account is never opened again: every extra opening is one more idle
// output and three quarters of the reward not minted. A sequencer that is down
// or has let a transit stall is replaced by a delegation to another one, and a
// node that cannot be read is answered from the last sequencer list.
func (m *miner) choosePayout(succSlot uint32) (minePayout, bool) {
	a := m.consts.MineAmountAtSlot(succSlot)
	active := m.targetCandidates(succSlot)
	if !m.accountKnown() {
		return m.openingPayout(active), true
	}
	if m.cfg.SendOwn && m.cfg.SendTo != nil {
		_, ok := active[*m.cfg.SendTo]
		logChange(&m.ownDown, !ok,
			fmt.Sprintf("   own sequencer %s is not active or did not include our transits; rewards go into delegations until it is back", m.cfg.SendTo.StringShort()),
			fmt.Sprintf("   own sequencer %s is back; rewards go to it in full", m.cfg.SendTo.StringShort()))
		if ok {
			return minePayout{option: txbuildercore.MinePayoutFull, minted: a, target: *m.cfg.SendTo,
				why: "full reward to own sequencer " + m.cfg.SendTo.StringShort()}, true
		}
	}
	dels, err := m.ownDelegations(succSlot)
	if err != nil {
		// a new delegation needs only the sequencer list
		glb.Verbosef("   cannot list own delegations: %v", err)
		dels = nil
	}
	p, ok := m.placeReward(a, active, dels)
	logChange(&m.noTarget, !ok,
		"   no active sequencer can take the reward; waiting for one",
		"   an active sequencer can take the reward again")
	return p, ok
}

// placeReward places a reward of a into the delegations dels, smallest first,
// by the consolidator's rules, as an order of attempts:
//
//  1. a top-up of the smallest delegation below the target size that can take one;
//  2. fewer delegations than the target count: a new delegation;
//  3. a top-up of the smallest delegation that can take one;
//  4. a new delegation.
//
// While a pinned autodelegate target is active, steps 1 to 3 read only the
// delegations at it, and a top-up of another delegation comes before step 4.
// Without that, a delegation opened elsewhere while the pinned target was
// down would take every top-up after it came back, being the smallest.
func (m *miner) placeReward(a uint64, active map[base.ChainID]txbuildercore.SequencerCandidate, dels []*ownDelegation) (minePayout, bool) {
	candidates := make([]txbuildercore.SequencerCandidate, 0, len(active))
	for _, c := range active {
		candidates = append(candidates, c)
	}
	first, rest := dels, []*ownDelegation(nil)
	if m.cfg.DelegateTo != nil {
		_, err := consolidator.SelectDelegationTarget(candidates, m.cfg.DelegateTo, nil)
		logChange(&m.pinnedDown, err != nil,
			fmt.Sprintf("   delegation target %s is unavailable (%v); rewards go to other sequencers until it is back", m.cfg.DelegateTo.StringShort(), err),
			fmt.Sprintf("   delegation target %s is back; rewards go to it first", m.cfg.DelegateTo.StringShort()))
		if err == nil {
			first = nil
			for _, d := range dels {
				if d.view.Target == *m.cfg.DelegateTo {
					first = append(first, d)
				} else {
					rest = append(rest, d)
				}
			}
		}
	}
	topUp := func(d *ownDelegation) minePayout {
		return minePayout{option: txbuildercore.MinePayoutFull, minted: a, target: d.view.Target, topUp: true, delegationID: d.view.ChainID,
			why: fmt.Sprintf("full reward topping up delegation %s (%s) at sequencer %s", d.view.ChainID.StringShort(), util.Th(d.balance), d.view.Target.StringShort())}
	}
	canTopUp := func(d *ownDelegation) bool {
		t, ok := active[d.view.Target]
		return ok && t.MinimumTopUp <= a && (!d.frozen || d.view.AdvanceShare <= t.ShareLeft)
	}
	newDelegation := func() (minePayout, bool) {
		t, _, err := pickDelegationTarget(candidates, m.cfg.DelegateTo, rand.Intn)
		if err != nil {
			glb.Verbosef("   no target for a new delegation: %v", err)
			return minePayout{}, false
		}
		return minePayout{
			option: txbuildercore.MinePayoutDelegation,
			minted: m.consts.MineDelegationAmountProx*m.consts.SmallestAmountsPerBaseToken + m.consts.MineTagAlongFee,
			target: t.ID, cut: t.ShareLeft,
			why: fmt.Sprintf("%s PROX into a new delegation at sequencer %s leaving %d promille (%d of %d)",
				util.Th(m.consts.MineDelegationAmountProx), t.ID.StringShort(), t.ShareLeft, len(first)+1, m.cfg.TargetDelegations),
		}, true
	}
	for _, d := range first {
		if d.balance < m.cfg.TargetSize && canTopUp(d) {
			return topUp(d), true
		}
	}
	if len(first) < m.cfg.TargetDelegations {
		if p, ok := newDelegation(); ok {
			return p, true
		}
	}
	for _, d := range append(first, rest...) {
		if canTopUp(d) {
			return topUp(d), true
		}
	}
	return newDelegation()
}

// fullPayoutTo is the full option as a plain tag-along to the sequencer: what
// an own sequencer gets, and what the tests build.
func (m *miner) fullPayoutTo(target base.ChainID, succSlot uint32) minePayout {
	return minePayout{option: txbuildercore.MinePayoutFull, minted: m.consts.MineAmountAtSlot(succSlot), target: target, why: "full reward to sequencer " + target.StringShort()}
}

// openingPayout opens the account. Its fee goes to the tag-along sequencer of
// the profile, or, while that one is not active, to an active sequencer drawn
// by the tag-along rating: a transit only lands if its target includes it.
func (m *miner) openingPayout(active map[base.ChainID]txbuildercore.SequencerCandidate) minePayout {
	target := m.tagAlongSeqID
	if _, ok := active[target]; !ok && len(active) > 0 {
		candidates := make([]txbuildercore.SequencerCandidate, 0, len(active))
		for _, c := range active {
			if c.MinimumFee <= m.fee {
				candidates = append(candidates, c)
			}
		}
		if len(candidates) > 0 {
			target = txbuildercore.DrawSequencer(txbuildercore.RateSequencers(candidates, txbuildercore.TagAlongCriteria), rand.Intn).ID
			glb.Verbosef("   tag-along sequencer %s is not active; the opening goes to %s", m.tagAlongSeqID.StringShort(), target.StringShort())
		}
	}
	return minePayout{
		option: txbuildercore.MinePayoutOpening,
		minted: m.consts.MineOpeningAmountProx*m.consts.SmallestAmountsPerBaseToken + m.consts.MineTagAlongFee,
		target: target,
		why:    fmt.Sprintf("%s PROX to the account (the account is not known on the ledger yet)", util.Th(m.consts.MineOpeningAmountProx)),
	}
}

// targetCandidates is the active sequencers a reward can be paid into at slot:
// the last list the node gave, of any age when it cannot be read now, minus
// the sequencers that let one of this miner's transits stall. The exclusions
// step aside when they would leave nothing: a stall can also be the node's.
func (m *miner) targetCandidates(slot uint32) map[base.ChainID]txbuildercore.SequencerCandidate {
	active, err := m.activeSequencers(slot)
	if err != nil {
		if m.seqs == nil {
			glb.Verbosef("   cannot list the sequencers: %v", err)
			return nil
		}
		glb.Verbosef("   cannot list the sequencers: %v; using the list of slot %d", err, m.seqsSlot)
		active = m.seqs
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ret := make(map[base.ChainID]txbuildercore.SequencerCandidate, len(active))
	for id, c := range active {
		if until, ok := m.stalledTargets[id]; ok && slot < until {
			continue
		}
		ret[id] = c
	}
	if len(ret) == 0 {
		return active
	}
	return ret
}

// excludeStalledTargets leaves out, until slot until, the sequencers the
// unconfirmed transits of the branch being dropped were tagged to: they did
// not include them, and the active list only notices a stopped sequencer
// several slots late.
func (m *miner) excludeStalledTargets(until uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.tree.speculativePayouts() {
		if m.stalledTargets[p.target] < until {
			glb.Infof("   sequencer %s did not include our transit; left out of the targets until slot %d", p.target.StringShort(), until)
		}
		m.stalledTargets[p.target] = until
	}
}

// logChange logs on or off once each time *flag changes to now.
func logChange(flag *bool, now bool, on, off string) {
	if now == *flag {
		return
	}
	*flag = now
	if now {
		glb.Infof("%s", on)
	} else {
		glb.Infof("%s", off)
	}
}

// accountKnown reports whether the ledger knows this wallet's holder, which
// the first confirmed transit makes true for good. A node that cannot answer
// counts as knowing it: wrongly assumed known costs one refused transit, while
// wrongly assumed unknown opens the account once more.
func (m *miner) accountKnown() bool {
	if m.known {
		return true
	}
	known, err := retryCall("ask whether the account is known", seqFetchAttempts, func() (bool, error) {
		return m.c.IsKnownController(m.wallet.Account.ControllerID())
	})
	if err != nil {
		glb.Verbosef("   cannot ask whether the account is known: %v; taking it as known", err)
		return true
	}
	m.known = known
	return known
}

// activeSequencers is the sequencers active in the LRB state, by ID. The list
// is fetched at most once per seqFetchSlots target slots: a round starts every
// few seconds, and a public node rate-limits the sequencer scan well below that.
func (m *miner) activeSequencers(slot uint32) (map[base.ChainID]txbuildercore.SequencerCandidate, error) {
	if m.seqs != nil && slot >= m.seqsSlot && slot < m.seqsSlot+uint32(m.seqFetchSlots) {
		return m.seqs, nil
	}
	type fetched struct {
		outs  map[base.ChainID]client.SequencerOutput
		lrbID *base.TransactionID
	}
	f, err := retryCall("list the sequencers", seqFetchAttempts, func() (fetched, error) {
		outs, lrbID, err := m.c.GetAllSequencerOutputs()
		return fetched{outs, lrbID}, err
	})
	if err != nil {
		return nil, err
	}
	active, _ := client.SequencerCandidates(f.outs, f.lrbID)
	m.seqs = make(map[base.ChainID]txbuildercore.SequencerCandidate, len(active))
	for _, c := range active {
		m.seqs[c.ID] = c
	}
	m.seqsSlot = slot
	return m.seqs, nil
}

// pickDelegationTarget is the consolidator's target choice with one difference
// for a miner: when the pinned target is inactive or leaves delegators nothing,
// the draw by rating among the active sequencers stands in for it, and
// fellBack says so. The consolidator can wait for a pinned target; a miner
// cannot without losing its hashrate.
func pickDelegationTarget(active []txbuildercore.SequencerCandidate, pinned *base.ChainID, draw func(n int) int) (t txbuildercore.SequencerCandidate, fellBack bool, err error) {
	t, err = consolidator.SelectDelegationTarget(active, pinned, draw)
	if err != nil && pinned != nil {
		if t, err = consolidator.SelectDelegationTarget(active, nil, draw); err == nil {
			fellBack = true
		}
	}
	return t, fellBack, err
}

// ownDelegations is every delegation mastered by this wallet, smallest first:
// the ones in the reliable branch, plus what this miner's unconfirmed transits
// open and top up. The reliable branch settles a transit several slots after
// it is submitted, and a miner winning every slot in between would otherwise
// open one delegation per slot and send every top-up to the same one.
func (m *miner) ownDelegations(slot uint32) ([]*ownDelegation, error) {
	res, err := m.c.GetOutputsForControllerID(m.wallet.Account.ControllerID(), client.GetOutputsParams{
		LockType:   api.GetOutputsLockTypeDelegateMaster,
		Chained:    client.ChainedOnly(),
		MaxOutputs: api.GetOutputsIterationCap,
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
		ret = append(ret, &ownDelegation{view: view, balance: o.Output.TokenBalance(), frozen: view.IsInFrozenSlot(slot, m.consts)})
	}
	ret = withSpeculative(ret, m.tree.speculativePayouts(), m.consts.MineDelegationAmountProx*m.consts.SmallestAmountsPerBaseToken)
	sort.SliceStable(ret, func(i, j int) bool { return ret[i].balance < ret[j].balance })
	return ret, nil
}

// withSpeculative adds to dels the delegations opened by the unconfirmed
// transits in spec, each with the amount a new delegation holds, and credits
// their top-ups to the delegation each names. A pending delegation is not
// frozen yet: its target can take a top-up as soon as the transit that opens
// it is included, which is the same lineage the top-up extends.
func withSpeculative(dels []*ownDelegation, spec []minePayout, newDelegationAmount uint64) []*ownDelegation {
	find := func(id base.ChainID) *ownDelegation {
		for _, d := range dels {
			if d.view.ChainID == id {
				return d
			}
		}
		return nil
	}
	for _, p := range spec {
		switch {
		case p.option == txbuildercore.MinePayoutDelegation:
			if find(p.delegationID) == nil {
				dels = append(dels, &ownDelegation{
					view:    &txbuildercore.DelegationOutputView{ChainID: p.delegationID, Target: p.target, RequiredInflationCut: p.cut},
					balance: newDelegationAmount,
				})
			}
		case p.topUp:
			if d := find(p.delegationID); d != nil {
				d.balance += p.minted
			}
		}
	}
	return dels
}
