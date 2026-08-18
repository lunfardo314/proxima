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
	// AmountIndexFrozenCoverageBound holds the number of epochs the
	// frozen-coverage cells cover; past it the coverage is 0. It is a bound,
	// not an amount: it never takes part in the per-index sums (see
	// VectorElement).
	AmountIndexFrozenCoverageBound
	AmountIndexFrozenCoverage
)

// NewAmounts builds the amounts vector from its values: token balance,
// inflation, the frozen-coverage bound and then one value per frozen epoch.
// The bound is derived from the frozen-coverage values, so callers leave that
// cell alone (or pass the matching value); nothing else about the vector may
// be inconsistent with it.
//
// Frozen coverage is constant over a delegation's frozen span, so the encoding
// keeps only the first cell of the run and the bound that ends it: a delegation
// costs a couple of cells instead of one per epoch, whatever the span. See
// frozenCoverageAt in amounts.easyfl.
func NewAmounts(args ...int64) (ret Amounts) {
	util.Assertf(len(args) <= 256, "NewAmounts: too many elements")

	bound := 0
	for i := len(args) - 1; i >= int(AmountIndexFrozenCoverage); i-- {
		if args[i] != 0 {
			bound = i - int(AmountIndexFrozenCoverage) + 1
			break
		}
	}
	v := make([]int64, len(args))
	copy(v, args)

	if bound == 0 {
		if len(v) > int(AmountIndexFrozenCoverageBound) {
			v = v[:AmountIndexFrozenCoverageBound]
		}
		for len(v) > 0 && v[len(v)-1] == 0 {
			v = v[:len(v)-1]
		}
	} else {
		util.Assertf(v[AmountIndexFrozenCoverageBound] == 0 || v[AmountIndexFrozenCoverageBound] == int64(bound),
			"NewAmounts: frozen-coverage bound %d does not match the frozen-coverage values (%d)",
			v[AmountIndexFrozenCoverageBound], bound)
		v[AmountIndexFrozenCoverageBound] = int64(bound)
		v = v[:int(AmountIndexFrozenCoverage)+bound]
		// collapse the run: every frozen-coverage cell the decoder repeats on its own
		for len(v) > int(AmountIndexFrozenCoverage)+1 && v[len(v)-1] == v[len(v)-2] {
			v = v[:len(v)-1]
		}
	}

	t := tuples.EmptyTupleEditable(256)
	for _, a := range v {
		t.MustPush(easyfl_util.TrimmedLeadingZeroUint64(uint64(a)))
	}
	ret.Tuple = t.Tuple()
	return
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
			if i == int(AmountIndexFrozenCoverageBound) {
				// a bound, not an amount: mark it so the rendering cannot be
				// mistaken for a frozen-coverage value
				argsStr[i] = fmt.Sprintf("bound(%d)", v)
			} else {
				argsStr[i] = util.Th(v)
			}
			return true
		})
		return nil
	})
	if err != nil {
		return fmt.Sprintf("amount.String(): %v", err)
	}
	return "(" + strings.Join(argsStr, join) + ")"
}

// Amount is the raw cell at index i: 0 past the end of the tuple. Reading the
// frozen-coverage region needs FrozenCoverageAt, which applies the bound and
// the repeating run.
func (a Amounts) Amount(i byte) (ret int64) {
	if int(i) >= a.NumElements() {
		return 0
	}
	u, err := easyfl_util.Uint64FromBytes(a.MustAt(int(i)))
	util.AssertNoError(err, "amount.Amount()")
	return int64(u)
}

// VectorElement is the value at index i as it takes part in the per-index sums
// over consumed and produced outputs. The frozen-coverage bound is a bound, not
// an amount, so it contributes nothing.
func (a Amounts) VectorElement(i byte) int64 {
	switch {
	case i == AmountIndexFrozenCoverageBound:
		return 0
	case i >= AmountIndexFrozenCoverage:
		return a.FrozenCoverageAt(i - AmountIndexFrozenCoverage)
	}
	return a.Amount(i)
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

// FrozenCoverageBound is the number of epochs covered by the frozen-coverage
// cells. 0 means the output carries no frozen coverage at all.
func (a Amounts) FrozenCoverageBound() int {
	return int(a.Amount(AmountIndexFrozenCoverageBound))
}

func (a Amounts) IsFrozenCoverageZero() bool {
	return a.FrozenCoverageBound() == 0
}

// FrozenCoverageAt is the frozen coverage at epoch offset i: 0 from the bound
// on, otherwise the cell of the epoch or, where the encoder collapsed the run,
// the last cell of the tuple.
func (a Amounts) FrozenCoverageAt(i byte) (ret int64) {
	n := a.NumElements()
	if int(i) >= a.FrozenCoverageBound() || n <= int(AmountIndexFrozenCoverage) {
		return 0
	}
	idx := int(AmountIndexFrozenCoverage) + int(i)
	if idx >= n {
		idx = n - 1
	}
	u, err := easyfl_util.Uint64FromBytes(a.MustAt(idx))
	util.AssertNoError(err, "amount.FrozenCoverageAt()")
	return int64(u)
}

func (a Amounts) FrozenCoverageVector(maxFrozenEpochs byte) []int64 {
	ret := make([]int64, maxFrozenEpochs)
	for i := byte(0); i < maxFrozenEpochs; i++ {
		ret[i] = a.FrozenCoverageAt(i)
	}
	return ret
}

// AddToVector adds int64 amounts from the tuple to vector with safe arithmetics
// Bounds safe, but vector must be longer than the tuple
// Returns false in case of arithmetic overflow, but does not panic
// It is up to the caller to process the overflow
// Reads through VectorElement so the collapsed run of a frozen-coverage vector
// is added at every epoch it covers, not only where the tuple has a cell.
func (a Amounts) AddToVector(vect []int64) (overflow bool) {
	for i := range vect {
		if i > math.MaxUint8 {
			return
		}
		v := a.VectorElement(byte(i))
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
	// A zero bound means no frozen coverage at any epoch, so together with
	// inflation == 0 the only non-zero amount (if any) is the token balance.
	// Reads the inflation cell raw: a negative one is not zero, and this
	// predicate must answer for any output, not assert about it.
	if amounts.IsFrozenCoverageZero() && amounts.Amount(AmountIndexInflation) == 0 {
		return par.AllocData(0xff)
	}
	return nil
}
