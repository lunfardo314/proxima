package txbuilder_seq

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

type (
	preParsedTagAlongOutput struct {
		Output        ledger.TagAlongOutput
		SenderHash    [32]byte
		RequestCode   byte
		RequestParams *base.SmallPersistentMap
	}
)

func preParseTagAlongOutput(o *ledger.Output) (valid bool, reason error) {
	if o.NumConstraints() > 4 {
		reason = fmt.Errorf("can't contain more than 4 constraints, got %d", o.NumConstraints())
		return
	}
	lock := o.Lock()
	switch lock := lock.(type) {
	case ledger.ChainLock:
	case *ledger.TagAlongLock:
	default:
		reason = fmt.Errorf("not a tag-along output")
		return
	}
}
