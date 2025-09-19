package ledger

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type TagAlongLock struct {
	TargetSequencerID base.ChainID
	SenderLock        Accountable
}

const (
	TagAlongLockName           = "tagAlong"
	tagAlongLockTemplateSource = TagAlongLockName + "(0x%s, %s)"
	tagAlongLockTemplateHR     = TagAlongLockName + "(target=%s, sender=%s)"
)

func TagAlongLockFromBytes(data []byte) (*TagAlongLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != TagAlongLockName {
		return nil, fmt.Errorf("not a TagAlongLock")
	}

	chainIdBin := easyfl.StripDataPrefix(args[0])
	chainID, err := base.ChainIDFromBytes(chainIdBin)
	if err != nil {
		return nil, err
	}

	//senderBin := easyfl.StripDataPrefix(args[1])
	sender, err := AccountableFromBytes(args[1])
	if err != nil {
		return nil, err
	}

	return &TagAlongLock{
		TargetSequencerID: chainID,
		SenderLock:        sender,
	}, nil
}

func (t *TagAlongLock) Source() string {
	return fmt.Sprintf(tagAlongLockTemplateSource, t.TargetSequencerID.StringHex(), t.SenderLock.Source())
}

func (t *TagAlongLock) String() string {
	return fmt.Sprintf(tagAlongLockTemplateHR, t.TargetSequencerID.String(), t.SenderLock.String())
}

func (t *TagAlongLock) Bytes() []byte {
	return mustBinFromSource(t.Source())
}

func (t *TagAlongLock) Accounts() []Accountable {
	return []Accountable{ChainLockFromChainID(t.TargetSequencerID), t.SenderLock}
}

func (t *TagAlongLock) Master() Accountable {
	return nil
}

func (t *TagAlongLock) Name() string {
	return ChainLockName
}

func (t *TagAlongLock) AsLock() Lock {
	return t
}

func NewTagAlongOutput(fee uint64, targetChainID base.ChainID, senderPrivateKey ed25519.PrivateKey) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithTokenBalance(fee)
		o.WithLock(&TagAlongLock{
			TargetSequencerID: targetChainID,
			SenderLock:        AddressED25519FromPrivateKey(senderPrivateKey),
		})
	})
}

func NewTagAlongLockUnlockParams(predChainOutputIndex, predChainConstraintIndex, unlockMode byte) []byte {
	return []byte{predChainOutputIndex, predChainConstraintIndex, unlockMode}
}

func registerTagAlongLockConstraint(lib *Library) {
	lib.mustRegisterConstraint(TagAlongLockName, 2, func(data []byte) (Constraint, error) {
		return TagAlongLockFromBytes(data)
	}, initTestTagAlongLockConstraint)
	lib.mustRegisterLock(TagAlongLockName, func(bytes []byte) (Lock, error) {
		ret, err := TagAlongLockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func initTestTagAlongLockConstraint() {
	chainID := base.RandomChainID()
	sender := AddressED25519Random()
	example := &TagAlongLock{
		TargetSequencerID: chainID,
		SenderLock:        sender,
	}
	tagAlongLockBack, err := TagAlongLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(tagAlongLockBack, example), "inconsistency "+TagAlongLockName)

	_, err = L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)
}

// --- helper structure

type TagAlongOutput struct {
	OutputWithID
	*TagAlongLock
}

func (o *TagAlongOutput) IsTagAlongSlot(slot uint32) bool {
	s := o.ID.Slot()
	return slot >= s && slot-s < Const.TagAlongSlots
}

func (o *TagAlongOutput) IsTagAlongReclaimSlot(slot uint32) bool {
	s := o.ID.Slot()
	return slot >= s && slot-s >= Const.TagAlongSlots && slot-s < Const.TagAlongReclaimSlots
}

func (o *TagAlongOutput) IsTagAlongPurgeSlot(slot uint32) bool {
	s := o.ID.Slot()
	return slot >= s && slot-s >= Const.TagAlongReclaimSlots
}

func (o *TagAlongOutput) StatusInSlot(slot uint32) string {
	switch {
	case o.IsTagAlongSlot(slot):
		return "tag-along"
	case o.IsTagAlongReclaimSlot(slot):
		return "tag-along-reclaim"
	case o.IsTagAlongPurgeSlot(slot):
		return "tag-along-purge"
	}
	return "undefined"
}

// TODO randomize access to purgeable tag-along outputs and incentivize ledger cleanup

const tagAlongLockConstraintSource = `
func constTagAlongSlots : u64/30  // 5 min
func constTagAlongReclaimSlots : u64/390 // 5 min + 1 hour

func selfInputSlotPace: sub(txSlot, slotOfInputByIndex(selfOutputIndex))

func _selfSenderBytecode : parseBytecode(self, 1, selfBytecodePrefix)

// $0 - target sequencer ID, like in the chainLock
// $1 - sender account source, usually addressED25519 
func tagAlong : 
or(
  and(
     selfIsProducedOutput,
     require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	 require(equal(len($0),u64/32), !!!32-byte_long_argument_expected),
	 require(not(isZero($0)), !!!non_zero_argument_expected),   // to prevent common error
     require(
        equal(
           parseInlineDataArgument(_selfSenderBytecode, 0, #a, #addressED25519), 
           blake2b(publicKeyED25519(txSignature))
        ),
        !!!sender_hash_check_failed
     ),
  ),
  and(
     selfIsConsumedOutput,
	 or(
		greaterOrEqualThan(selfInputSlotPace, constTagAlongReclaimSlots),  // unlockable by anybody  
		and( 
			 // unlockable by the target
		   lessThan(selfInputSlotPace, constTagAlongSlots),
		   require(chainLock($0), !!!unlock_window_error:_inside_tag_along_slots_must_be_unlocked_by_the_target)
		),
			 // unlockable by the sender
        require(
           $1,
           !!!unlock_window_error:_inside_reclaim_slots_must_be_unlocked_by_the_sender
        )
	 )
  ),
)
`
