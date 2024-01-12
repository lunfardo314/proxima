package attacher

import (
	"context"

	"github.com/lunfardo314/proxima/core/vertex"
)

func OptionWithContext(ctx context.Context) Option {
	return func(options *_attacherOptions) {
		options.ctx = ctx
	}
}

func OptionWithAttachmentCallback(fun func(vid *vertex.WrappedTx)) Option {
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
