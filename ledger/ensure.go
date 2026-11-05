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

// EnsureTopUpDelegation is the constraint a delegator attaches to a top-up
// request output: a tag-along to the delegation's target whose whole balance
// is to be added to the delegation. It forces the sequencer onto the delegate
// lock's top-up path, where the successor balance is exact. The strict form,
// valid from library upgrade 6 on, also requires a plain delegation; the mine
// lock demands it for the full reward (kb/library_upgrade6.md).
type EnsureTopUpDelegation struct {
	base.ChainID
	Strict bool
}

// ensureTopUpStrictMark is the byte after the delegation ID that makes the
// argument the strict form.
const ensureTopUpStrictMark = 0x01

const (
	EnsureStopDelegationName       = "ensureStopDelegation"
	EnsureStopDelegationTemplate   = EnsureStopDelegationName + "(0x%s, u64/%d)"
	EnsureStopDelegationTemplateHR = EnsureStopDelegationName + "(%s, %s)"

	EnsureTopUpDelegationName       = "ensureTopUpDelegation"
	EnsureTopUpDelegationTemplate   = EnsureTopUpDelegationName + "(0x%s)"
	EnsureTopUpDelegationTemplateHR = EnsureTopUpDelegationName + "(%s)"
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

func EnsureTopUpDelegationFromBytesWithLib(data []byte, lib *Library) (*EnsureTopUpDelegation, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, fmt.Errorf("EnsureTopUpDelegationFromBytes: %w", err)
	}
	if sym != EnsureTopUpDelegationName {
		return nil, fmt.Errorf("EnsureTopUpDelegationFromBytes: not a EnsureTopUpDelegation")
	}
	arg := easyfl.StripDataPrefix(args[0])
	strict := len(arg) == base.ChainIDLength+1
	if strict && arg[base.ChainIDLength] != ensureTopUpStrictMark {
		return nil, fmt.Errorf("EnsureTopUpDelegationFromBytes: wrong strict mark %d", arg[base.ChainIDLength])
	}
	if strict {
		arg = arg[:base.ChainIDLength]
	}
	delegationID, err := base.ChainIDFromBytes(arg)
	if err != nil {
		return nil, err
	}
	return &EnsureTopUpDelegation{ChainID: delegationID, Strict: strict}, nil
}

func (d *EnsureTopUpDelegation) Source() string {
	arg := d.ChainID.StringHex()
	if d.Strict {
		arg += fmt.Sprintf("%02x", ensureTopUpStrictMark)
	}
	return fmt.Sprintf(EnsureTopUpDelegationTemplate, arg)
}

func (d *EnsureTopUpDelegation) String() string {
	return fmt.Sprintf(EnsureTopUpDelegationTemplateHR, d.ChainID.String())
}

func (d *EnsureTopUpDelegation) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *EnsureTopUpDelegation) Name() string {
	return EnsureTopUpDelegationName
}

func registerEnsureConstraints(lib *Library) {
	lib.mustRegisterConstraint(EnsureStopDelegationName, 2, func(data []byte) (Constraint, error) {
		return EnsureStopDelegationFromBytesWithLib(data, lib)
	})
	lib.mustRegisterConstraint(EnsureTopUpDelegationName, 1, func(data []byte) (Constraint, error) {
		return EnsureTopUpDelegationFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		for _, strict := range []bool{false, true} {
			e := EnsureTopUpDelegation{ChainID: base.RandomChainID(), Strict: strict}
			eBack, err := EnsureTopUpDelegationFromBytesWithLib(e.Bytes(), lib)
			util.AssertNoError(err)
			util.Assertf(e.ChainID == eBack.ChainID && e.Strict == eBack.Strict, "EnsureTopUpDelegation: inconsistency")
		}
	})
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
