package ledger

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync/atomic"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// DelegationLock is a basic delegation lock which is:
// - unlockable by owner any slot
// - unlockable by delegation target on C=0,1,2,3 slots, where C = (slot + chainID[0:3]) mod 6
// - NOT unlockable by delegation target on C=5,6 slots, where C = (slot + chainID[0:3]) mod 6
type DelegationLock struct {
	TargetLock Accountable
	OwnerLock  Accountable
	// must point to the sibling chain constraint
	ChainConstraintIndex byte
	StartTime            base.LedgerTime
	StartAmount          uint64
}

const (
	DelegationLockName     = "delegationLock"
	delegationLockTemplate = DelegationLockName + "(%d, %s, %s, 0x%s, u64/%d)"
)

const delegationLockSource = `
func minimumDelegatedAmount : u64/50000000

// ledger constant. At least 1 slot between transactions of delegation chain
func delegationPaceTicks : u64/256

// $0 sibling index
func selfSiblingUnlockParams : atTuple8(unlockParamsByIndex(selfOutputIndex), $0)

// Enforces delegation target lock and additional constraints, such as immutable chain 
// transition with non-decreasing amount
// $0 chain constraint index
// $1 target lock
// $2 path to successor output
func _enforceDelegationTargetConstraintsOnSuccessor : and(
    $1,  // target lock must be unlocked
    require(lessOrEqualThan(selfAmountValue, amountValueByOutputPath($2)), !!!amount_should_not_decrease),
    require(equal(atPath(concat($2, lockConstraintIndex)), selfSiblingConstraint(lockConstraintIndex)), !!!lock_must_be_immutable),
    require(equal(byte(selfSiblingUnlockParams($0),2), 0), !!!chain_must_be_state_transition)
)

// constant. A map with!= 0 at bytes where delegation transaction is open  
func _openDelegationSlotMap : 0xffffffff0000

// $0 4-byte prefix of slice (usually chainID)
// $1 4 bytes of the slot
// return true if _openDelegationSlotMap has non-0 in the position of mod(sum($0,$1), len(_openDelegationSlotMap))
func isOpenDelegationSlot : not(isZero(
    byte(
       _openDelegationSlotMap, 
       byte(mod(add(slice($0,0,3), $1),len(_openDelegationSlotMap)), 7)
    )
))

// $0 predecessor chain constraint index
func _selfSuccessorChainData : parseInlineDataArgument(producedConstraintByIndex(slice(selfSiblingUnlockParams($0),0,1)), #chain, 0)	

// $0 chain constraint index
func _validDelegationChainPace : or(
	isZero(selfChainID($0)),  // origin
    require(lessOrEqualThan(delegationPaceTicks, ticksBefore(selfChainPredecessorTimestamp($0),txTimestampBytes)), !!!wrong_delegation_chain_pace)
)

// $0 chain constraint index
// $1 target lock
// $2 owner lock
// $3 start time 
// $4 start amount
func delegationLock: and(
	mustSize($0,1),
           // only sizes are enforced, otherwise $3 and $4 are auxiliary, for information
	require(and(equal(len($3),u64/5), equal(len($4),u64/8)), !!!args_$3_and_$4_must_be_5_and_8_bytes_length), 
    require(not(isBranchTransaction), !!!delegation_should_not_be_branch),
	enforceMinimumStorageDeposit,
    or(
		and(
             // check general consistency of the lock on the produced output
            selfIsProducedOutput,
            require(equal(parsePrefixBytecode(selfSiblingConstraint($0)), #chain), !!!wrong_chain_constraint_index),
            require(greaterOrEqualThan(selfAmountValue, minimumDelegatedAmount), !!!delegation_amount_is_below_minimum),
	        require(not(equal($0, 0xff)), !!!chain_constraint_index_0xff_is_not_alowed),
            _validDelegationChainPace($0),
            $1, $2
        ), 
        and(
            // check unlock conditions of the consumed output
            selfIsConsumedOutput,
            or(
               $2,   // unlocked owner's lock validates it all
               and(
                   require(isOpenDelegationSlot(_selfSuccessorChainData($0), txSlot), !!!must _be_on_liquidity_slot),
                   require(_enforceDelegationTargetConstraintsOnSuccessor(
                      $0,
                      $1, 
                      concat(pathToProducedOutputs, byte(selfSiblingUnlockParams($0), 0)),
                   ), !!!wrong_delegation_target_successor)
               )
            )
        )
    )
)
`

func NewDelegationLock(owner, target Accountable, chainConstraintIndex byte, startTime base.LedgerTime, startAmount uint64) *DelegationLock {
	return &DelegationLock{
		TargetLock:           target,
		OwnerLock:            owner,
		ChainConstraintIndex: chainConstraintIndex,
		StartTime:            startTime,
		StartAmount:          startAmount,
	}
}

