package ledger

import (
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type EnsureStopDelegation struct {
	base.ChainID
}

const (
	EnsureStopDelegationName       = "ensureStopDelegation"
	EnsureStopDelegationTemplate   = EnsureStopDelegationName + "(0x%s)"
	EnsureStopDelegationTemplateHR = EnsureStopDelegationName + "(%s)"
)

//go:embed def/ensure.easyfl
var ensureStopFreezeDelegationConstraintSource string

func EnsureStopDelegationFromBytesWithLib(data []byte, lib *Library) (*EnsureStopDelegation, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 1)
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
	return &EnsureStopDelegation{delegationID}, nil
}

func (d *EnsureStopDelegation) Source() string {
	return fmt.Sprintf(EnsureStopDelegationTemplate, d.ChainID.StringHex())
}

func (d *EnsureStopDelegation) String() string {
	return fmt.Sprintf(EnsureStopDelegationTemplateHR, d.ChainID.String())
}

func (d *EnsureStopDelegation) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *EnsureStopDelegation) Name() string {
	return EnsureStopDelegationName
}

func registerEnsureConstraints(lib *Library) {
	lib.mustRegisterConstraint(EnsureStopDelegationName, 1, func(data []byte) (Constraint, error) {
		return EnsureStopDelegationFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		e := EnsureStopDelegation{base.RandomChainID()}

		eBack, err := EnsureStopDelegationFromBytesWithLib(e.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(eBack.ChainID == e.ChainID, "EnsureStopDelegation: inconsistency")
	})
}
