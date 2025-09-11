package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
)

type Amounts []int64

const AmountsConstraintName = "amounts"

const (
	AmountIndexTokenBalance = byte(iota)
	AmountIndexInflation
	AmountIndexFrozenCoverage
)

func NewAmounts(args ...int64) Amounts {
	util.Assertf(len(args) <= 15, "NewAmounts: too many arguments")
	lastNotZero := -1
	for i, arg := range args {
		if arg != 0 {
			lastNotZero = i
		}
	}
	if lastNotZero == -1 {
		return nil
	}
	return slices.Clone(args[:lastNotZero+1])
}

func (a Amounts) Name() string {
	return AmountsConstraintName
}

func (a Amounts) Bytes() []byte {
	return mustBinFromSource(a.Source())
}

func (a Amounts) Source() string {
	if len(a) == 0 {
		return AmountsConstraintName
	}
	argsStr := make([]string, len(a))
	for i, arg := range a {
		if 0 < arg && arg <= 255 {
			// one byte
			argsStr[i] = strconv.FormatInt(arg, 10)
		} else {
			// zero-trimmed 8 uint64 bytes
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(arg))
			trimmed := easyfl_util.TrimLeadingZeroBytes(b[:])
			argsStr[i] = "0x" + hex.EncodeToString(trimmed)
		}
	}
	return AmountsConstraintName + "(" + strings.Join(argsStr, ",") + ")"
}

func (a Amounts) String() string {
	argsStr := make([]string, len(a))
	for i, arg := range a {
		argsStr[i] = util.Th(arg)
	}
	return AmountsConstraintName + "(" + strings.Join(argsStr, ",") + ")"
}

func (a Amounts) Amount(i byte) (ret int64) {
	util.Assertf(i <= 15, "amount index is out of range: %d", i)
	if int(i) < len(a) {
		ret = a[i]
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

func (a Amounts) IsFrozenCoverageZero() bool {
	for i := 0; i < int(Const.MaxFrozenEpochs); i++ {
		if a.Amount(AmountIndexFrozenCoverage+byte(i)) != 0 {
			return false
		}
	}
	return true
}

func (a Amounts) FrozenCoverageAt(i byte) (ret int64) {
	util.Assertf(uint32(i) < Const.MaxFrozenEpochs, "Amounts.FrozenCoverageAt: wrong vector index %d", i)
	return a.Amount(AmountIndexFrozenCoverage + i)
}

func (a Amounts) FrozenCoverageVector() []int64 {
	ret := make([]int64, Const.MaxFrozenEpochs)
	for i := range ret {
		ret[i] = a.FrozenCoverageAt(byte(i))
	}
	return ret
}

// AddToVector adds amounts to vector with safe arithmetics
// Returns false in case of arithmetic overflow, but does not panic
// It is up to the caller to process overflow
func (a Amounts) AddToVector(vect *[15]int64) (overflow bool) {
	for i, v := range a {
		if v >= 0 {
			if vect[i] >= math.MaxInt64-v {
				overflow = true
			}
		} else {
			if vect[i] < math.MinInt64-v {
				overflow = true
			}
		}
		vect[i] += v
	}
	return
}

func AmountsFromBytes(data []byte) (Amounts, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data)
	if err != nil {
		return nil, err
	}
	if sym != AmountsConstraintName {
		return nil, fmt.Errorf("AmountsFromBytes: not 'amounts' constraint")
	}
	ret := make(Amounts, len(args))
	var v uint64
	for i, arg := range args {
		v, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(arg))
		if err != nil {
			return nil, fmt.Errorf("AmountsFromBytes: %w", err)
		}
		ret[i] = int64(v)
	}
	return ret, nil
}

func TokenBalanceFromAmountsBytes(data []byte) (int64, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data)
	if err != nil {
		return 0, err
	}
	if sym != AmountsConstraintName {
		return 0, fmt.Errorf("TokenBalanceFromAmountsBytes: not 'amounts' constraint")
	}
	if len(args) == 0 {
		return 0, nil
	}
	ret, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return 0, fmt.Errorf("TokenBalanceFromAmountsBytes: %w", err)
	}
	if int64(ret) < 0 {
		return 0, fmt.Errorf("TokenBalanceFromAmountsBytes: negative amount")
	}
	return int64(ret), nil
}

func registerAmountsConstraint(lib *Library) {
	lib.mustRegisterVarargConstraint(AmountsConstraintName, func(data []byte) (Constraint, error) {
		return AmountsFromBytes(data)
	}, initTestAmountsConstraint)
}

func initTestAmountsConstraint() {
	example := NewAmounts(1, 2, 1337, 0x01020304050607)

	exampleBack, err := ConstraintFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.Name() == AmountsConstraintName, "inconsistency 1")
	exampleBack1 := exampleBack.(Amounts)
	util.Assertf(len(exampleBack1) == 4, "inconsistency 2")

	util.Assertf(exampleBack1[0] == 1, "exampleBack1[0]==1")
	util.Assertf(exampleBack1[1] == 2, "exampleBack1[1]==2")
	util.Assertf(exampleBack1[2] == 1337, "exampleBack1[2]==1337")
	util.Assertf(exampleBack1[3] == 0x01020304050607, "exampleBack1[3]==0x01020304050607")
}

const amountsAuxSource = `
// $0 - 'amounts'' bytecode
// $1 - index of 'amounts' vector element, 1 byte
func amountAt:
if(
   lessThan($1, parseNumArgs($0)),
   uint8Bytes(parseInlineDataArgument($0, #amounts,$1)),
   u64/0
)

// $0 path to output
func tokenBalanceByOutputPath : amountAt(atPath(concat($0, amountsConstraintIndex)), 0)

func selfTokenBalanceValue: amountAt(selfSiblingConstraint(amountsConstraintIndex),0)
`
