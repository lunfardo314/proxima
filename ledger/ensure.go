package ledger

import (
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// EnsureStopDelegation is the constraint a delegator attaches to an
// askstop command output. Allowance is the maximum the target sequencer
// may take out of the delegation balance as compensation; 0 means none,
// and the delegation's non-decrease rule applies unchanged.
type EnsureStopDelegation struct {
	base.ChainID
	Allowance uint64
}

const (
	EnsureStopDelegationName       = "ensureStopDelegation"
	EnsureStopDelegationTemplate   = EnsureStopDelegationName + "(0x%s, u64/%d)"
	EnsureStopDelegationTemplateHR = EnsureStopDelegationName + "(%s, %s)"
)

//go:embed def/ensure.easyfl
var ensureStopFreezeDelegationConstraintSource string

func EnsureStopDelegationFromBytesWithLib(data []byte, lib *Library) (*EnsureStopDelegation, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, fmt.Errorf("EnsureStopDelegationFromBytes: %w", err)
	}
	if sym != EnsureStopDelegationName {
		return nil, fmt.Errorf("EnsureStopDelegationFromBytes: not a EnsureStopDelegation")
	}
	delegationID, err := base.ChainIDFromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, err
	}
	allowance, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil {
		return nil, fmt.Errorf("EnsureStopDelegationFromBytes: wrong allowance: %w", err)
	}
	return &EnsureStopDelegation{ChainID: delegationID, Allowance: allowance}, nil
}

func (d *EnsureStopDelegation) Source() string {
	return fmt.Sprintf(EnsureStopDelegationTemplate, d.ChainID.StringHex(), d.Allowance)
}

func (d *EnsureStopDelegation) String() string {
	return fmt.Sprintf(EnsureStopDelegationTemplateHR, d.ChainID.String(), util.Th(d.Allowance))
}

func (d *EnsureStopDelegation) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *EnsureStopDelegation) Name() string {
	return EnsureStopDelegationName
}

func registerEnsureConstraints(lib *Library) {
	lib.mustRegisterConstraint(EnsureStopDelegationName, 2, func(data []byte) (Constraint, error) {
		return EnsureStopDelegationFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		// both the no-allowance and the allowance-bearing forms must round-trip;
		// 0 is encoded as empty inline data, which is the common case
		for _, allowance := range []uint64{0, 1_337_000} {
			e := EnsureStopDelegation{ChainID: base.RandomChainID(), Allowance: allowance}

			eBack, err := EnsureStopDelegationFromBytesWithLib(e.Bytes(), lib)
			util.AssertNoError(err)
			util.Assertf(eBack.ChainID == e.ChainID, "EnsureStopDelegation: inconsistency")
			util.Assertf(eBack.Allowance == e.Allowance, "EnsureStopDelegation: allowance inconsistency")
		}
	})
}
