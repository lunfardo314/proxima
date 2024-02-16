package attacher

import (
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
)

func OptionWithTransactionMetadata(metadata *txmetadata.TransactionMetadata) Option {
	return func(options *_attacherOptions) {
		options.metadata = metadata
	}
}

func OptionWithAttachmentCallback(fun func(vid *vertex.WrappedTx, err error)) Option {
	return func(options *_attacherOptions) {
		options.attachmentCallback = fun
	}
}

func OptionPullNonBranch(options *_attacherOptions) {
	options.pullNonBranch = true
}

func OptionDoNotLoadBranch(options *_attacherOptions) {
	options.doNotLoadBranch = true
}

func OptionInvokedBy(name string) Option {
	return func(options *_attacherOptions) {
		options.calledBy = name
	}
}
