package txbuilder_seq

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/lines"
)

type NoRequestTxBuilderCommand struct {
	ledger.TagAlongOutput
}

const RequestCodeNoop = byte(0)

func noRequestCmdParser(_ *SeqTxBuilder, o *preParsedTagAlongOutput) (cmd TxBuilderCommand, valid bool, err error) {
	return &NoRequestTxBuilderCommand{
		TagAlongOutput: o.TagAlongOutput,
	}, true, nil
}

func (c *NoRequestTxBuilderCommand) Apply(txb *SeqTxBuilder) (valid bool, err error) {
	// treated as a usual chain lock
	var idx byte
	if idx, err = txb.ConsumeOutput(c.Output, c.ID); err != nil {
		return
	}
	txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(c.Output.TokenBalance())
	valid = true
	return
}

func (c *NoRequestTxBuilderCommand) Lines(prefix ...string) *lines.Lines {
	ln := c.Output.LinesHR(prefix...)
	return ln
}

func (c *NoRequestTxBuilderCommand) AttachmentCostDelta() int {
	// +1 for the consumed tag-along input
	return 1
}
