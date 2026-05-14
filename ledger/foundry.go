package ledger

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

//go:embed def/native_token.easyfl
var nativeTokenSource string

// Foundry is the typed wrapper for the 2-arg `foundry(tag, supply)`
// constraint. It lives at ConstraintIndexFoundry (= 4) on a foundry
// output — a chained UTXO whose chain ID equals Tag. Carries the
// circulating supply of the tag's native token. foundry()'s EasyFL
// body enforces the tag-equals-chainID invariant at every transit (the
// check is skipped at origin, where the chain ID is still NilChainID).
// See claude/native_token.md §2.
type Foundry struct {
	// Tag is the foundry chain ID (must equal the chain ID at
	// ConstraintIndexChain on the same output, enforced in Phase C).
	Tag base.ChainID
	// Supply is the circulating supply of the tag's native token.
	Supply uint64
}

const (
	FoundryName     = "foundry"
	foundryTemplate = FoundryName + "(0x%s, z64/%d)"
)

func NewFoundry(tag base.ChainID, supply uint64) *Foundry {
	return &Foundry{Tag: tag, Supply: supply}
}

// NewFoundryOrigin returns a foundry constraint for the origin output of a
// foundry chain. At origin the chain ID is still NilChainID; foundry()
// EasyFL skips the tag-equals-chain-ID check at origin and starts
// enforcing it from the first transit onwards.
func NewFoundryOrigin(initialSupply uint64) *Foundry {
	return NewFoundry(base.NilChainID, initialSupply)
}

func (f *Foundry) Name() string { return FoundryName }

func (f *Foundry) Source() string {
	return fmt.Sprintf(foundryTemplate, hex.EncodeToString(f.Tag[:]), f.Supply)
}

func (f *Foundry) Bytes() []byte { return mustBinFromSource(f.Source()) }

func (f *Foundry) String() string {
	return fmt.Sprintf("%s(tag=%s, supply=%d)", FoundryName, f.Tag.String(), f.Supply)
}

// FoundryFromBytes parses the 2-arg foundry bytecode.
func FoundryFromBytes(data []byte) (*Foundry, error) {
	return FoundryFromBytesWithLib(data, L(base.MaxSlot))
}

func FoundryFromBytesWithLib(data []byte, lib *Library) (*Foundry, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, fmt.Errorf("FoundryFromBytes: %w", err)
	}
	if sym != FoundryName {
		return nil, fmt.Errorf("FoundryFromBytes: not a foundry")
	}
	ret := &Foundry{}
	if ret.Tag, err = base.ChainIDFromBytes(easyfl.StripDataPrefix(args[0])); err != nil {
		return nil, fmt.Errorf("FoundryFromBytes: %w", err)
	}
	if ret.Supply, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1])); err != nil {
		return nil, fmt.Errorf("FoundryFromBytes: %w", err)
	}
	return ret, nil
}

func registerFoundry(lib *Library) {
	lib.mustRegisterConstraint(FoundryName, 2, func(data []byte) (Constraint, error) {
		return FoundryFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		// Round-trip both a zero-supply foundry (origin shape) and a
		// non-zero supply foundry to exercise the z64 encoding boundary.
		tag := base.RandomChainID()
		example := NewFoundry(tag, 1_000_000)
		back, err := FoundryFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(back.Tag == example.Tag, "foundry tag round-trip")
		util.Assertf(back.Supply == example.Supply, "foundry supply round-trip")
		util.Assertf(EqualConstraints(example, back), "inconsistency in "+FoundryName)

		zero := NewFoundry(tag, 0)
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
