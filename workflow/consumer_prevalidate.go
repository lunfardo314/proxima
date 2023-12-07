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
		*PrimaryTransactionData
	}

	PreValidateConsumer struct {
		*Consumer[*PreValidateConsumerInputData]
		// waiting room for transaction which came too early wrt to the local clock
		waitingRoom *wait.Delay
	}
)

func (w *Workflow) initPreValidateConsumer() {
	ret := &PreValidateConsumer{
		Consumer:    NewConsumer[*PreValidateConsumerInputData](PreValidateConsumerName, w),
		waitingRoom: wait.NewDelay(),
	}
	ret.AddOnConsume(func(inp *PreValidateConsumerInputData) {
		// trace each input message
		ret.traceTx(inp.PrimaryTransactionData, "IN")
	})
	ret.AddOnConsume(ret.consume) // process the input message
	ret.AddOnClosed(func() {
		// cleanup on close
		ret.waitingRoom.Stop()
		w.txGossipOutConsumer.Stop()
		w.solidifyConsumer.Stop()
	})
	w.preValidateConsumer = ret
}

// process the input message
// TODO check lower time bounds
func (c *PreValidateConsumer) consume(inp *PreValidateConsumerInputData) {
	inp.eventCallback(PreValidateConsumerName+".in", inp.Tx)

	var err error
	// time bounds are checked if it is not an insider transaction, and it is not in the solidifier pipeline
	// TODO
	enforceTimeBounds := inp.Source == TransactionSourceAPI || inp.Source == TransactionSourcePeer
	// transaction is rejected if it is too far in the future wrt the local clock
	nowis := time.Now()
	timeUpperBound := nowis.Add(c.glb.maxDurationInTheFuture())
	err = inp.Tx.Validate(transaction.CheckTimestampUpperBound(timeUpperBound))
	if err != nil {
		if enforceTimeBounds {
			c.IncCounter("invalid")
			inp.eventCallback("finish."+PreValidateConsumerName, err)

			c.glb.pullConsumer.removeFromPullList(inp.Tx.ID())
			c.glb.solidifyConsumer.postRemoveTxIDs(inp.Tx.ID())
			c.glb.PostEventDropTxID(inp.Tx.ID(), PreValidateConsumerName, "%v", err)
			return
		}
		c.Warnf(inp.PrimaryTransactionData, "checking time bounds: '%v'", err)
	}
	// run remaining validations on the transaction
	if err = inp.Tx.Validate(transaction.MainTxValidationOptions...); err != nil {
		c.IncCounter("invalid")

		c.glb.pullConsumer.removeFromPullList(inp.Tx.ID())
		c.glb.solidifyConsumer.postRemoveTxIDs(inp.Tx.ID())
		c.glb.PostEventDropTxID(inp.Tx.ID(), PreValidateConsumerName, "%v", err)
		return
	}
	c.IncCounter("ok")

	c.GossipTransactionIfNeeded(inp.PrimaryTransactionData)

	out := &SolidifyInputData{
		PrimaryTransactionData: inp.PrimaryTransactionData,
	}
	// passes transaction for solidification
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := inp.Tx.TimestampTime()

	if txTime.Before(nowis) {
		// timestamp is in the past, pass it to the solidifier
		c.Debugf(inp.PrimaryTransactionData, "->"+c.glb.solidifyConsumer.Name())
		c.IncCounter("ok.now")
		c.evidenceBranch(inp.Tx)
		c.glb.solidifyConsumer.Push(out)
		return
	}
	// timestamp is in the future. Put it into the waiting room
	c.IncCounter("ok.delay")
	c.Debugf(inp.PrimaryTransactionData, "-> waitingRoom for %v", txTime.Sub(nowis))

	c.glb.preValidateConsumer.waitingRoom.RunAfterDeadline(txTime, func() {
		c.IncCounter("ok.release")
		c.Debugf(inp.PrimaryTransactionData, "release from waiting room")
		c.evidenceBranch(inp.Tx)
		c.glb.solidifyConsumer.Push(out)
	})
}

func (c *PreValidateConsumer) evidenceBranch(tx *transaction.Transaction) {
	if tx.IsBranchTransaction() {
		c.glb.utxoTangle.SyncData().EvidenceIncomingBranch(tx.ID(), tx.SequencerTransactionData().SequencerID)
	}
}
