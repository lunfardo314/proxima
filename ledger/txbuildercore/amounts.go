package txbuildercore

import (
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
)

// AmountIndex* are the slot indices inside the amounts vector at
// output element index 0. Trailing zero slots are elided on the wire.
const (
	AmountIndexTokenBalance   = byte(0)
	AmountIndexInflation      = byte(1)
	AmountIndexFrozenCoverage = byte(2)
)

// EncodeAmounts serialises a list of amount values into the wire form
// of the amounts vector (output slot 0). Each value is encoded as a
// trimmed-leading-zero big-endian uint64. Trailing zeros are elided —
// e.g. EncodeAmounts(100) and EncodeAmounts(100, 0, 0) produce the
// same bytes.
//
// Slot 0 is the token balance; slot 1 is the inflation; slots 2+ are
// frozen-coverage epochs. Wallets typically only need
// EncodeTokenBalance for sigLock-style outputs.
func EncodeAmounts(args ...uint64) []byte {
	// Find the last non-zero so trailing zero slots are skipped.
	lastNonZero := -1
	for i := len(args) - 1; i >= 0; i-- {
		if args[i] != 0 {
			lastNonZero = i
			break
		}
	}
	t := tuples.EmptyTupleEditable(MaxNumConstraints)
	for i := 0; i <= lastNonZero; i++ {
		t.MustPush(easyfl_util.TrimmedLeadingZeroUint64(args[i]))
	}
	return t.Tuple().Bytes()
}

// EncodeTokenBalance is the common-case sugar for "this output has
// only a token balance, no inflation or frozen coverage".
func EncodeTokenBalance(balance uint64) []byte {
	return EncodeAmounts(balance)
}

// DecodeAmountsVector is the inverse of EncodeAmounts: parse the
// serialised amounts vector (output slot 0) back to its uint64 slots.
// Empty bytes → empty slice (an all-zero / elided vector). Each element
// is a trimmed big-endian uint64.
func DecodeAmountsVector(data []byte) ([]uint64, error) {
	if len(data) == 0 {
		return nil, nil
	}
	t, err := tuples.TupleFromBytes(data, MaxNumConstraints)
	if err != nil {
		return nil, err
	}
	ret := make([]uint64, 0, t.NumElements())
	var perr error
	t.ForEach(func(_ int, v []byte) bool {
		n, e := easyfl_util.Uint64FromBytes(v)
		if e != nil {
			perr = e
			return false
		}
		ret = append(ret, n)
		return true
	})
	return ret, perr
}

// DecodeTokenBalance returns the token balance (amounts slot 0) of a
// serialised output. Zero when the amounts vector is empty. The wallet
// uses this to total consumed inputs and compute the change output.
func DecodeTokenBalance(outputBytes []byte) (uint64, error) {
	o, err := OutputFromBytes(outputBytes)
	if err != nil {
		return 0, err
	}
	amountsBin, err := o.ConstraintAt(ConstraintIndexAmounts)
	if err != nil {
		return 0, err
	}
	vec, err := DecodeAmountsVector(amountsBin)
	if err != nil || len(vec) == 0 {
		return 0, err
	}
	return vec[AmountIndexTokenBalance], nil
}
