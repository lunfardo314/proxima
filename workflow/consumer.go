package workflow

import (
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/consumer"
	"github.com/lunfardo314/unitrie/common"
)

// allConsumerNames non thread-safe
var allConsumerNames = make([]string, 0)

func NewConsumer[T any](name string, wrk *Workflow) *Consumer[T] {
	lvl := wrk.configParams.logLevel
	if l, ok := wrk.configParams.consumerLogLevel[name]; ok {
		lvl = l
	}
	ret := &Consumer[T]{
		Consumer: consumer.NewConsumer[T](name, lvl, wrk.configParams.logOutput),
		glb:      wrk,
	}
	ret.AddOnConsume(func(_ T) {
		wrk.IncCounter(name + ".in")
	})
	ret.AddOnClosed(func() {
		ret.glb.terminateWG.Done()
	})
	allConsumerNames = append(allConsumerNames, name)
	return ret
}

func (c *Consumer[T]) Start() {
	c.glb.terminateWG.Add(1)
	util.RunWrappedRoutine(c.Name(), func() {
		c.Consumer.Run()
	}, func(err error) {
		c.Log().Fatal(err)
	},
		common.ErrDBUnavailable)
}

func (c *Consumer[T]) TxLogPrefix() string {
	return c.Name() + ": "
}

func (c *Consumer[T]) Debugf(inp *PrimaryTransactionData, format string, args ...any) {
	c.Log().Debugf(format+"   "+inp.Tx.IDShort(), args...)
}

func (c *Consumer[T]) Warnf(inp *PrimaryTransactionData, format string, args ...any) {
	c.Log().Warnf(format+"    "+inp.Tx.IDShort(), args...)
}

func (c *Consumer[T]) Infof(inp *PrimaryTransactionData, format string, args ...any) {
	c.Log().Infof(format+"    "+inp.Tx.IDShort(), args...)
}

func (c *Consumer[T]) setTrace(t bool) {
	c.traceFlag = t
}

func (c *Consumer[T]) trace(f string, a ...any) {
	if c.traceFlag {
		c.Log().Infof("TRACE: "+f, util.EvalLazyArgs(a...)...)
	}
}

func (c *Consumer[T]) IncCounter(name string) {
	c.glb.IncCounter(c.Name() + "." + name)
}

func (c *Consumer[T]) InfoStr() string {
	p, l := c.Info()
	return fmt.Sprintf("pushCount: %d, queueLen: %d", p, l)
}

func (c *Consumer[T]) GossipTransactionIfNeeded(inp *PrimaryTransactionData) {
	if inp.WasGossiped || !c.glb.utxoTangle.SyncData().IsSynced() {
		return
	}
	inp.WasGossiped = true
	c.glb.txGossipOutConsumer.Push(TxGossipSendInputData{
		PrimaryTransactionData: inp,
		ReceivedFrom:           inp.ReceivedFrom,
	})
}
