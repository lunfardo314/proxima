package utangle_new

import (
	"context"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
)

type (
	Solidifier interface {
		Run(ctx context.Context)
		Notify(any)
	}

	_sequencerSolidifier struct {
		vid            *WrappedTx
		inChan         chan any
		getVertex      func(txid *core.TransactionID, ifAbsent func()) *WrappedTx
		pull           func(txid *core.TransactionID)
		baselineState  func() multistate.SugaredStateReader
		withGlobalLock func(func())
	}

	_branchSolidifier struct {
		_sequencerSolidifier
	}
)

func (s *_sequencerSolidifier) Run(ctx context.Context) {
	go s.run(ctx)
}

const periodicCheckEach = 500 * time.Millisecond

func (s *_sequencerSolidifier) run(ctx context.Context) {
	s.vid.Unwrap(UnwrapOptions{
		Vertex: nil,
		TxID: func(txid *core.TransactionID) {
			s.pull(txid)
		},
		Deleted: s.vid.PanicAccessDeleted,
	})
	s.check()
	for status := s.vid.GetTxStatus(); status == TxStatusUndefined; status = s.vid.GetTxStatus() {
		select {
		case <-ctx.Done():
			return
		case msg := <-s.inChan:
			s.processMsg(msg)
		case <-time.After(periodicCheckEach):
			s.check()
		}
	}
	s.withGlobalLock(func() {

	})
}

func (s *_sequencerSolidifier) processMsg(msg any) {

}

func (s *_sequencerSolidifier) check() {

}

func (s *_sequencerSolidifier) Notify(msg any) {
	s.inChan <- msg
}

func (s *_branchSolidifier) Run(ctx context.Context) {
	s._sequencerSolidifier.Run(ctx)
}
