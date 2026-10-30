package task

import (
	"sort"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// candidate builds the only two things tagAlongCandidateLess reads: the token
// balance (which for a tag-along output IS the fee) and the output timestamp.
// Both the amounts encoding and the ID are pure — no ledger singleton needed.
func candidate(t *testing.T, fee uint64, tick byte) *_inputCandidate {
	t.Helper()
	ts := base.T(1, tick)
	txid := base.RandomTransactionID(false, 1, ts)
	oid, err := base.NewOutputID(txid, 0)
	require.NoError(t, err)
	return &_inputCandidate{
		o: &ledger.OutputWithID{
			ID:     oid,
			Output: ledger.NewOutput(func(o *ledger.OutputBuilder) { o.WithTokenBalance(fee) }),
		},
	}
}

// The backlog must be consumed biggest-fee-first. The earlier comparator fell
// through to the timestamp whenever the fee comparison failed, which made it
// report both less(a,b) and less(b,a) for a cheap-but-early vs expensive-but-late
// pair — a non-transitive comparator that sort.Slice answers with an arbitrary
// permutation, silently dropping the fee preference.
func TestTagAlongCandidateOrder(t *testing.T) {
	// cheap+early against expensive+late: exactly the pair the old comparator
	// contradicted itself on. Only one direction may hold.
	cheapEarly := candidate(t, 100, 5)
	richLate := candidate(t, 10_000, 9)
	require.True(t, tagAlongCandidateLess(richLate, cheapEarly), "the bigger fee must sort first")
	require.False(t, tagAlongCandidateLess(cheapEarly, richLate), "the comparator must be antisymmetric")

	// equal fees fall back to oldest-first, so a fee tie is served FIFO
	early := candidate(t, 500, 3)
	late := candidate(t, 500, 8)
	require.True(t, tagAlongCandidateLess(early, late))
	require.False(t, tagAlongCandidateLess(late, early))

	// sorting a mixed set puts fees in descending order regardless of the
	// timestamps, which is the property the proposal loop depends on when it
	// stops early at maxTagAlongs.
	outs := []*_inputCandidate{
		candidate(t, 100, 1),
		candidate(t, 10_000, 9),
		candidate(t, 500, 2),
		candidate(t, 10_000, 4),
		candidate(t, 1, 0),
	}
	sort.Slice(outs, func(i, j int) bool { return tagAlongCandidateLess(outs[i], outs[j]) })

	fees := make([]uint64, 0, len(outs))
	for _, o := range outs {
		fees = append(fees, o.o.Output.TokenBalance())
	}
	require.Equal(t, []uint64{10_000, 10_000, 500, 100, 1}, fees)
	// the two equal top fees keep FIFO order among themselves
	require.EqualValues(t, 4, outs[0].o.ID.Timestamp().Tick)
	require.EqualValues(t, 9, outs[1].o.ID.Timestamp().Tick)
}
