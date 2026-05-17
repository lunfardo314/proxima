package ledger

import (
	"bytes"
	_ "embed"
	"fmt"
	"math"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

//go:embed def/delegation_params.easyfl
var delegationParamsSource string

// DelegationParams is the typed wrapper for the 2-arg
// delegationParams(epochSlots, maxFrozenEpochs) constraint living at
// ConstraintIndexDelegationParams (= 6) on chain outputs that opt in to
// accept delegations. Pinned by selfImmutableOnSuccessorIndex across
// every chain transit; attachable only at chain origin.
//
// See claude/delegation_epoch_params.md.
type DelegationParams struct {
	EpochSlots      uint32
	MaxFrozenEpochs byte
}

const (
	DelegationParamsName     = "delegationParams"
	delegationParamsTemplate = DelegationParamsName + "(z32/%d, %d)"
)

func NewDelegationParams(epochSlots uint32, maxFrozenEpochs byte) *DelegationParams {
	return &DelegationParams{
		EpochSlots:      epochSlots,
		MaxFrozenEpochs: maxFrozenEpochs,
	}
}

func (p *DelegationParams) Name() string { return DelegationParamsName }

func (p *DelegationParams) Source() string {
	return fmt.Sprintf(delegationParamsTemplate, p.EpochSlots, p.MaxFrozenEpochs)
}

func (p *DelegationParams) Bytes() []byte { return mustBinFromSource(p.Source()) }

func (p *DelegationParams) String() string {
	return fmt.Sprintf("%s(epochSlots=%d, maxFrozenEpochs=%d)",
		DelegationParamsName, p.EpochSlots, p.MaxFrozenEpochs)
}

func DelegationParamsFromBytes(data []byte) (*DelegationParams, error) {
	return DelegationParamsFromBytesWithLib(data, L(base.MaxSlot))
}

func DelegationParamsFromBytesWithLib(data []byte, lib *Library) (*DelegationParams, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, fmt.Errorf("DelegationParamsFromBytes: %w", err)
	}
	if sym != DelegationParamsName {
		return nil, fmt.Errorf("DelegationParamsFromBytes: not a delegationParams")
	}
	e0, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil || e0 > math.MaxUint32 {
		return nil, fmt.Errorf("DelegationParamsFromBytes: epochSlots out of range: %v", err)
	}
	e1, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil || e1 >= 256 {
		return nil, fmt.Errorf("DelegationParamsFromBytes: maxFrozenEpochs out of range: %v", err)
	}
	return &DelegationParams{
		EpochSlots:      uint32(e0),
		MaxFrozenEpochs: byte(e1),
	}, nil
}

func registerDelegationParams(lib *Library) {
	lib.mustRegisterConstraint(DelegationParamsName, 2, func(data []byte) (Constraint, error) {
		return DelegationParamsFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		// Round-trip a typical default (600/20) and a bounds-corner value
		// (500/8) to exercise z32 trimming for both args.
		for _, ex := range []*DelegationParams{
			NewDelegationParams(600, 20),
			NewDelegationParams(500, 8),
			NewDelegationParams(2000, 32),
		} {
			back, err := DelegationParamsFromBytesWithLib(ex.Bytes(), lib)
			util.AssertNoError(err)
			util.Assertf(back.EpochSlots == ex.EpochSlots, "delegationParams epochSlots round-trip (%d)", ex.EpochSlots)
			util.Assertf(back.MaxFrozenEpochs == ex.MaxFrozenEpochs, "delegationParams maxFrozenEpochs round-trip (%d)", ex.MaxFrozenEpochs)
			util.Assertf(EqualConstraints(ex, back), "inconsistency in "+DelegationParamsName)
		}

		example := NewDelegationParams(600, 20)
		pref1, err := lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
		pref2, err := lib.EvalFromSource(nil, "#"+DelegationParamsName)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(pref1, pref2), "delegationParams prefix match")
	})
}
