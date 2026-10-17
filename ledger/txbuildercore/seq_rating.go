package txbuildercore

import (
	"bytes"
	"math/bits"
	"sort"

	"github.com/lunfardo314/proxima/ledger/base"
)

// Sequencer rating: how a wallet chooses a delegation or tag-along target
// among the sequencers it does not know. Spec: kb/sequencer_rating.md.
//
// Each criterion sorts the candidates; a candidate's rank under it is its
// 1-based position, equals sharing the rank of the first of them. The rating
// is the weighted sum of the ranks, smaller is better; the price criterion
// weighs as much as all the others together, since they tend to agree with
// each other (on a network of few large sequencers the balance and the
// frozen-to-balance ratio, and always the transit count and the transit rate)
// and would otherwise outvote the price. The draw is over the list sorted
// by rating, linear in position, so the best is drawn N times as often as the
// worst and nobody eligible is ever left out. Rank sums are scale free: a
// balance in the billions and a share in promille mix without units, and one
// absurd value moves its owner by one rank.

// ActiveSequencerSlots is how many slots older than the LRB the sequencer
// output in the LRB state may be for the sequencer to count as active, i.e.
// worth handing tokens to. Judged on settled milestones, not on what the
// node's tippool has seen: a milestone that never settles serves nobody.
const ActiveSequencerSlots = 5

// TransitRateCap is the transit rate, in transits per slot over the chain's
// life, at and above which sequencers rank equal under the transit rate
// criterion. The rate is uptime times milestone frequency, and the frequency
// follows the node's settings and load: capped at one transit per slot, the
// criterion ranks how much of its life a chain was alive, not how often it
// issues milestones. A sequencer running all the time settles more than one
// per slot.
const TransitRateCap = 1

// SequencerCandidate is what the rating reads off one sequencer output in
// the LRB state.
type SequencerCandidate struct {
	ID   base.ChainID
	Name string
	// Slot of the sequencer output, i.e. of the last settled milestone.
	Slot uint32
	// OriginSlot and Transits are the chain constraint's origin slot and
	// transition counter, both enforced by the ledger.
	OriginSlot     uint32
	Transits       uint64
	Balance        uint64
	FrozenCoverage uint64 // cumulative total frozen by the delegations on this sequencer
	// ShareLeft is what the sequencer leaves a delegator of the delegation's
	// inflation, in promille: 1000 minus the sequencer's own cut.
	ShareLeft    uint16
	MinimumFee   uint64 // minimum tag-along fee
	MinimumTopUp uint64 // smallest top-up request taken, the floor MinimumTopUpAmount applied
	// Bootstrap is set when the output was produced by a bootstrap transaction: a milestone
	// on an explicit baseline, which a sequencer issues while it sees no branches. Settled in
	// a branching network it is the sign of a node that is stuck or mis-clocked, not of one
	// at work.
	Bootstrap bool
}

// Active reports whether the candidate's last settled milestone lies within
// ActiveSequencerSlots of the LRB and is not a bootstrap transaction.
func (c *SequencerCandidate) Active(lrbSlot uint32) bool {
	return !c.Bootstrap && c.Slot+ActiveSequencerSlots >= lrbSlot
}

// Criterion orders candidates, best first; its rank enters the rating
// multiplied by Weight.
type Criterion struct {
	Name   string
	Weight int
	// better reports whether a is strictly better than b
	better func(a, b *SequencerCandidate) bool
}

// DelegationCriteria: the share left (the price), the balance (skin in the
// game, and the only stake-weighted component), ascending, the
// frozen-to-balance ratio, since a delegator earns the same per token anywhere
// at the same cut and the crowded sequencer has less of its own capital behind
// each delegated token, and the liveness experience: the transit count and
// the capped transit rate.
var DelegationCriteria = []Criterion{
	{"share left", 4, func(a, b *SequencerCandidate) bool { return a.ShareLeft > b.ShareLeft }},
	{"balance", 1, moreBalance},
	{"DE ratio", 1, lessFrozenToBalance},
	{"transits", 1, moreTransits},
	{"transit rate", 1, higherTransitRate},
}

// TagAlongCriteria: the fee the wallet pays on every transaction, the
// balance and the liveness experience.
var TagAlongCriteria = []Criterion{
	{"minimum fee", 3, func(a, b *SequencerCandidate) bool { return a.MinimumFee < b.MinimumFee }},
	{"balance", 1, moreBalance},
	{"transits", 1, moreTransits},
	{"transit rate", 1, higherTransitRate},
}

