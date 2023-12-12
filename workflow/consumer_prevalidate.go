package workflow

import (
	"time"

	"github.com/lunfardo314/proxima/global"
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
	inp.eventCallback(PreValidateConsumerName+".in", inp.tx)

	var err error
	// time bounds are checked if it is not an insider transaction, and it is not in the solidifier pipeline
	// TODO
	enforceTimeBounds := inp.source == TransactionSourceAPI || inp.source == TransactionSourcePeer
	// transaction is rejected if it is too far in the future wrt the local clock
	nowis := time.Now()
	txid := inp.tx.ID()
	timeUpperBound := nowis.Add(c.glb.maxDurationInTheFuture())
	err = inp.tx.Validate(transaction.CheckTimestampUpperBound(timeUpperBound))
	if err != nil {
		if enforceTimeBounds {
			c.IncCounter("invalid")
			inp.eventCallback("finish."+PreValidateConsumerName, err)

			c.glb.pullConsumer.stopPulling(txid)
			c.glb.solidifyConsumer.postDropTxID(txid)
			c.glb.PostEventDropTxID(inp.tx.ID(), PreValidateConsumerName, "%v", err)
			return
		}
		c.Warnf(inp.PrimaryTransactionData, "checking time bounds: '%v'", err)
	}
	// run remaining validations on the transaction
	if err = inp.tx.Validate(transaction.MainTxValidationOptions...); err != nil {
		c.IncCounter("invalid")

		c.glb.pullConsumer.stopPulling(txid)
		c.glb.solidifyConsumer.postDropTxID(txid)
		c.glb.PostEventDropTxID(inp.tx.ID(), PreValidateConsumerName, "%v", err)
		return
	}
	c.IncCounter("ok")

	{ // tracing big tx
		const traceBigTx = false
		if traceBigTx {
			if inp.tx.NumInputs() >= 100 {
				global.SetTracePull(true)
				global.SetTraceTx(true)
				inp.PrimaryTransactionData.traceFlag = true
				c.traceTx(inp.PrimaryTransactionData, ">>>>>>>>> logging as big one, num inputs %d, source: '%s'",
					inp.tx.NumInputs(), inp.source.String())
			}
		}
	}

	if inp.source == TransactionSourceStore && !inp.tx.IsSequencerMilestone() {
		// it is from the tx store, and it is not a milestone, jump right to append it as virtual tx,
		// bypass validation and solidification. We cannot pass sequencer transactions directly, they must be solidified
		// in the utangle down to the baseline branch!!!
		inp.PrimaryTransactionData.makeVirtualTx = true
		c.glb.appendTxConsumer.Push(&AppendTxConsumerInputData{
			PrimaryTransactionData: inp.PrimaryTransactionData,
		})
		return
	}

	c.GossipTransactionIfNeeded(inp.PrimaryTransactionData)

	out := &SolidifyInputData{
		PrimaryTransactionData: inp.PrimaryTransactionData,
	}
	// passes transaction for solidification
	// - immediately if timestamp is in the past
	// - with delay if timestamp is in the future
	txTime := inp.tx.TimestampTime()

	if txTime.Before(nowis) {
		// timestamp is in the past, pass it to the solidifier
		c.Debugf(inp.PrimaryTransactionData, "->"+c.glb.solidifyConsumer.Name())
		c.IncCounter("ok.now")
		c.evidenceBranch(inp.tx)
		c.glb.solidifyConsumer.Push(out)
		return
	}
	// timestamp is in the future. Put it into the waiting room
	c.IncCounter("ok.delay")
	c.Debugf(inp.PrimaryTransactionData, "-> waitingRoom for %v", txTime.Sub(nowis))

	c.glb.preValidateConsumer.waitingRoom.RunAfterDeadline(txTime, func() {
		c.IncCounter("ok.release")
		c.Debugf(inp.PrimaryTransactionData, "release from waiting room")
		c.evidenceBranch(inp.tx)
		c.glb.solidifyConsumer.Push(out)
	})
}

func (c *PreValidateConsumer) evidenceBranch(tx *transaction.Transaction) {
	if tx.IsBranchTransaction() {
		c.glb.utxoTangle.SyncData().EvidenceIncomingBranch(tx.ID(), tx.SequencerTransactionData().SequencerID)
	}
}
