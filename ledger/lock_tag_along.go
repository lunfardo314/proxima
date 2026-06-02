package ledger

import (
	"encoding/hex"
	"fmt"
	"sync"

	_ "embed"

	"github.com/lunfardo314/proxima/ledger/base"
)

// TagAlongLock is a typed wrapper for the (sender, target) pair on a
// tag-along output. Both fields live in the index-value tuple at output
// element index 1: position 0 = senderID (master-first §4.1 convention),
// position 1 = targetSequencerID. The bytecode at index 2 is the
// per-kind constant `tagAlong` 0-arg call (TagAlongBytecode).
type TagAlongLock struct {
	TargetSequencerID base.ChainID
	SenderID          base.HolderID
}

const TagAlongLockName = "tagAlong"

//go:embed def/lock_tag_along.easyfl
var tagAlongLockConstraintSource string

// TagAlongBytecode returns the bytecode of the public 0-arg `tagAlong`
// constraint at output element index 2. Constant for all tag-along
// outputs (sender / target live in the index-value tuple at index 1).
var (
	tagAlongBytecodeOnce  sync.Once
	tagAlongBytecodeCache []byte
)

func TagAlongBytecode() []byte {
	tagAlongBytecodeOnce.Do(func() {
		tagAlongBytecodeCache = mustBinFromSource(TagAlongLockName)
	})
	return tagAlongBytecodeCache
}

func (t *TagAlongLock) String() string {
	return fmt.Sprintf("tagAlong(target=%s, sender=%s)",
		t.TargetSequencerID.String(), hex.EncodeToString(t.SenderID[:]))
}

// IndexValues returns [senderID, targetSequencerID]. Sender is at position
// 0 per the §4.1 master-first convention; for tagAlong the sender plays
// the master role.
func (t *TagAlongLock) IndexValues() [][]byte {
	return [][]byte{t.SenderID[:], t.TargetSequencerID[:]}
}

func (t *TagAlongLock) Name() string         { return TagAlongLockName }
func (t *TagAlongLock) LockBytecode() []byte { return TagAlongBytecode() }

// NewTagAlongLockUnlockParams creates unlock params for tag-along lock. 2 bytes:
// the input index of the consumed chain output, and the unlock mode.
func NewTagAlongLockUnlockParams(predChainOutputIndex, unlockMode byte) []byte {
	return []byte{predChainOutputIndex, unlockMode}
}

// NewTagAlongOutput builds an output with the given fee and tag-along
// lock (target sequencer + sender).
func NewTagAlongOutput(fee uint64, targetChainID base.ChainID, senderID base.HolderID) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithTokenBalance(fee)
		o.WithLock(&TagAlongLock{
			TargetSequencerID: targetChainID,
			SenderID:          senderID,
		})
	})
}

func registerTagAlongLockConstraint(lib *Library) {
	lib.registerLockKind(TagAlongLockName)
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
