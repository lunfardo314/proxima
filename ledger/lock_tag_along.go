package ledger

import (
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type TagAlongLock struct {
	TargetSequencerID base.ChainID
	Sender            Accountable
}

const (
	TagAlongLockName           = "tagAlong"
	tagAlongLockTemplateSource = TagAlongLockName + "(0x%s, %s)"
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

	sender, err := AccountableFromBytesWithLib(args[1], lib)
	if err != nil {
		return nil, err
	}

	return &TagAlongLock{
		TargetSequencerID: chainID,
		Sender:            sender,
	}, nil
}

func (t *TagAlongLock) Source() string {
	return fmt.Sprintf(tagAlongLockTemplateSource, t.TargetSequencerID.StringHex(), t.Sender.Source())
}

func (t *TagAlongLock) String() string {
	return fmt.Sprintf(tagAlongLockTemplateHR, t.TargetSequencerID.String(), t.Sender.String())
}

func (t *TagAlongLock) Bytes() []byte {
	return mustBinFromSource(t.Source())
}

func (t *TagAlongLock) Accounts() []Accountable {
	return []Accountable{ChainLockFromChainID(t.TargetSequencerID), t.Sender}
}

func (t *TagAlongLock) Master() Accountable {
	return nil
}

func (t *TagAlongLock) Name() string {
	return TagAlongLockName
}

func (t *TagAlongLock) AsLock() Lock {
	return t
}

func NewTagAlongOutput(fee uint64, targetChainID base.ChainID, sender AddressED25519) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithTokenBalance(fee)
		o.WithLock(&TagAlongLock{
			TargetSequencerID: targetChainID,
			Sender:            sender,
		})
	})
}

func NewTagAlongLockUnlockParams(predChainOutputIndex, predChainConstraintIndex, unlockMode byte) []byte {
	return []byte{predChainOutputIndex, predChainConstraintIndex, unlockMode}
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
		sender := AddressED25519Random()
		example := &TagAlongLock{
			TargetSequencerID: chainID,
			Sender:            sender,
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
