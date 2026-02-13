package ledger

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

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
	for _, arg := range args {
		if arg != 0 {
			t.MustPush(easyfl_util.TrimmedLeadingZeroUint64(uint64(arg)))
		}
	}
	ret.Tuple = t.Tuple()
	return
}

func (a Amounts) String() string {
	argsStr := make([]string, a.NumElements())
	err := util.CatchPanicOrError(func() error {
		a.ForEach(func(i int, data []byte) bool {
			v := int64(binary.BigEndian.Uint64(data))
			argsStr[i] = util.Th(v)
			return true
		})
		return nil
	})
	if err != nil {
		return fmt.Sprintf("amount.String(): %v", err)
	}
	return "(" + strings.Join(argsStr, ",") + ")"
}

func (a Amounts) Amount(i byte) (ret int64) {
	if int(i) < a.NumElements() {
		u, err := easyfl_util.Uint64FromBytes(a.MustAt(int(i)))
		util.AssertNoError(err, "amount.Amount()")
		ret = int64(u)
	}
	return
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
	a.ForEach(func(i int, data []byte) bool {
		ret[i] = int64(easyfl_util.MustUint64FromBytes(data))
		return true
	})
	return ret
}

// AddToVector adds int64 amounts from the tuple to vector with safe arithmetics
// Bounds safe, but vector must be longer than the tuple
// Returns false in case of arithmetic overflow, but does not panic
// It is up to the caller to process the overflow
func (a Amounts) AddToVector(vect []int64) (overflow bool) {
	sz := a.NumElements()
	for i := range vect {
		if i >= sz {
			return
		}
		v := int64(easyfl_util.MustUint64FromBytes(a.MustAt(i)))
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
	ret.ForEach(func(i int, d []byte) bool {
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
