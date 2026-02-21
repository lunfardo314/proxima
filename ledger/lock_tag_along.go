package ledger

import (
	"encoding/hex"
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type TagAlongLock struct {
	TargetSequencerID base.ChainID
	SenderID          base.SpenderID
}

const (
	TagAlongLockName           = "tagAlong"
	tagAlongLockTemplateSource = TagAlongLockName + "(0x%s, 0x%s)"
	tagAlongLockTemplateHR     = TagAlongLockName + "(target=%s, sender=%s)"
)

// TODO randomize access to purgeable tag-along outputs and incentivize ledger cleanup

//go:embed def/lock_tag_along.easyfl
var tagAlongLockConstraintSource string

func TagAlongLockFromBytesWithLib(data []byte, lib *Library) (*TagAlongLock, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 2)
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

	senderIDbin := easyfl.StripDataPrefix(args[1])
	if len(senderIDbin) != len(base.SpenderID{}) {
		return nil, fmt.Errorf("wrong sender ID size in TagAlongLock")
	}
	var senderID base.SpenderID
	copy(senderID[:], senderIDbin)

	return &TagAlongLock{
		TargetSequencerID: chainID,
		SenderID:          senderID,
	}, nil
}

func (t *TagAlongLock) Source() string {
	return fmt.Sprintf(tagAlongLockTemplateSource, t.TargetSequencerID.StringHex(), hex.EncodeToString(t.SenderID[:]))
}

func (t *TagAlongLock) String() string {
	return fmt.Sprintf(tagAlongLockTemplateHR, t.TargetSequencerID.String(), hex.EncodeToString(t.SenderID[:]))
}

func (t *TagAlongLock) Bytes() []byte {
	return mustBinFromSource(t.Source())
}

func (t *TagAlongLock) Controllers() []Controller {
	return []Controller{ChainLockFromChainID(t.TargetSequencerID), SigLock(t.SenderID)}
}

func (t *TagAlongLock) Master() Controller {
	return nil
}

func (t *TagAlongLock) Name() string {
	return TagAlongLockName
}

func (t *TagAlongLock) AsLock() Lock {
	return t
}

func NewTagAlongOutput(fee uint64, targetChainID base.ChainID, senderID base.SpenderID) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithTokenBalance(fee)
		o.WithLock(&TagAlongLock{
			TargetSequencerID: targetChainID,
			SenderID:          senderID,
		})
	})
}

// NewTagAlongLockUnlockParams creates unlock params for tag-along lock. 2 bytes:
// the input index of the consumed chain output, and the unlock mode.
// The chain constraint is always at index 2.
func NewTagAlongLockUnlockParams(predChainOutputIndex, unlockMode byte) []byte {
	return []byte{predChainOutputIndex, unlockMode}
}

func registerTagAlongLockConstraint(lib *Library) {
	lib.mustRegisterConstraint(TagAlongLockName, 2, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return TagAlongLockFromBytesWithLib(data, lib)
	})
	lib.mustRegisterLockSerde(TagAlongLockName, func(bytes []byte) (Lock, error) {
		// Use latest library version for library registration parsing
		ret, err := TagAlongLockFromBytesWithLib(bytes, lib)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		chainID := base.RandomChainID()
		senderID := base.SpenderID(SigLockRandom())
		example := &TagAlongLock{
			TargetSequencerID: chainID,
			SenderID:          senderID,
		}
		tagAlongLockBack, err := TagAlongLockFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(EqualConstraints(tagAlongLockBack, example), "inconsistency "+TagAlongLockName)

		_, err = lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
	})
}

// --- helper structure

type TagAlongOutput struct {
	OutputWithID
	*TagAlongLock
}

func (o *TagAlongOutput) IsTagAlongSlot(slot uint32) bool {
	s := o.ID.Slot()
	lib := L(s) // use library from output creation slot
	return slot >= s && slot-s < lib.TagAlongSlots
}

func (o *TagAlongOutput) IsTagAlongReclaimSlot(slot uint32) bool {
	s := o.ID.Slot()
	lib := L(s) // use library from output creation slot
	return slot >= s && slot-s >= lib.TagAlongSlots && slot-s < lib.TagAlongReclaimSlots
}

func (o *TagAlongOutput) IsTagAlongPurgeSlot(slot uint32) bool {
	s := o.ID.Slot()
	lib := L(s) // use library from output creation slot
	return slot >= s && slot-s >= lib.TagAlongReclaimSlots
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
