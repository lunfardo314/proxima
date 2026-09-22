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
// weighs double, the others single, since on a network of few large
// sequencers the balance and the frozen-to-balance ratio tend to agree and
// would otherwise outvote the price. The draw is over the list sorted
// by rating, linear in position, so the best is drawn N times as often as the
// worst and nobody eligible is ever left out. Rank sums are scale free: a
// balance in the billions and a share in promille mix without units, and one
// absurd value moves its owner by one rank.

// ActiveSequencerSlots is how many slots older than the LRB the sequencer
// output in the LRB state may be for the sequencer to count as active, i.e.
// worth handing tokens to. Judged on settled milestones, not on what the
// node's tippool has seen: a milestone that never settles serves nobody.
const ActiveSequencerSlots = 5

// SequencerCandidate is what the rating reads off one sequencer output in
// the LRB state.
type SequencerCandidate struct {
	ID   base.ChainID
	Name string
	// Slot of the sequencer output, i.e. of the last settled milestone.
	Slot           uint32
	Balance        uint64
	FrozenCoverage uint64 // cumulative total frozen by the delegations on this sequencer
	// ShareLeft is what the sequencer leaves a delegator of the delegation's
	// inflation, in promille: 1000 minus the sequencer's own cut.
	ShareLeft    uint16
	MinimumFee   uint64 // minimum tag-along fee
	MinimumTopUp uint64 // smallest top-up request taken, the floor MinimumTopUpAmount applied
}

// Active reports whether the candidate's last settled milestone lies within
// ActiveSequencerSlots of the LRB.
func (c *SequencerCandidate) Active(lrbSlot uint32) bool {
	return c.Slot+ActiveSequencerSlots >= lrbSlot
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
// game, and the only stake-weighted component) and, ascending, the
// frozen-to-balance ratio, since a delegator earns the same per token anywhere
// at the same cut and the crowded sequencer has less of its own capital behind
// each delegated token.
var DelegationCriteria = []Criterion{
	{"share left", 2, func(a, b *SequencerCandidate) bool { return a.ShareLeft > b.ShareLeft }},
	{"balance", 1, func(a, b *SequencerCandidate) bool { return a.Balance > b.Balance }},
	{"DE ratio", 1, lessFrozenToBalance},
}

// TagAlongCriteria: the fee the wallet pays on every transaction, and the
// balance.
var TagAlongCriteria = []Criterion{
	{"minimum fee", 2, func(a, b *SequencerCandidate) bool { return a.MinimumFee < b.MinimumFee }},
	{"balance", 1, func(a, b *SequencerCandidate) bool { return a.Balance > b.Balance }},
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
