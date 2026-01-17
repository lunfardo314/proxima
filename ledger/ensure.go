package ledger

import (
	"fmt"

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

func EnsureStopDelegationFromDelegationID(chainID base.ChainID) EnsureStopDelegation {
	return EnsureStopDelegation{chainID}
}

// EnsureStopDelegationFromBytesAtSlot parses an EnsureStopDelegation constraint using the library for the given slot.
func EnsureStopDelegationFromBytesAtSlot(data []byte, slot uint32) (*EnsureStopDelegation, error) {
	return EnsureStopDelegationFromBytesWithLib(data, L(slot))
}

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

// EnsureStopDelegationFromBytes parses an EnsureStopDelegation constraint using the latest library version.
// Deprecated: Use EnsureStopDelegationFromBytesAtSlot for parsing historical bytecode.
func EnsureStopDelegationFromBytes(data []byte) (*EnsureStopDelegation, error) {
	return EnsureStopDelegationFromBytesAtSlot(data, base.MaxSlot)
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

const ensureStopFreezeDelegationConstraintSource = `
func _ensureStopDelegation :
and(
  require(
	 equal(
		parseInlineDataArgument(producedConstraintByIndex(concat(selfUnlockParameters,2)), 0, #chain), 
		$0
	 ),
	 !!!ensureStopDelegation:_delegationID_is_wrong
  ),
  require(
	 equal(
		parseInlineDataArgument(producedConstraintByIndex(concat(selfUnlockParameters,3)), 1, #delegateLockState),
		2 // 2 means on hold
	),
	!!!ensureStopDelegation:_delegation_produced_state_is_not_on_hold
  )
)

// $0 delegation chain ID
// Checks unlock conditions. Conditions are satisfied when unlock data is one byte with the number of
// produced output that is delegation output with the given delegation chain ID and it is 'on hold''
// 
// This constraint script is attached to the sequencer command. 
// Its purpose is to enforce real revocation of the delegation by the sequencer
// For tagAlong outputs condition is only enforced for tag-along slot range
func ensureStopDelegation :
or(
   and(
      selfIsProducedOutput,
      require(
        equal(len($0), u64/32),
        !!!wrong_chain_id
      )
   ),
   and(
      selfIsConsumedOutput,
      if(
         selfHasLockType(#tagAlong),
         or(
            greaterOrEqualThan(selfInputSlotPace, constTagAlongReclaimSlots), 
            _ensureStopDelegation($0)
         ),
         _ensureStopDelegation($0)
      )
   )
)
`
