package workflow

import (
	"time"

	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util/wait"
)

// PreValidateConsumer performs syntactic and semantic checks on te basic structure of the transaction
// before solidification and full validation

const PreValidateConsumerName = "prevalid"

type (
	// PreValidateConsumerInputData input data type of the consumer
	PreValidateConsumerInputData struct {
		*PrimaryInputConsumerData
	}

	PreValidateConsumer struct {
		*Consumer[*PreValidateConsumerInputData]
		// waiting room for transaction which came too early wrt to the local clock
		waitingRoom *wait.Delay
	}
)

func (w *Workflow) initPreValidateConsumer() {
	c := &PreValidateConsumer{
		Consumer:    NewConsumer[*PreValidateConsumerInputData](PreValidateConsumerName, w),
		waitingRoom: wait.NewDelay(),
	}
	c.AddOnConsume(func(inp *PreValidateConsumerInputData) {
		// trace each input message
		c.Debugf(inp.PrimaryInputConsumerData, "IN")
	})
	c.AddOnConsume(c.consume) // process the input message
	c.AddOnClosed(func() {
		// cleanup on close
		c.waitingRoom.Stop()
		w.solidifyConsumer.Stop()
		w.terminateWG.Done()
	})
	w.preValidateConsumer = c
}

// process the input message
func (c *PreValidateConsumer) consume(inp *PreValidateConsumerInputData) {
	inp.eventCallback(PreValidateConsumerName+".in", inp.Tx)

	var err error
	// time bounds are checked if it is not an insider transaction, and it is not in the solidifier pipeline
	enforceTimeBounds := inp.Source == TransactionSourceAPI ||
		inp.Source == TransactionSourcePeer ||
		c.glb.solidifyConsumer.IsWaitedTransaction(inp.Tx.ID())

	// transaction is rejected if it is too far in the future wrt the local clock
	nowis := time.Now()
	timeUpperBound := nowis.Add(c.glb.maxDurationInTheFuture())
	err = inp.Tx.Validate(transaction.CheckTimestampUpperBound(timeUpperBound))
	if err != nil {
		if enforceTimeBounds {
			c.IncCounter("invalid")
			inp.eventCallback("finish."+PreValidateConsumerName, err)
			c.RejectTransaction(inp.PrimaryInputConsumerData, "%v", err)
			return
		}
		c.Warnf(inp.PrimaryInputConsumerData, "checking time bounds: '%v'", err)
	}
	// run remaining validations on the transaction
	if err = inp.Tx.Validate(transaction.MainTxValidationOptions...); err != nil {
		c.IncCounter("invalid")
		c.RejectTransaction(inp.PrimaryInputConsumerData, "%v", err)
		return
	}
	// early check for rejected inputs
	for txid := range inp.Tx.InputTransactionIDs() {
		if c.glb.IsRejected(&txid) {
			c.IncCounter("invalidInput")
			c.RejectTransaction(inp.PrimaryInputConsumerData, "contains rejected input txID %s", txid.String())
			return
		}
	}
	c.IncCounter("ok")

	out := &SolidifyInputData{
		PrimaryInputConsumerData: inp.PrimaryInputConsumerData,
	}
	// passes transaction for solidification
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := inp.Tx.TimestampTime()

	if txTime.Before(nowis) {
		// timestamp is in the past, pass it to the solidifier
		c.Debugf(inp.PrimaryInputConsumerData, "->"+c.glb.solidifyConsumer.Name())
		c.IncCounter("ok.now")
		c.glb.solidifyConsumer.Push(out)
		return
	}
	// timestamp is in the future. Put it into the waiting room
	c.IncCounter("ok.delay")
	c.Debugf(inp.PrimaryInputConsumerData, "-> waitingRoom for %v", txTime.Sub(nowis))

	c.glb.preValidateConsumer.waitingRoom.RunAfterDeadline(txTime, func() {
		c.IncCounter("ok.release")
		c.Debugf(inp.PrimaryInputConsumerData, "release from waiting room")
		c.glb.solidifyConsumer.Push(out)
	})
}
