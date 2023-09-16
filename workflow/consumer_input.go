package workflow

import (
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/seenset"
	"github.com/lunfardo314/proxima/util/txlog"
)

// PrimaryInputConsumer is where transaction enters the workflow pipeline

const PrimaryInputConsumerName = "input"

// PrimaryInputConsumerData is a basic data of the raw transaction
type (
	// PrimaryInputConsumerData is an input message type for this consumer
	PrimaryInputConsumerData struct {
		Tx      *transaction.Transaction
		txLog   *txlog.TransactionLog
		insider bool // for insider transactions do not check time bounds, only report as warning
	}

	PrimaryConsumer struct {
		*Consumer[*PrimaryInputConsumerData]
		seen *seenset.SeenSet[core.TransactionID]
	}
)

// EventCodeDuplicateTx this consumer rises the event with transaction ID as a parameter whenever duplicate is detected
var EventCodeDuplicateTx = eventtype.RegisterNew[*core.TransactionID]("duplicateTx")

// initPrimaryInputConsumer initializes the consumer
func (w *Workflow) initPrimaryInputConsumer() {
	c := &PrimaryConsumer{
		Consumer: NewConsumer[*PrimaryInputConsumerData](PrimaryInputConsumerName, w),
		seen:     seenset.New[core.TransactionID](),
	}
	c.AddOnConsume(func(inp *PrimaryInputConsumerData) {
		// tracing every input message
		c.Debugf(inp, "IN")
	})
	c.AddOnConsume(c.consume) // process input
	c.AddOnClosed(func() {
		// cleanup on close
		w.preValidateConsumer.Stop()
		w.terminateWG.Done()
	})

	nmDuplicate := EventCodeDuplicateTx.String()
	w.MustOnEvent(EventCodeDuplicateTx, func(txid *core.TransactionID) {
		// log duplicate transaction upon event
		c.glb.IncCounter(c.Name() + "." + nmDuplicate)
		c.Log().Debugf("%s: %s", nmDuplicate, txid.Short())
	})
	// the consumer is globally known in the workflow
	w.primaryInputConsumer = c
}

// consume processes the input
func (c *PrimaryConsumer) consume(inp *PrimaryInputConsumerData) {
	// the input is preparse transaction with base validation ok. It means it is identifiable as a transaction
	if c.isDuplicate(inp.Tx.ID()) {
		// if duplicate, rise the event
		c.glb.PostEvent(EventCodeDuplicateTx, inp.Tx.ID())
		return
	}
	c.glb.IncCounter(c.Name() + ".out")
	// passes identifiable transaction which is not a duplicate to the pre-validation consumer
	c.glb.preValidateConsumer.Push(&PreValidateConsumerInputData{
		PrimaryInputConsumerData: inp,
	})
}

func (c *PrimaryConsumer) isDuplicate(txid *core.TransactionID) bool {
	if c.glb.utxoTangle.HasTransactionOnTangle(txid) {
		c.glb.IncCounter(c.Name() + ".duplicate.tangle")
		c.Log().Debugf("already on tangle -- " + txid.Short())
		return true
	}
	if c.seen.Seen(*txid) {
		c.glb.IncCounter(c.Name() + ".duplicate.seen")
		c.Log().Debugf("already seen -- " + txid.String())
		return true
	}
	return false
}

func (w *Workflow) TransactionIn(txBytes []byte) error {
	_, err := w.transactionInWithOptions(txBytes, false, false, "", func(inData *PrimaryInputConsumerData) error {
		w.primaryInputConsumer.Push(inData)
		return nil
	})
	return err
}

// TransactionInInsider explicitly states do not enforce time bounds
func (w *Workflow) TransactionInInsider(txBytes []byte) error {
	_, err := w.transactionInWithOptions(txBytes, true, false, "", func(inData *PrimaryInputConsumerData) error {
		w.primaryInputConsumer.Push(inData)
		return nil
	})
	return err
}

func (w *Workflow) TransactionInWithLog(txBytes []byte, givenLogName string) error {
	_, err := w.transactionInWithOptions(txBytes, false, true, givenLogName, func(inData *PrimaryInputConsumerData) error {
		w.primaryInputConsumer.Push(inData)
		return nil
	})
	return err
}

func (w *Workflow) transactionInWithOptions(txBytes []byte, insider bool, logit bool, givenLogName string, doFun func(*PrimaryInputConsumerData) error) (*transaction.Transaction, error) {
	util.Assertf(w.IsRunning(), "workflow has not been started yet")
	out := &PrimaryInputConsumerData{insider: insider}
	var err error
	// base validation
	out.Tx, err = transaction.FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	// if raw transaction data passes the basic check, it means it is identifiable as a transaction and main properties
	// are correct: ID, timestamp, sequencer and branch transaction flags. The signature and semantic has not been checked yet
	if logit || w.logTransaction.Load() {
		out.txLog = txlog.NewTransactionLog(out.Tx.ID(), givenLogName)
	}
	return out.Tx, doFun(out)
}

// TransactionInWaitAppendSyncTx for testing
func (w *Workflow) TransactionInWaitAppendSyncTx(txBytes []byte, insider ...bool) (*transaction.Transaction, error) {
	special := false
	if len(insider) > 0 {
		special = insider[0]
	}
	var wg sync.WaitGroup
	wg.Add(1)

	var err error
	tx, err1 := w.transactionInWithOptions(txBytes, special, false, "", func(inData *PrimaryInputConsumerData) error {
		err2 := w.defaultTxStatusWaitingList.OnTransactionStatus(inData.Tx.ID(), func(statusMsg *TxStatusMsg) {
			defer wg.Done()

			if statusMsg == nil {
				err = fmt.Errorf("timeout on appending %s", inData.Tx.IDShort())
				return
			}
			if !statusMsg.Appended {
				err = fmt.Errorf("transaction %s has been rejected: '%s'", inData.Tx.IDShort(), statusMsg.Msg)
				return
			}
		})
		if err2 == nil {
			w.primaryInputConsumer.Push(inData)
		}
		return err2
	})
	if err1 != nil {
		return nil, err1
	}
	wg.Wait()
	if err != nil {
		return tx, err
	}
	return tx, nil
}

func (w *Workflow) TransactionInWaitAppendSync(txBytes []byte, insider ...bool) (*utangle.WrappedTx, error) {
	tx, err := w.TransactionInWaitAppendSyncTx(txBytes, insider...)
	if err != nil {
		return nil, err
	}
	return w.utxoTangle.MustGetVertex(tx.ID()), nil
}
