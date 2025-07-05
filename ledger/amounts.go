package ledger

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
)

type Amounts []uint64

const (
	AmountsName = "amounts"
)

func NewAmounts(args ...uint64) (Amounts, error) {
	if len(args) > 15 {
		return nil, fmt.Errorf("NewAmounts: too many arguments")
	}
	return slices.Clone(args), nil
}

func (a Amounts) Name() string {
	return AmountsName
}

func (a Amounts) Bytes() []byte {
	return mustBinFromSource(a.Source())
}

func (a Amounts) Source() string {
	argsStr := make([]string, len(a))
	for i, arg := range a {
		if arg == 0 {
			argsStr[i] = "0x"
		} else if arg <= 255 {
			argsStr[i] = strconv.FormatUint(arg, 10)
		} else {
			argsStr[i] = "z64/" + strconv.FormatUint(arg, 10)
		}
	}
	return AmountsName + "(" + strings.Join(argsStr, ",") + ")"
}

func (a Amounts) String() string {
	return a.Source()
}

func AmountsFromBytes(data []byte) (Amounts, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data)
	if err != nil {
		return nil, err
	}
	if sym != AmountsName {
		return nil, fmt.Errorf("AmountsFromBytes: not 'amounts' constraint")
	}
	ret := make(Amounts, len(args))
	for i, arg := range args {
		if ret[i], err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(arg)); err != nil {
			return nil, fmt.Errorf("AmountsFromBytes: %w", err)
		}
	}
	return ret, nil
}

func registerAmountsConstraint(lib *Library) {
	lib.mustRegisterVarargConstraint(AmountsName, func(data []byte) (Constraint, error) {
		return AmountsFromBytes(data)
	}, initTestAmountsConstraint)
}

func initTestAmountsConstraint() {
	example, err := NewAmounts(1, 2, 1337, 0x01020304050607)
	util.AssertNoError(err)

	exampleBack, err := ConstraintFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.Name() == AmountsName, "inconsistency 1")
	exampleBack1 := exampleBack.(Amounts)
	util.Assertf(len(exampleBack1) == 4, "inconsistency 2")

	util.Assertf(exampleBack1[0] == 1, "exampleBack1[0]==1")
	util.Assertf(exampleBack1[1] == 2, "exampleBack1[1]==2")
	util.Assertf(exampleBack1[2] == 1337, "exampleBack1[2]==1337")
	util.Assertf(exampleBack1[3] == 0x01020304050607, "exampleBack1[3]==0x01020304050607")
}

func evalAmounts(par *easyfl.CallParams[*EvalContext]) []byte {
	return []byte{0xff}
}
