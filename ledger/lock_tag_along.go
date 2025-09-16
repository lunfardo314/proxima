package ledger

import (
	"encoding/hex"
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
	TagAlongLockName           = "tag_along"
	tagAlongLockTemplateSource = TagAlongLockName + "(0x%s, 0x%s)"
	tagAlongLockTemplateHR     = TagAlongLockName + "(%s, %s)"
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

	senderBin := easyfl.StripDataPrefix(args[1])
	sender, err := AccountableFromBytes(senderBin)
	if err != nil {
		return nil, err
	}

	return &TagAlongLock{
		TargetSequencerID: chainID,
		SenderLock:        sender,
	}, nil
}

func (t *TagAlongLock) Source() string {
	return fmt.Sprintf(tagAlongLockTemplateSource, t.TargetSequencerID.StringHex(), hex.EncodeToString(t.SenderLock.Bytes()))
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

const tagAlongLockConstraintSource = `
func tag_along : concat($0,$1)
`
