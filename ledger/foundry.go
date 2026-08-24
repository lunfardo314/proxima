package ledger

import (
	"bytes"
	_ "embed"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

//go:embed def/native_token.easyfl
var nativeTokenSource string

// Foundry is the typed wrapper for the 1-arg `foundry(supply)`
// constraint. It lives at ConstraintIndexFoundry (= 4) on a foundry
// output — a chained UTXO whose chain ID is the foundry's tag. The tag
// is NOT stored here: it is read off the sibling chain constraint at
// ConstraintIndexChain (= 3). Foundry carries only the circulating
// supply of the tag's native token.
// See claude/archive/shipped/native_token.md §2.
type Foundry struct {
	// Supply is the circulating supply of the tag's native token.
	Supply uint64
}

const (
	FoundryName     = "foundry"
	foundryTemplate = FoundryName + "(z64/%d)"
)

func NewFoundry(supply uint64) *Foundry {
	return &Foundry{Supply: supply}
}

func (f *Foundry) Name() string { return FoundryName }

func (f *Foundry) Source() string {
	return fmt.Sprintf(foundryTemplate, f.Supply)
}

func (f *Foundry) Bytes() []byte { return mustBinFromSource(f.Source()) }

func (f *Foundry) String() string {
	return fmt.Sprintf("%s(supply=%d)", FoundryName, f.Supply)
}

// FoundryFromBytes parses the 1-arg foundry bytecode.
func FoundryFromBytes(data []byte) (*Foundry, error) {
	return FoundryFromBytesWithLib(data, L(base.MaxSlot))
}

func FoundryFromBytesWithLib(data []byte, lib *Library) (*Foundry, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, fmt.Errorf("FoundryFromBytes: %w", err)
	}
	if sym != FoundryName {
		return nil, fmt.Errorf("FoundryFromBytes: not a foundry")
	}
	ret := &Foundry{}
	if ret.Supply, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0])); err != nil {
		return nil, fmt.Errorf("FoundryFromBytes: %w", err)
	}
	return ret, nil
}

func registerFoundry(lib *Library) {
	lib.mustRegisterConstraint(FoundryName, 1, func(data []byte) (Constraint, error) {
		return FoundryFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		// Round-trip a zero-supply foundry (origin shape) and a
		// non-zero supply foundry to exercise the z64 encoding boundary.
		example := NewFoundry(1_000_000)
		back, err := FoundryFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(back.Supply == example.Supply, "foundry supply round-trip")
		util.Assertf(EqualConstraints(example, back), "inconsistency in "+FoundryName)

		zero := NewFoundry(0)
		zeroBack, err := FoundryFromBytesWithLib(zero.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(zeroBack.Supply == 0, "foundry zero supply round-trip")

		pref1, err := lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
		pref2, err := lib.EvalFromSource(nil, "#"+FoundryName)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(pref1, pref2), "foundry prefix match")
	})
}
