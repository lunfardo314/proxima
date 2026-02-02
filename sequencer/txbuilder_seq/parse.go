package txbuilder_seq

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

type (
	preParsedTagAlongOutput struct {
		ledger.TagAlongOutput
		SenderID      base.SpenderID
		RequestCode   byte
		RequestParams *base.SmallPersistentMap
	}

	outputParser func(txb *SeqTxBuilder, o *preParsedTagAlongOutput) (cmd TxBuilderCommand, valid bool, err error)
)

const FieldCmdCode = byte(0)

var _cmdParsers = map[byte]outputParser{
	RequestCodeNoop:              noRequestCmdParser,
	RequestCodeSetSequencerData:  setSequencerDataOutputParser,
	RequestCodeWithdrawFromSeq:   withdrawFromSeqRequestParser,
	RequestCodeAskStopDelegation: parseAskStopDelegationOutput,
}

func preParseOutputAsTagAlong(o ledger.OutputWithID) (ret preParsedTagAlongOutput, valid bool, reason error) {
	ret.OutputWithID = o
	ret.RequestCode = RequestCodeNoop

	switch lock := o.Output.Lock().(type) {
	case ledger.ChainLock:
		if o.Output.NumConstraints() > 2 {
			reason = fmt.Errorf("chain-locked output can't contain more than 2 constraints, got %d", o.Output.NumConstraints())
			return
		}
		valid = true
		return
	case *ledger.TagAlongLock:
		if o.Output.NumConstraints() > 4 {
			reason = fmt.Errorf("tag-along lock does not allow output can't contain more than 4 constraints, got %d", o.Output.NumConstraints())
			return
		}
		if lock.Sender.Name() != ledger.SigLockName {
			reason = fmt.Errorf("tag-along lock allows only sigLock as sender")
			return
		}
		ret.TagAlongLock = lock
		ret.SenderID = base.SpenderID(lock.Sender)
		if o.Output.NumConstraints() == 2 {
			valid = true
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
		reqCode := p.Get(FieldCmdCode)
		if reqCode == nil || len(reqCode) != 1 || reqCode[0] == RequestCodeNoop {
			reason = fmt.Errorf("wrong command code field")
			return
		}
		ret.RequestCode = reqCode[0]
		valid = true
		return
	}
	reason = fmt.Errorf("can't be interpreted as a tag-along output")
	return
}

func (txb *SeqTxBuilder) TxBuilderCommandFromOutput(o ledger.OutputWithID) (cmd TxBuilderCommand, isValid bool, reason error) {
	var preParsed preParsedTagAlongOutput

	if preParsed, isValid, reason = preParseOutputAsTagAlong(o); reason != nil || !isValid {
		reason = fmt.Errorf("TxBuilderCommandFromOutput: %v", reason)
		return
	}
	if preParsed.TagAlongLock != nil && !preParsed.TagAlongOutput.IsTagAlongSlot(txb.Slot()) {
		// missed tag-along slots -> won't be able to consume it in the future either
		reason = fmt.Errorf("TxBuilderCommandFromOutput: missed tag-along window")
		return
	}

	if parser, found := _cmdParsers[preParsed.RequestCode]; found {
		return parser(txb, &preParsed)
	}
	reason = fmt.Errorf("TxBuilderCommandFromOutput: unknown request code %d", preParsed.RequestCode)
	return
}
