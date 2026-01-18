package ledger

import (
	"bytes"
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type (
	DelegateLock struct {
		Target                 ChainLock
		MasterLock             Accountable
		MaxFrozenEpochs        byte
		RequiredInflationShare uint16 // in promille, <= 1000
	}
	DelegateLockState struct {
		LastFrozenEpoch uint32
		State           byte
	}
)

const (
	DelegateLockName       = "delegateLock"
	DelegateLockTemplate   = DelegateLockName + "(%s, %s, %s, z16/%d)"
	DelegateLockTemplateHR = DelegateLockName + "(target=%s, master=%s, maxFreezeEpochs=%d, inflationShare=%d%%%%)"

	DelegateLockStateName       = "delegateLockState"
	DelegateLockStateTemplate   = DelegateLockStateName + "(z32/%d, %d)"
	DelegateLockStateTemplateHR = DelegateLockStateName + "(frozenUntilEpoch=%d, state=%s)"

	DelegateLockStateUndef  = byte(0)
	DelegateLockStateFrozen = byte(1)
	DelegateLockStateOnHold = byte(2)

	// 3rd unlock byte in the delegation output unlock parameters

	DelegationUnlockedByTarget = byte(0)
	DelegationUnlockedByMaster = byte(0xff)
)

//go:embed def/lock_delegate.efl
var delegateLock2Source string

//------------ DelegateLock

func NewDelegateLock(target ChainLock, master Accountable, maxFreezeEpochs byte, requiredInflationShare uint16) *DelegateLock {
	return &DelegateLock{
		Target:                 target,
		MasterLock:             master,
		MaxFrozenEpochs:        maxFreezeEpochs,
		RequiredInflationShare: requiredInflationShare,
	}
}

func (d *DelegateLock) Source() string {
	m := "0x"
	if d.MaxFrozenEpochs != 0 && d.MaxFrozenEpochs != byte(L(base.MaxSlot).MaxFrozenEpochs) {
		m = fmt.Sprintf("%d", d.MaxFrozenEpochs)
	}
	return fmt.Sprintf(DelegateLockTemplate, d.Target.Source(), d.MasterLock.Source(), m, d.RequiredInflationShare)
}

func (d *DelegateLock) String() string {
	return fmt.Sprintf(DelegateLockTemplateHR, d.Target.String(), d.MasterLock.String(), d.MaxFrozenEpochs, d.RequiredInflationShare)
}

func (d *DelegateLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegateLock) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.Target, d.MasterLock})
}

func DelegateLockFromBytesWithLib(data []byte, lib *Library) (*DelegateLock, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 4)
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}
	if sym != DelegateLockName {
		return nil, fmt.Errorf("Delegate2LockFromBytes: not a DelegateLock")
	}
	// chain constraint index
	ret := &DelegateLock{}

	// target lock
	ret.Target, err = ChainLockFromBytesWithLib(args[0], lib)
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}
	// master lock
	ret.MasterLock, err = AccountableFromBytesWithLib(args[1], lib)
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}

	// max coverage lock slots
	a2, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil || a2 >= 256 {
		return nil, fmt.Errorf("Delegate2LockFromBytes: wrong max frozen epochs: %v", err)
	}
	ret.MaxFrozenEpochs = byte(a2)
	if ret.MaxFrozenEpochs == 0 {
		// set default from the library for this slot
		ret.MaxFrozenEpochs = byte(lib.MaxFrozenEpochs)
	}

	// minimum inflation advance
	ret.RequiredInflationShare, err = easyfl_util.Uint16FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: wrong max inflation margin: %v", err)
	}

	return ret, nil
}

func (d *DelegateLock) Name() string {
	return DelegateLockName
}

func (d *DelegateLock) Master() Accountable {
	return d.MasterLock
}

func registerDelegateLock(lib *Library) {
	lib.mustRegisterConstraint(DelegateLockName, 4, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return DelegateLockFromBytesWithLib(data, lib)
	})
	lib.mustRegisterLockSerde(DelegateLockName, func(bytes []byte) (Lock, error) {
		// Use latest library version for library registration parsing
		ret, err := DelegateLockFromBytesWithLib(bytes, lib)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
	lib.mustRegisterConstraint(DelegateLockStateName, 2, func(data []byte) (Constraint, error) {
		return DelegateLockStateFromBytesWithLib(data, lib)
	})
}

//--------------------------- delegationLockState

func DelegateLockStateFromBytesWithLib(data []byte, lib *Library) (DelegateLockState, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: %w", err)
	}
	if sym != DelegateLockStateName {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: not a DelegateLockState")
	}
	fr, err := easyfl_util.Uint32FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: wrong argument 0: %w", err)
	}
	state := easyfl.StripDataPrefix(args[1])
	if len(state) != 1 {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: argument 1 must be one byte")
	}
	return DelegateLockState{
		LastFrozenEpoch: fr,
		State:           state[0],
	}, nil
}

func (d DelegateLockState) Source() string {
	return fmt.Sprintf(DelegateLockStateTemplate, d.LastFrozenEpoch, d.State)
}

func (d DelegateLockState) String() string {
	s := "undef"
	switch d.State {
	case DelegateLockStateFrozen:
		s = "frozen"
	case DelegateLockStateOnHold:
		s = "on hold"
	}
	return fmt.Sprintf(DelegateLockStateTemplateHR, d.LastFrozenEpoch, s)
}

func (d DelegateLockState) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegateLockState) Name() string {
	return DelegateLockStateName
}

func init() {
	registerInlineTest(func(lib *Library) {
		target := ChainLockFromChainID(base.RandomChainID())
		master := AddressED25519Random()
		example := NewDelegateLock(target, master, 3, 10)

		exampleBack, err := DelegateLockFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(example.MaxFrozenEpochs == 3, "Delegate2LockFromBytes: wrong back 1")
		util.Assertf(exampleBack.MaxFrozenEpochs == example.MaxFrozenEpochs, "Delegate2LockFromBytes: wrong back 2")
		util.Assertf(exampleBack.RequiredInflationShare == example.RequiredInflationShare, "Delegate2LockFromBytes: wrong back 3")
		util.Assertf(example.RequiredInflationShare == 10, "Delegate2LockFromBytes: wrong back 4")

		util.Assertf(EqualConstraints(example, exampleBack), "inconsistency 1 "+DelegateLockName)
		exampleBack2, err := LockFromBytes(example.Bytes())
		util.AssertNoError(err)
		util.Assertf(EqualConstraints(example, exampleBack2), "inconsistency 2 "+DelegateLockName)

		pref1, err := lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)

		pref2, err := lib.EvalFromSource(nil, "#"+DelegateLockName)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(pref1, pref2), "bytes.Equal(pref1, pref2)")
		util.Assertf(example.Source() == exampleBack.Source(), "example.Source()==exampleBack.Source()")
	})

	registerInlineTest(func(lib *Library) {
		dlz := DelegateLockState{3001, DelegateLockStateFrozen}

		dlzBack, err := DelegateLockStateFromBytesWithLib(dlz.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(dlzBack.LastFrozenEpoch == 3001, "DelegateLockState: inconsistency 1")
		util.Assertf(dlzBack.State == DelegateLockStateFrozen, "DelegateLockState: inconsistency 2")
		util.Assertf(dlz == dlzBack, "DelegateLockState: inconsistency 3")
	})
}
