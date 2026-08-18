package tests

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/rand"
)

// The frozen-coverage read rule exists twice: in Go (ledger.Amounts) for the
// builders and the embedded validators, and in EasyFL (frozenCoverageAt) for
// the constraints that read another output's coverage. The two must agree cell
// for cell - a mismatch would let a transaction pass one side and fail the
// other - so this walks a range of encodings and compares them directly,
// including the epochs past the bound and past the last encoded cell, which is
// exactly where the encoding does its work.
func TestFrozenCoverageEasyFLMirrorsGo(t *testing.T) {
	maxFrozenEpochs := int(ledger.L(0).DelegationMaxFrozenEpochs)

	// (balance, inflation, how many epochs of coverage, coverage value)
	testCases := []struct {
		name     string
		balance  int64
		coverage []int64
	}{
		{"no coverage", 1_000_000, nil},
		{"one epoch", 1_000_000, repeatInt64(777, 1)},
		{"partial span", 1_000_000, repeatInt64(777, 10)},
		{"full span", 1_000_000, repeatInt64(777, maxFrozenEpochs)},
		{"negative deltas", 1_000_000, repeatInt64(-777, 23)},
		// a sequencer aggregate: values differ per epoch, so the encoder can
		// collapse only the tail of the run
		{"staircase", 1_000_000, []int64{900, 900, 800, 800, 700}},
		// a zero inside the span is a value, not the end of the span
		{"gap inside the span", 1_000_000, []int64{900, 0, 700}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]int64{tc.balance, 42, 0}, tc.coverage...)
			a := ledger.NewAmounts(args...)
			amountsHex := hex.EncodeToString(a.Bytes())

			require.EqualValues(t, len(tc.coverage), a.FrozenCoverageBound())

			// one epoch past the end of the vector too: both sides must say 0
			for i := 0; i <= maxFrozenEpochs; i++ {
				src := fmt.Sprintf("frozenCoverageAt(0x%s, %d)", amountsHex, i)
				resBin, err := ledger.L(0).EvalFromSource(nil, src)
				require.NoError(t, err)
				require.EqualValues(t, 8, len(resBin), "frozenCoverageAt must return a uint64")

				require.EqualValues(t, uint64(a.FrozenCoverageAt(byte(i))), binary.BigEndian.Uint64(resBin),
					"frozen coverage at epoch %d", i)
			}

			// the plain cells must agree as well: the bound sits between them and
			// the coverage region, so an off-by-one there would move the balance
			for i, want := range []int64{tc.balance, 42, int64(len(tc.coverage))} {
				src := fmt.Sprintf("amountAt(0x%s, %d)", amountsHex, i)
				resBin, err := ledger.L(0).EvalFromSource(nil, src)
				require.NoError(t, err)
				require.EqualValues(t, uint64(want), binary.BigEndian.Uint64(resBin), "amount at %d", i)
			}
		})
	}
}

// Random encodings, to cover shapes the table above does not enumerate: any
// vector must survive the round trip through the encoder and read back the same
// on both sides.
func TestFrozenCoverageEasyFLMirrorsGoRandom(t *testing.T) {
	maxFrozenEpochs := int(ledger.L(0).DelegationMaxFrozenEpochs)
	rnd := rand.New(rand.NewSource(31337))

	for round := 0; round < 20; round++ {
		coverage := make([]int64, rnd.Intn(maxFrozenEpochs+1))
		for i := range coverage {
			// few distinct values, so runs of equal cells occur often
			coverage[i] = int64(rnd.Intn(3)-1) * 1_000
		}
		a := ledger.NewAmounts(append([]int64{1_000_000, 0, 0}, coverage...)...)
		amountsHex := hex.EncodeToString(a.Bytes())

		for i := 0; i < maxFrozenEpochs; i++ {
			want := int64(0)
			if i < len(coverage) {
				want = coverage[i]
			}
			// trailing zeros are outside the bound, so they read as 0 either way
			require.EqualValues(t, want, a.FrozenCoverageAt(byte(i)), "round %d, epoch %d (Go)", round, i)

			resBin, err := ledger.L(0).EvalFromSource(nil, fmt.Sprintf("frozenCoverageAt(0x%s, %d)", amountsHex, i))
			require.NoError(t, err)
			require.EqualValues(t, uint64(want), binary.BigEndian.Uint64(resBin), "round %d, epoch %d (EasyFL)", round, i)
		}
	}
}

func repeatInt64(v int64, n int) []int64 {
	ret := make([]int64, n)
	for i := range ret {
		ret[i] = v
	}
	return ret
}
