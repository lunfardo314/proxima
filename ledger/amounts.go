package ledger

import (
	_ "embed"
	"fmt"
	"math"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/util"
)

//go:embed def/amounts.easyfl
var amountsSource string

type Amounts struct {
	*tuples.Tuple
}

const (
	AmountIndexTokenBalance = byte(iota)
	AmountIndexInflation
	AmountIndexFrozenCoverage
)

func NewAmounts(args ...int64) (ret Amounts) {
	t := tuples.EmptyTupleEditable(256)
	util.Assertf(len(args) <= 256, "NewAmounts: too many elements")
	// Drop every trailing cell the decoding rule reconstructs on its own, so a
	// frozen-coverage vector that is constant to its tail costs one cell instead
	// of one per epoch. See amountAt in amounts.easyfl.
	n := len(args)
	for n > 0 && args[n-1] == impliedAmountPastEnd(args, n-1) {
		n--
	}
	for i := 0; i < n; i++ {
		t.MustPush(easyfl_util.TrimmedLeadingZeroUint64(uint64(args[i])))
	}
	ret.Tuple = t.Tuple()
	return
}

// impliedAmountPastEnd is what a tuple holding only args[:n] decodes to at index
// n: 0 while it does not reach the frozen-coverage region, otherwise its last
// cell. Mirrors the past-the-end branch of amountAt.
func impliedAmountPastEnd(args []int64, n int) int64 {
	if n <= int(AmountIndexFrozenCoverage) {
		return 0
	}
	return args[n-1]
}

// String renders the amounts vector with the "_" thousands separator. Elements
// are joined with "," by default; an optional element separator (e.g. ", ")
// overrides the join — used by the human-facing output decoders.
func (a Amounts) String(elementSeparator ...string) string {
	join := ","
	if len(elementSeparator) > 0 {
		join = elementSeparator[0]
	}
	argsStr := make([]string, a.NumElements())
	err := util.CatchPanicOrError(func() error {
		a.ForEach(func(i int, data []byte) bool {
			v := int64(easyfl_util.MustUint64FromBytes(data))
			argsStr[i] = util.Th(v)
			return true
		})
		return nil
	})
	if err != nil {
		return fmt.Sprintf("amount.String(): %v", err)
	}
	return "(" + strings.Join(argsStr, join) + ")"
}

func (a Amounts) Amount(i byte) (ret int64) {
	n := a.NumElements()
	idx := int(i)
	if idx >= n {
		// past the end: 0 below the frozen-coverage region, last cell from there
		// on. Mirrors amountAt in amounts.easyfl.
		if n <= int(AmountIndexFrozenCoverage) {
			return 0
		}
		idx = n - 1
	}
	u, err := easyfl_util.Uint64FromBytes(a.MustAt(idx))
	util.AssertNoError(err, "amount.Amount()")
	return int64(u)
}

func (a Amounts) TokenBalance() uint64 {
	ret := a.Amount(AmountIndexTokenBalance)
	util.Assertf(ret >= 0, "token balance can't be negative. Got %d", a)
	return uint64(ret)
}

func (a Amounts) InflationAmount() uint64 {
	ret := a.Amount(AmountIndexInflation)
	util.Assertf(ret >= 0, "inflation amount must can't be negative. Got %d", a)
	return uint64(ret)
}

func (a Amounts) IsFrozenCoverageZero(maxFrozenEpochs byte) bool {
	for i := 0; i < int(maxFrozenEpochs); i++ {
		if a.Amount(AmountIndexFrozenCoverage+byte(i)) != 0 {
			return false
		}
	}
	return true
}

func (a Amounts) FrozenCoverageAt(i byte) (ret int64) {
	return a.Amount(AmountIndexFrozenCoverage + i)
}

func (a Amounts) FrozenCoverageVector(maxFrozenEpochs byte) []int64 {
	ret := make([]int64, maxFrozenEpochs)
	for i := byte(0); i < maxFrozenEpochs; i++ {
		ret[i] = a.Amount(AmountIndexFrozenCoverage + i)
	}
	return ret
}

// AddToVector adds int64 amounts from the tuple to vector with safe arithmetics
// Bounds safe, but vector must be longer than the tuple
// Returns false in case of arithmetic overflow, but does not panic
// It is up to the caller to process the overflow
// Reads through Amount so the repeating tail of a compressed frozen-coverage
// vector is added at every index, not only where the tuple has a cell.
func (a Amounts) AddToVector(vect []int64) (overflow bool) {
	for i := range vect {
		if i > math.MaxUint8 {
			return
		}
		v := a.Amount(byte(i))
		if overflowThreshold := math.MaxInt64 - v; v >= 0 {
			if vect[i] >= overflowThreshold {
				overflow = true
			}
		} else {
			if vect[i] < overflowThreshold {
				overflow = true
			}
		}
		vect[i] += v
	}
	return
}

// AmountsFromBytes parses an Amounts constraint using the provided library.
// Serde is Library upgrade-invariant
func AmountsFromBytes(data []byte) (ret Amounts, err error) {
	var r *tuples.Tuple
	if r, err = tuples.TupleFromBytes(data, 256); err != nil {
		return Amounts{}, fmt.Errorf("AmountsFromBytes: %v", err)
	}
	r.ForEach(func(i int, d []byte) bool {
		if _, err = easyfl_util.Uint64FromBytes(d); err != nil {
			err = fmt.Errorf("AmountsFromBytes: wrong data at index %d: %v", i, err)
			return false
		}
		return true
	})
	if err == nil {
		ret.Tuple = r
	}
	return
}

// TokenBalanceFromAmountsBytes parses the token balance using
func TokenBalanceFromAmountsBytes(data []byte) (uint64, error) {
	a, err := AmountsFromBytes(data)
	if err != nil {
		return 0, err
	}
	return a.TokenBalance(), nil
}

func evalTotalConsumed(par *easyfl.CallParams[*EvalContext]) []byte {
	idxBin := par.Arg(0)
	ret := easyfl_util.Uint64To8Bytes(uint64(par.DataContext().ConsumedTotal(idxBin[0])))
	return par.AllocData(ret[:]...)
}

func evalTotalProduced(par *easyfl.CallParams[*EvalContext]) []byte {
	idxBin := par.Arg(0)
	ret := easyfl_util.Uint64To8Bytes(uint64(par.DataContext().ProducedTotal(idxBin[0])))
	return par.AllocData(ret[:]...)
}

func evalIsInflationAndFrozenCoverageZero(par *easyfl.CallParams[*EvalContext]) []byte {
	ctx := par.DataContext()
	o := ctx.SelfOutput()

	amounts := o.Amounts()
	// NewAmounts trims trailing zeros, so the tuple has no
	// frozen-coverage cells iff NumElements <= AmountIndexFrozenCoverage.
	// Combined with inflation == 0, that means the only non-zero amount
	// (if any) is the token balance. Per-chain maxFrozenEpochs is not
	// needed for this check.
	if amounts.NumElements() <= int(AmountIndexFrozenCoverage) &&
		(amounts.NumElements() <= int(AmountIndexInflation) || amounts.InflationAmount() == 0) {
		return par.AllocData(0xff)
	}
	return nil
}
