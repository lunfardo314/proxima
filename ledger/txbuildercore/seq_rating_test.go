package txbuildercore

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

func cand(idByte byte, share uint16, balance, frozen, fee uint64) SequencerCandidate {
	id := base.ChainID{}
	id[len(id)-1] = idByte
	return SequencerCandidate{ID: id, ShareLeft: share, Balance: balance, FrozenCoverage: frozen, MinimumFee: fee}
}

func last(r *RatedSequencer) byte { return r.ID[len(r.ID)-1] }

// withLife sets the chain's life: the output in slot 1000, the origin life-1
// slots earlier, and the transit count.
func withLife(c SequencerCandidate, life uint32, transits uint64) SequencerCandidate {
	c.Slot = 1000
	c.OriginSlot = c.Slot - life + 1
	c.Transits = transits
	return c
}

// Ranks under the five delegation criteria: share descending, balance
// descending, frozen/balance ascending, transits descending and the transit
// rate capped at the median descending. Equal values share the rank of
// the first of them (1, 2, 2, 4), and the rating is the sum with the share
// rank counted four times. The list comes back best first.
func TestRateSequencersDelegation(t *testing.T) {
	c := []SequencerCandidate{
		withLife(cand(1, 900, 1000, 500, 0), 100, 100), // share 1, balance 1, DE 1/2 -> 3, transits 2, rate 1, the median -> 1
		withLife(cand(2, 900, 100, 10, 0), 600, 300),   // share 1, balance 3, DE 1/10 -> 1, transits 1, rate 1/2 -> 3
		withLife(cand(3, 800, 500, 100, 0), 40, 50),    // share 3, balance 2, DE 1/5 -> 2, transits 3, rate 5/4, above the median -> 1
	}
	rated := RateSequencers(c, DelegationCriteria)
	require.Len(t, rated, 3)

	byID := map[byte]RatedSequencer{}
	for _, r := range rated {
		byID[last(&r)] = r
	}
	require.Equal(t, []int{1, 1, 3, 2, 1}, byID[1].Ranks)
	require.Equal(t, []int{1, 3, 1, 1, 3}, byID[2].Ranks)
	require.Equal(t, []int{3, 2, 2, 3, 1}, byID[3].Ranks)
	require.Equal(t, 4*1+1+3+2+1, byID[1].Rating)
	require.Equal(t, 4*1+3+1+1+3, byID[2].Rating)
	require.Equal(t, 4*3+2+2+3+1, byID[3].Rating)

	require.Equal(t, byte(1), last(&rated[0]))
	require.Equal(t, byte(2), last(&rated[1]))
	require.Equal(t, byte(3), last(&rated[2]))
	require.Equal(t, []int{3, 2, 1}, []int{rated[0].Weight, rated[1].Weight, rated[2].Weight})
}

// The tag-along criteria are the minimum fee ascending, counted three times,
// the balance, the transits and the transit rate; the share and the frozen
// coverage play no part.
func TestRateSequencersTagAlong(t *testing.T) {
	c := []SequencerCandidate{
		withLife(cand(1, 0, 100, 0, 500), 10, 10),    // fee 2, balance 2, transits 3, rate 1, the median -> 12
		withLife(cand(2, 0, 1000, 0, 500), 400, 200), // fee 2, balance 1, transits 1, rate 1/2 -> 11
		withLife(cand(3, 0, 10, 0, 100), 100, 100),   // fee 1, balance 3, transits 2, rate 1, the median -> 9
	}
	rated := RateSequencers(c, TagAlongCriteria)
	require.Equal(t, byte(3), last(&rated[0]))
	require.Equal(t, 9, rated[0].Rating)
	require.Equal(t, byte(2), last(&rated[1]))
	require.Equal(t, 11, rated[1].Rating)
	require.Equal(t, byte(1), last(&rated[2]))
	require.Equal(t, 12, rated[2].Rating)
}

// The transit rate is compared exactly, and a chain whose output and origin
// share a slot has a life of one slot, not zero.
func TestTransitRateCompare(t *testing.T) {
	half := withLife(cand(1, 0, 0, 0, 0), 1000, 500)
	third := withLife(cand(2, 0, 0, 0, 0), 999, 333)
	require.True(t, higherTransitRate(&half, &third))
	require.False(t, higherTransitRate(&third, &half))
	require.False(t, higherTransitRate(&half, &half))

	fresh := withLife(cand(3, 0, 0, 0, 0), 1, 0)
	require.Equal(t, uint64(1), fresh.LifeSlots())
	require.True(t, higherTransitRate(&third, &fresh))
}

