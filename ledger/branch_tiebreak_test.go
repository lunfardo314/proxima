package ledger

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tie-break term of library upgrade 7: its key orders branches by sigma x u - B, and the score
// term never goes negative and does not change when the baseline's sequencer output is consumed
// (the consumed amount then carries the bonus, which the term takes back out).
func TestTieBreakTerm(t *testing.T) {
	const sigma = 10_000_000 // 10 PROX in motes
	half := uint64(1) << 63  // u = 0.5

	t.Run("sigma x u is exact on the high word", func(t *testing.T) {
		require.EqualValues(t, sigma/2, TieBreakKey(sigma, half, 0)-TieBreakOffset/2)
		require.EqualValues(t, 0, TieBreakKey(sigma, 0, TieBreakOffset/2))
	})
	t.Run("the worked example of the spec", func(t *testing.T) {
		// X asks 0.30 PROX and draws 0.62, Y asks 0.05 and draws 0.55: X ranks first
		x := TieBreakKey(sigma, frac(0.62), 300_000)
		y := TieBreakKey(sigma, frac(0.55), 50_000)
		require.Greater(t, x, y)
		// with a draw above 0.665 Y would have won
		y = TieBreakKey(sigma, frac(0.67), 50_000)
		require.Greater(t, y, x)
	})
	t.Run("consuming the output does not change the score", func(t *testing.T) {
		for _, bonus := range []uint64{1, 200_000, 5_000_000} {
			for _, draw := range []uint64{0, half, math.MaxUint64} {
				unconsumed := TieBreakScoreTerm(sigma, draw, bonus, false)
				consumed := TieBreakScoreTerm(sigma, draw, bonus, true)
				// consumed, the counted amount holds the bonus: delta + term is the same
				require.EqualValues(t, unconsumed, consumed+bonus)
				require.LessOrEqual(t, consumed, unconsumed)
			}
		}
	})
	t.Run("a bonus above the bound counts as the bound", func(t *testing.T) {
		require.EqualValues(t, TieBreakKey(sigma, half, 5_000_000), TieBreakKey(sigma, half, 9_000_000))
	})
}

// frac is U for a draw u in [0, 1)
func frac(u float64) uint64 {
	return uint64(u * float64(math.MaxUint64))
}
