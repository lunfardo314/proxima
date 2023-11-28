package consumer

import (
	"sync"

	"github.com/lunfardo314/proxima/general"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Consumer[T any] struct {
	name              string
	que               *Queue[T]
	onConsume         []func(T)
	onClosed          []func()
	emptyAfterCloseWG sync.WaitGroup
	log               *zap.SugaredLogger
	stopOnce          sync.Once
}

func NewConsumer[T any](name string, logLevel zapcore.Level, outputs []string) *Consumer[T] {
	return NewConsumerWithBufferSize[T](name, defaultBufferSize, logLevel, outputs)
}

func NewConsumerWithBufferSize[T any](name string, bufSize int, logLevel zapcore.Level, outputs []string) *Consumer[T] {
	log := general.NewLogger("["+name+"]", logLevel, outputs, "")
	ret := &Consumer[T]{
		name:      name,
		que:       New[T](bufSize),
		log:       log,
		onConsume: make([]func(T), 0),
		onClosed:  make([]func(), 0),
	}
	ret.emptyAfterCloseWG.Add(1)
	ret.que.OnEmptyAfterClose(func() {
		ret.emptyAfterCloseWG.Done()
	})
	return ret
}

func (c *Consumer[T]) Info() (int, int) {
	return c.que.Info()
}

func (c *Consumer[T]) Name() string {
	return c.name
}

func (c *Consumer[T]) Log() *zap.SugaredLogger {
	return c.log
}

func (c *Consumer[T]) AddOnConsume(funs ...func(T)) *Consumer[T] {
	c.onConsume = append(c.onConsume, funs...)
	return c
}

// AddOnClosed specifies functions invoked after the queue is closed and emptied
func (c *Consumer[T]) AddOnClosed(funs ...func()) *Consumer[T] {
	c.onClosed = append(c.onClosed, funs...)
	return c
}

func (c *Consumer[T]) Push(inp T, prio ...bool) {
	c.que.Push(inp, prio...)
}

func (c *Consumer[T]) PushAny(inp any) {
	c.que.PushAny(inp)
}

func (c *Consumer[T]) Run() {
	c.log.Debugf("STARTING [%s]..", c.Log().Level())
	_ = c.log.Sync()
	c.que.Consume(c.onConsume...)
}

func (c *Consumer[T]) Stop() {
	c.stopOnce.Do(func() {
		c.Log().Debugf("STOPPING...")
		c.que.Close()
		c.emptyAfterCloseWG.Wait()
		for _, fun := range c.onClosed {
			fun()
		}
		_ = c.Log().Sync()
	})
}
