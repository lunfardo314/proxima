package txbuilder_seq

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/smallkv"
)

type (
	preParsedTagAlongOutput struct {
		ledger.TagAlongOutput
		SenderID      base.HolderID
		RequestCode   byte
		RequestParams *smallkv.Map
	}

	outputParser func(txb *SeqTxBuilder, o *preParsedTagAlongOutput) (cmd TxBuilderCommand, valid bool, err error)
)

const FieldCmdCode = byte(0)

var _cmdParsers = map[byte]outputParser{
	RequestCodeNoop:              noRequestCmdParser,
	RequestCodeSetSequencerData:  setSequencerDataOutputParser,
	RequestCodeWithdrawFromSeq:   withdrawFromSeqRequestParser,
	RequestCodeAskStopDelegation: parseAskStopDelegationOutput,
	RequestCodeTopUpDelegation:   parseTopUpDelegationOutput,
}

func preParseOutputAsTagAlong(o ledger.OutputWithID) (ret preParsedTagAlongOutput, valid bool, reason error) {
	ret.OutputWithID = o
	ret.RequestCode = RequestCodeNoop

	switch lock := o.Output.Lock().(type) {
	case ledger.ChainLock:
		if o.Output.NumElements() > 3 {
			reason = fmt.Errorf("chain-locked output can't contain more than 3 UTXO elements, got %d", o.Output.NumElements())
			return
		}
		valid = true
		return
	case *ledger.TagAlongLock:
		if o.Output.NumElements() > 5 {
			reason = fmt.Errorf("tag-along lock does not allow output can't contain more than 5 UTXO elements, got %d", o.Output.NumElements())
			return
		}
		ret.TagAlongLock = lock
		ret.SenderID = lock.SenderID
		if o.Output.NumElements() == 3 {
			valid = true
			return
		}
		requestData := o.Output.MustConstraintAt(3)
		if !easyfl.HasInlineDataPrefix(requestData) {
			if o.Output.NumElements() > 4 {
				reason = fmt.Errorf("UTXO element at index 3 must be either inline data or it must be the last one")
			}
			return
		}
		// inline data is interpreted as a request parameters
		requestData = easyfl.StripDataPrefix(requestData)
		var p smallkv.Map
		p, reason = smallkv.FromBytes(requestData)
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
		if o.Output.NumElements() == 5 && !isGuarantorOf(ret.RequestCode, o.Output.MustConstraintAt(4)) {
			// element 4 is evaluated in the milestone: anything but the request's own guarantor
			// could refuse every sequencer transaction consuming the request
			reason = fmt.Errorf("UTXO element at index 4 must be the guarantor of the request")
			return
		}
		valid = true
		return
	}
	reason = fmt.Errorf("can't be interpreted as a tag-along output")
	return
}

// isGuarantorOf is true when data is the guarantor a request of the given code may carry at
// element 4: ensureStopDelegation on a stop request, ensureTopUpDelegation on a top-up.
func isGuarantorOf(requestCode byte, data []byte) bool {
	lib := ledger.L(base.MaxSlot)
	switch requestCode {
	case RequestCodeAskStopDelegation:
		_, err := ledger.EnsureStopDelegationFromBytesWithLib(data, lib)
		return err == nil
	case RequestCodeTopUpDelegation:
		_, err := ledger.EnsureTopUpDelegationFromBytesWithLib(data, lib)
		return err == nil
	}
	return false
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
