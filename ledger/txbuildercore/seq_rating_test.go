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

// Ranks under the three delegation criteria: share descending, balance
// descending, frozen/balance ascending. Equal values share the rank of the
// first of them (1, 2, 2, 4), and the rating is the sum with the share rank
// counted twice. The list comes back best first; equal ratings are ordered
// by chain ID.
func TestRateSequencersDelegation(t *testing.T) {
	c := []SequencerCandidate{
		cand(1, 900, 1000, 500, 0), // share 1st, balance 1st, DE 1/2 -> 3rd
		cand(2, 900, 100, 10, 0),   // share 1st (tie), balance 3rd, DE 1/10 -> 1st
		cand(3, 800, 500, 100, 0),  // share 3rd, balance 2nd, DE 1/5 -> 2nd
	}
	rated := RateSequencers(c, DelegationCriteria)
	require.Len(t, rated, 3)

	byID := map[byte]RatedSequencer{}
	for _, r := range rated {
		byID[last(&r)] = r
	}
	require.Equal(t, []int{1, 1, 3}, byID[1].Ranks)
	require.Equal(t, []int{1, 3, 1}, byID[2].Ranks)
	require.Equal(t, []int{3, 2, 2}, byID[3].Ranks)
	require.Equal(t, 2*1+1+3, byID[1].Rating)
	require.Equal(t, 2*1+3+1, byID[2].Rating)
	require.Equal(t, 2*3+2+2, byID[3].Rating)

	// 1 and 2 tie at 6 and are ordered by chain ID; weights 3, 2, 1
	require.Equal(t, byte(1), last(&rated[0]))
	require.Equal(t, byte(2), last(&rated[1]))
	require.Equal(t, byte(3), last(&rated[2]))
	require.Equal(t, []int{3, 2, 1}, []int{rated[0].Weight, rated[1].Weight, rated[2].Weight})
}

// The tag-along criteria are the minimum fee ascending, counted twice, and
// the balance descending; the share and the frozen coverage play no part.
func TestRateSequencersTagAlong(t *testing.T) {
	c := []SequencerCandidate{
		cand(1, 0, 100, 0, 500),  // fee 2nd, balance 2nd -> 6
		cand(2, 0, 1000, 0, 500), // fee 2nd, balance 1st -> 5
		cand(3, 0, 10, 0, 100),   // fee 1st, balance 3rd -> 5
	}
	rated := RateSequencers(c, TagAlongCriteria)
	// 2 and 3 tie at 5 and are ordered by chain ID
	require.Equal(t, byte(2), last(&rated[0]))
	require.Equal(t, 5, rated[0].Rating)
	require.Equal(t, byte(3), last(&rated[1]))
	require.Equal(t, 5, rated[1].Rating)
	require.Equal(t, byte(1), last(&rated[2]))
	require.Equal(t, 6, rated[2].Rating)
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