func moreBalance(a, b *SequencerCandidate) bool { return a.Balance > b.Balance }

func moreTransits(a, b *SequencerCandidate) bool { return a.Transits > b.Transits }

// LifeSlots is the chain's life span in slots up to the candidate's output,
// both ends counted, so never zero.
func (c *SequencerCandidate) LifeSlots() uint64 {
	if c.Slot < c.OriginSlot {
		return 1
	}
	return uint64(c.Slot-c.OriginSlot) + 1
}

// higherTransitRate compares Transits/LifeSlots capped at TransitRateCap, as
// cross-multiplied fractions in 128 bits, no division. All at the cap are
// equal.
func higherTransitRate(a, b *SequencerCandidate) bool {
	la, lb := a.LifeSlots(), b.LifeSlots()
	capA := a.Transits >= TransitRateCap*la
	capB := b.Transits >= TransitRateCap*lb
	if capA || capB {
		return capA && !capB
	}
	hiA, loA := bits.Mul64(a.Transits, lb)
	hiB, loB := bits.Mul64(b.Transits, la)
	return hiA > hiB || (hiA == hiB && loA > loB)
}

// lessFrozenToBalance compares frozen/balance as cross-multiplied fractions
// in 128 bits, no division. A zero balance is the worst ratio.
func lessFrozenToBalance(a, b *SequencerCandidate) bool {
	switch {
	case a.Balance == 0:
		return false
	case b.Balance == 0:
		return true
	}
	hiA, loA := bits.Mul64(a.FrozenCoverage, b.Balance)
	hiB, loB := bits.Mul64(b.FrozenCoverage, a.Balance)
	return hiA < hiB || (hiA == hiB && loA < loB)
}

// RatedSequencer is a candidate with its ranks, one per criterion in the
// order given, their weighted sum and its draw weight.
type RatedSequencer struct {
	SequencerCandidate
	Ranks  []int
	Rating int
	Weight int
}

// RateSequencers ranks the candidates under each criterion and returns them
// in draw order: rating ascending, ties by chain ID, so every wallet sees the
// same list. The candidate in position p of N carries weight N-p+1.
func RateSequencers(candidates []SequencerCandidate, criteria []Criterion) []RatedSequencer {
	n := len(candidates)
	rated := make([]RatedSequencer, n)
	for i, c := range candidates {
		rated[i] = RatedSequencer{SequencerCandidate: c, Ranks: make([]int, len(criteria))}
	}
	order := make([]int, n)
	for j, crit := range criteria {
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(x, y int) bool {
			return crit.better(&candidates[order[x]], &candidates[order[y]])
		})
		// competition ranking: equals share the rank of the first of them
		for pos, i := range order {
			rank := pos + 1
			if pos > 0 && !crit.better(&candidates[order[pos-1]], &candidates[i]) {
				rank = rated[order[pos-1]].Ranks[j]
			}
			rated[i].Ranks[j] = rank
			rated[i].Rating += rank * crit.Weight
		}
	}
	sort.Slice(rated, func(x, y int) bool {
		if rated[x].Rating != rated[y].Rating {
			return rated[x].Rating < rated[y].Rating
		}
		return bytes.Compare(rated[x].ID[:], rated[y].ID[:]) < 0
	})
	for p := range rated {
		rated[p].Weight = n - p
	}
	return rated
}

// DrawWeightTotal is the sum of the draw weights of a rated list of n.
func DrawWeightTotal(n int) int {
	return n * (n + 1) / 2
}

// DrawSequencer draws one of the rated list in proportion to the weights;
// draw(n) returns a number in [0, n). The list must not be empty.
func DrawSequencer(rated []RatedSequencer, draw func(n int) int) *RatedSequencer {
	r := draw(DrawWeightTotal(len(rated)))
	for i := range rated {
		if r < rated[i].Weight {
			return &rated[i]
		}
		r -= rated[i].Weight
	}
	return &rated[len(rated)-1]
}

// DelegationCandidates keeps the candidates a delegation may go to: those
// leaving a delegator something, and at least minimumCut promille when the
// delegation is built at a fixed cut (a target leaving less would never
// freeze it). A price taker passes 0.
func DelegationCandidates(candidates []SequencerCandidate, minimumCut uint16) []SequencerCandidate {
	ret := make([]SequencerCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.ShareLeft > 0 && c.ShareLeft >= minimumCut {
			ret = append(ret, c)
		}
	}
	return ret
}
