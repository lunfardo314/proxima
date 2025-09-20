package txbuilder_seq

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

type (
	preParsedTagAlongOutput struct {
		ledger.OutputWithID
		*ledger.TagAlongLock
		SenderHash    [32]byte
		RequestCode   byte
		RequestParams *base.SmallPersistentMap
	}
)

func preParseTagAlongOutput(o ledger.OutputWithID) (ret *preParsedTagAlongOutput, valid bool, reason error) {
	switch lock := o.Output.Lock().(type) {
	case ledger.ChainLock:
		if o.Output.NumConstraints() > 2 {
			reason = fmt.Errorf("chain-locked output can't contain more than 2 constraints, got %d", o.Output.NumConstraints())
			return
		}
		ret = &preParsedTagAlongOutput{OutputWithID: o}
		valid = true
		return
	case *ledger.TagAlongLock:
		if o.Output.NumConstraints() > 4 {
			reason = fmt.Errorf("tag-along lock does not allow output can't contain more than 4 constraints, got %d", o.Output.NumConstraints())
			return
		}
		if lock.SenderLock.Name() != ledger.AddressED25519Name {
			reason = fmt.Errorf("tag-along lock allows only ED25519 address as sender")
			return
		}
		ret = &preParsedTagAlongOutput{
			OutputWithID: o,
			TagAlongLock: lock,
		}
		copy(ret.SenderHash[:], lock.SenderLock.(ledger.AddressED25519))
		if o.Output.NumConstraints() == 2 {
			return
		}
		requestData := o.Output.MustConstraintAt(2)
		if !easyfl.HasInlineDataPrefix(requestData) {
			if o.Output.NumConstraints() > 3 {
				reason = fmt.Errorf("constraint at index 2 must be either inline data or it must be the last one")
			}
			return
		}
		// inline data is interpreted as a request parameters
		requestData = easyfl.StripDataPrefix(requestData)
		var p base.SmallPersistentMap
		p, reason = base.SmallPersistentMapFromBytes(requestData)
		if reason != nil {
			return
		}
		ret.RequestParams = &p
		cmdCode := p.Get(FieldCmdCode)
		if cmdCode == nil || len(cmdCode) != 1 {
			reason = fmt.Errorf("wrong command code field")
			return
		}
		ret.RequestCode = cmdCode[0]
	default:
		reason = fmt.Errorf("can't be interpreted as a tag-along output")
		return
	}
	return
}
