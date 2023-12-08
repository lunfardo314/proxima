package workflow

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

func (w *Workflow) TransactionIn(txBytes []byte, opts ...TransactionInOption) error {
	_, err := w.TransactionInReturnTx(txBytes, opts...)
	return err
}

func (w *Workflow) TransactionInReturnTx(txBytes []byte, opts ...TransactionInOption) (*transaction.Transaction, error) {
	if !w.IsRunning() {
		return nil, fmt.Errorf("TransactionInReturnTx: workflow is inactive")
	}
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	// if raw transaction data passes the basic check, it means it is identifiable as a transaction and main properties
	// are correct: ID, timestamp, sequencer and branch transaction flags. The signature and semantic has not been checked yet

	inData := newPrimaryInputConsumerData(tx)
	for _, opt := range opts {
		opt(inData)
	}
	// once tx reached the node, stop pulling
	inData.wasPulled = w.pullConsumer.stopPulling(tx.ID())
	// prevent unnecessary dissemination via gossip
	inData.doNotGossip = inData.wasPulled || inData.source == TransactionSourceStore

	w.primaryInputConsumer.Push(inData)
	return tx, nil
}

func newPrimaryInputConsumerData(tx *transaction.Transaction) *PrimaryTransactionData {
	return &PrimaryTransactionData{
		tx:            tx,
		receivedWhen:  time.Now(),
		source:        TransactionSourceAPI,
		eventCallback: func(_ string, _ any) {},
	}
}

func WithTransactionSource(src TransactionSource) TransactionInOption {
	return func(data *PrimaryTransactionData) {
		data.source = src
	}
}

func WithTransactionSourcePeer(from peer.ID) TransactionInOption {
	return func(data *PrimaryTransactionData) {
		data.source = TransactionSourcePeer
		data.receivedFromPeer = from
	}
}

func WithTraceCondition(cond func(tx *transaction.Transaction, src TransactionSource, rcv peer.ID) bool) TransactionInOption {
	return func(data *PrimaryTransactionData) {
		data.traceFlag = cond(data.tx, data.source, data.receivedFromPeer)
	}
}

var OptionWithSourceSequencer = WithTransactionSource(TransactionSourceSequencer)

func WithWorkflowEventCallback(fun func(event string, data any)) TransactionInOption {
	return func(data *PrimaryTransactionData) {
		prev := data.eventCallback
		data.eventCallback = func(event string, data any) {
			prev(event, data)
			fun(event, data)
		}
	}
}

func WithOnWorkflowEventPrefix(eventPrefix string, fun func(event string, data any)) TransactionInOption {
	return WithWorkflowEventCallback(func(event string, data any) {
		if strings.HasPrefix(event, eventPrefix) {
			fun(event, data)
		}
	})
}

func decodeError(errData any) error {
	if util.IsNil(errData) {
		return nil
	}
	switch dataStr := errData.(type) {
	case interface{ Error() string }:
		return dataStr
	case string:
		return errors.New(dataStr)
	case interface{ Short() string }:
		return errors.New(dataStr.Short())
	case interface{ IDShort() string }:
		return errors.New(dataStr.IDShort())
	case interface{ String() string }:
		return errors.New(dataStr.String())
	default:
		return fmt.Errorf("decodeError: unsupported data type: %T", errData)
	}
}

func (w *Workflow) TransactionInWaitAppend(txBytes []byte, timeout time.Duration, opts ...TransactionInOption) (*transaction.Transaction, error) {
	errCh := make(chan error)
	var closed bool
	var closeMutex sync.Mutex
	var err error
	waitFailOpt := WithOnWorkflowEventPrefix("finish", func(event string, data any) {
		err1 := decodeError(data)

		closeMutex.Lock()
		defer closeMutex.Unlock()

		if !closed {
			errCh <- err1
		}
	})

	defer func() {
		closeMutex.Lock()
		defer closeMutex.Unlock()

		close(errCh)
		closed = true
	}()

	opts = append(opts, waitFailOpt)
	tx, err := w.TransactionInReturnTx(txBytes, opts...)
	if err != nil {
		return nil, err
	}
	select {
	case err = <-errCh:
		return tx, err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout of %v exceed", timeout)
	}
}

func (w *Workflow) TransactionInWaitAppendWrap(txBytes []byte, timeout time.Duration, opts ...TransactionInOption) (*utangle.WrappedTx, error) {
	tx, err := w.TransactionInWaitAppend(txBytes, timeout, opts...)
	if err != nil {
		return nil, err
	}
	return w.utxoTangle.MustGetVertex(tx.ID()), nil
}