func DelegationLockFromBytes(data []byte) (*DelegationLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 5)
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: %w", err)
	}
	if sym != DelegationLockName {
		return nil, fmt.Errorf("DelegationLockFromBytes: not a DelegationLock")
	}
	arg0 := easyfl.StripDataPrefix(args[0])
	ret := &DelegationLock{}
	if len(arg0) != 1 || arg0[0] == 255 {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong chain constraint index")
	}
	ret.ChainConstraintIndex = arg0[0]

	ret.TargetLock, err = AccountableFromBytes(args[1])
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: %w", err)
	}
	ret.OwnerLock, err = AccountableFromBytes(args[2])
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: %w", err)
	}

	arg3 := easyfl.StripDataPrefix(args[3])
	ret.StartTime, err = base.LedgerTimeFromBytes(arg3)
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: %w", err)
	}

	arg4 := easyfl.StripDataPrefix(args[4])
	if len(arg4) != 8 {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong start amount")
	}
	ret.StartAmount = binary.BigEndian.Uint64(arg4)

	return ret, nil
}

func (d *DelegationLock) Source() string {
	return fmt.Sprintf(delegationLockTemplate,
		d.ChainConstraintIndex, d.TargetLock.Source(), d.OwnerLock.Source(), hex.EncodeToString(d.StartTime.Bytes()), d.StartAmount)
}

func (d *DelegationLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegationLock) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.TargetLock, d.OwnerLock})
}

func (d *DelegationLock) Name() string {
	return DelegationLockName
}

func (d *DelegationLock) String() string {
	return d.Source()
}

func (d *DelegationLock) Master() Accountable {
	return d.OwnerLock
}

func registerDelegationLock(lib *Library) {
	lib.mustRegisterConstraint(DelegationLockName, 5, func(data []byte) (Constraint, error) {
		return DelegationLockFromBytes(data)
	}, initTestDelegationConstraint)
	lib.mustRegisterLock(DelegationLockName, func(bytes []byte) (Lock, error) {
		ret, err := DelegationLockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func initTestDelegationConstraint() {
	a1 := AddressED25519Random()
	a2 := AddressED25519Random()
	ts := TimeNow()
	example := NewDelegationLock(a1, a2, 1, ts, 1337)
	exampleBack, err := DelegationLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(example, exampleBack), "inconsistency "+DelegationLockName)

	pref1, err := L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)

	pref2, err := L().EvalFromSource(nil, "#delegationLock")
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(pref1, pref2), "bytes.Equal(pref1, pref2)")
	util.Assertf(example.Source() == exampleBack.Source(), "example.Source()==exampleBack.Source()")
}

var __precompiledIsOpenDelegationSlotVar atomic.Pointer[easyfl.Expression[*EvalContext]]

func __precompiledIsOpenDelegationSlot() (ret *easyfl.Expression[*EvalContext]) {
	if ret = __precompiledIsOpenDelegationSlotVar.Load(); ret != nil {
		return ret
	}
	var err error
	ret, _, _, err = L().CompileExpression("isOpenDelegationSlot($0,$1)")
	util.AssertNoError(err)
	__precompiledIsOpenDelegationSlotVar.Store(ret)
	return
}

func IsOpenDelegationSlot(chainID base.ChainID, slot base.Slot) bool {
	return len(easyfl.EvalExpression(nil, __precompiledIsOpenDelegationSlot(), chainID[:4], slot.Bytes())) > 0
}

func MinimumDelegationAmount() uint64 {
	res, err := L().EvalFromSource(nil, "minimumDelegatedAmount")
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(res)
}

func NextOpenDelegationSlot(chainID base.ChainID, slot base.Slot) base.Slot {
	for ; !IsOpenDelegationSlot(chainID, slot); slot++ {
	}
	return slot
}

func NextOpenDelegationTimestamp(chainID base.ChainID, ts base.LedgerTime) (ret base.LedgerTime) {
	ret = ts
	if base.DiffTicks(ret, ts) < int64(DelegationLockPaceTicks()) {
		ret = ts.AddTicks(int(DelegationLockPaceTicks()))
	}
	return base.NewLedgerTime(NextOpenDelegationSlot(chainID, ret.Slot), ret.Tick)
}

func NextClosedDelegationSlot(chainID base.ChainID, slot base.Slot) base.Slot {
	for ; IsOpenDelegationSlot(chainID, slot); slot++ {
	}
	return slot
}

func NextClosedDelegationTimestamp(chainID base.ChainID, ts base.LedgerTime) (ret base.LedgerTime) {
	ret = ts
	if base.DiffTicks(ret, ts) < int64(DelegationLockPaceTicks()) {
		ret = ts.AddTicks(int(DelegationLockPaceTicks()))
	}
	return base.NewLedgerTime(NextClosedDelegationSlot(chainID, ret.Slot), ret.Tick)
}

var _delegationLockPace atomic.Uint64

func DelegationLockPaceTicks() uint64 {
	v := _delegationLockPace.Load()
	if v > 0 {
		return v
	}
	constBin, err := L().EvalFromSource(nil, "delegationPaceTicks")
	util.AssertNoError(err)

	v = binary.BigEndian.Uint64(constBin)
	_delegationLockPace.Store(v)
	return v
}

func ValidDelegationPace(predTs, succTs base.LedgerTime) bool {
	return base.DiffTicks(succTs, predTs) >= int64(DelegationLockPaceTicks())
}