// Under the transit rate, capped at the median, every candidate at least as
// good as the median one (position (n-1)/2 of the best-first order) ranks 1,
// however far above it; below the median the ranks continue by position,
// equals sharing a rank as usual.
func TestTransitRateCappedAtMedian(t *testing.T) {
	rateOnly := []Criterion{{"transit rate", 1, higherTransitRate, true}}
	c := []SequencerCandidate{
		withLife(cand(1, 0, 0, 0, 0), 100, 4000), // 40 per slot, pace turned down
		withLife(cand(2, 0, 0, 0, 0), 100, 900),  // 9
		withLife(cand(3, 0, 0, 0, 0), 100, 800),  // 8, the median of five
		withLife(cand(4, 0, 0, 0, 0), 100, 400),  // 4, down half its life
		withLife(cand(5, 0, 0, 0, 0), 100, 400),  // 4
	}
	byID := map[byte]int{}
	for _, r := range RateSequencers(c, rateOnly) {
		byID[last(&r)] = r.Ranks[0]
	}
	require.Equal(t, map[byte]int{1: 1, 2: 1, 3: 1, 4: 4, 5: 4}, byID)

	// of two, the median is the better one: the other still ranks below it
	two := RateSequencers(c[2:4], rateOnly)
	require.Equal(t, []int{1, 2}, []int{two[0].Ranks[0], two[1].Ranks[0]})
}

// The frozen-to-balance comparison is exact on products that overflow 64
// bits, and a zero balance is the worst ratio, behind any candidate with a
// balance, however much is frozen on it.
func TestFrozenToBalanceCompare(t *testing.T) {
	big := uint64(1) << 62
	a := cand(1, 0, big, big-1, 0)   // just under 1
	b := cand(2, 0, big-1, big-1, 0) // exactly 1
	require.True(t, lessFrozenToBalance(&a, &b))
	require.False(t, lessFrozenToBalance(&b, &a))
	require.False(t, lessFrozenToBalance(&a, &a))

	zero := cand(3, 0, 0, 0, 0)
	full := cand(4, 0, 1, 1<<63, 0)
	require.True(t, lessFrozenToBalance(&full, &zero))
	require.False(t, lessFrozenToBalance(&zero, &full))
	require.False(t, lessFrozenToBalance(&zero, &zero))
}

// The draw is linear in position: of N(N+1)/2 tickets the best holds N, the
// worst one. Every ticket lands on exactly one candidate.
func TestDrawSequencer(t *testing.T) {
	c := []SequencerCandidate{cand(1, 900, 3, 0, 0), cand(2, 900, 2, 0, 0), cand(3, 900, 1, 0, 0)}
	rated := RateSequencers(c, DelegationCriteria)
	require.Equal(t, 6, DrawWeightTotal(len(rated)))

	hits := map[byte]int{}
	for r := 0; r < 6; r++ {
		got := DrawSequencer(rated, func(n int) int {
			require.Equal(t, 6, n)
			return r
		})
		hits[last(got)]++
	}
	require.Equal(t, map[byte]int{1: 3, 2: 2, 3: 1}, hits)

	single := RateSequencers(c[:1], DelegationCriteria)
	require.Equal(t, byte(1), last(DrawSequencer(single, func(n int) int { return 0 })))
}

// A delegation candidate must leave something and at least the wallet's
// floor; activity is measured from the LRB slot.
func TestDelegationCandidatesAndActivity(t *testing.T) {
	c := []SequencerCandidate{cand(1, 0, 1, 0, 0), cand(2, 500, 1, 0, 0), cand(3, 900, 1, 0, 0)}
	require.Len(t, DelegationCandidates(c, 0), 2)
	require.Len(t, DelegationCandidates(c, 900), 1)
	require.Len(t, DelegationCandidates(c, 901), 0)

	s := SequencerCandidate{Slot: 100}
	require.True(t, s.Active(100))
	require.True(t, s.Active(100+ActiveSequencerSlots))
	require.False(t, s.Active(101+ActiveSequencerSlots))
}
