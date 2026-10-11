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
// the smallest.

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

// choosePayout picks the layout of the transit stamped at succSlot:
//
//  1. the account is not known on the ledger yet: the opening, which is how
//     it becomes known; only this option passes the node's unknown-sender gate;
//  2. an own sequencer is configured and active: the full amount to it;
//  3. an own delegation below the target size whose target can take a top-up:
//     the full amount as a top-up request to the smallest such delegation;
//  4. fewer delegations than the target count: a new delegation to a target
//     drawn by the delegation rating;
//  5. otherwise a top-up of the smallest delegation that can take one, then a
//     new delegation, then the opening rather than nothing.
//
// A node that cannot be read leaves the opening as the answer: it is always
// valid and costs this round's reward, not the round.
func (m *miner) choosePayout(succSlot uint32) minePayout {
	a := m.consts.MineAmountAtSlot(succSlot)
	fee := m.consts.MineTagAlongFee
	opening := func(why string) minePayout {
		return minePayout{
			option: txbuildercore.MinePayoutOpening,
			minted: m.consts.MineOpeningAmountProx*m.consts.SmallestAmountsPerBaseToken + fee,
			target: m.tagAlongSeqID,
			why:    fmt.Sprintf("%s PROX to the account (%s)", util.Th(m.consts.MineOpeningAmountProx), why),
		}
	}
	if !m.accountKnown() {
		return opening("the account is not known on the ledger yet")
	}
	active, err := m.activeSequencers()
	if err != nil {
		glb.Verbosef("   cannot list the sequencers: %v", err)
		return opening("the sequencers cannot be read")
	}
	if m.cfg.SendOwn && m.cfg.SendTo != nil {
		if _, ok := active[*m.cfg.SendTo]; ok {
			return minePayout{option: txbuildercore.MinePayoutFull, minted: a, target: *m.cfg.SendTo,
				why: "full reward to own sequencer " + m.cfg.SendTo.StringShort()}
		}
		glb.Verbosef("   own sequencer %s is not active; delegating instead", m.cfg.SendTo.StringShort())
	}
	dels, err := m.ownDelegations(succSlot)
	if err != nil {
		glb.Verbosef("   cannot list own delegations: %v", err)
		return opening("the delegations cannot be read")
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
		candidates := make([]txbuildercore.SequencerCandidate, 0, len(active))
		for _, c := range active {
			candidates = append(candidates, c)
		}
		t, fellBack, err := pickDelegationTarget(candidates, m.cfg.DelegateTo, rand.Intn)
		if err != nil {
			glb.Verbosef("   no target for a new delegation: %v", err)
			return minePayout{}, false
		}
		if fellBack != m.pinnedDown {
			m.pinnedDown = fellBack
			if fellBack {
				glb.Infof("   delegation target %s is unavailable; drawing by rating until it is back", m.cfg.DelegateTo.StringShort())
			} else {
				glb.Infof("   delegation target %s is back", m.cfg.DelegateTo.StringShort())
			}
		}
		return minePayout{
			option: txbuildercore.MinePayoutDelegation,
			minted: m.consts.MineDelegationAmountProx*m.consts.SmallestAmountsPerBaseToken + fee,
			target: t.ID, cut: t.ShareLeft,
			why: fmt.Sprintf("%s PROX into a new delegation at sequencer %s leaving %d promille (%d of %d)",
				util.Th(m.consts.MineDelegationAmountProx), t.ID.StringShort(), t.ShareLeft, len(dels)+1, m.cfg.TargetDelegations),
		}, true
	}
	for _, d := range dels {
		if d.balance < m.cfg.TargetSize && canTopUp(d) {
			return topUp(d)
		}
	}
	if len(dels) < m.cfg.TargetDelegations {
		if p, ok := newDelegation(); ok {
			return p
		}
	}
	for _, d := range dels {
		if canTopUp(d) {
			return topUp(d)
		}
	}
	if p, ok := newDelegation(); ok {
		return p
	}
	return opening("no delegation can take a top-up and no sequencer leaves delegators anything")
}

// fullPayoutTo is the full option as a plain tag-along to the sequencer: what
// an own sequencer gets, and what the tests build.
func (m *miner) fullPayoutTo(target base.ChainID, succSlot uint32) minePayout {
	return minePayout{option: txbuildercore.MinePayoutFull, minted: m.consts.MineAmountAtSlot(succSlot), target: target, why: "full reward to sequencer " + target.StringShort()}
}

// accountKnown reports whether the ledger knows this wallet's holder, which
// the first confirmed transit makes true for good.
func (m *miner) accountKnown() bool {
	if m.known {
		return true
	}
	known, err := m.c.IsKnownController(m.wallet.Account.ControllerID())
	if err != nil {
		glb.Verbosef("   cannot ask whether the account is known: %v", err)
		return false
	}
	m.known = known
	return known
}

// activeSequencers is the sequencers active in the LRB state, by ID.
func (m *miner) activeSequencers() (map[base.ChainID]txbuildercore.SequencerCandidate, error) {
	outs, lrbID, err := m.c.GetAllSequencerOutputs()
	if err != nil {
		return nil, err
	}
	active, _ := client.SequencerCandidates(outs, lrbID)
	ret := make(map[base.ChainID]txbuildercore.SequencerCandidate, len(active))
	for _, c := range active {
		ret[c.ID] = c
	}
	return ret, nil
}

// pickDelegationTarget is the consolidator's target choice with one difference
// for a miner: when the pinned target is inactive or leaves delegators nothing,
// the draw by rating among the active sequencers stands in for it, and
// fellBack says so. The consolidator can wait for a pinned target; a miner
// cannot, since the alternative in that round is the opening, which forgoes
// most of the reward.
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
