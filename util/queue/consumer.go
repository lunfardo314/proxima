package queue

import (
	"sync"

	"github.com/lunfardo314/proxima/global"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Queue[T any] struct {
	name              string
	que               *QueueVariableSize[T]
	onConsume         []func(T)
	onClosed          []func()
	emptyAfterCloseWG sync.WaitGroup
	log               *zap.SugaredLogger
	stopOnce          sync.Once
}

func NewConsumer[T any](name string, logLevel zapcore.Level, outputs []string) *Queue[T] {
	return NewConsumerWithBufferSize[T](name, defaultBufferSize, logLevel, outputs)
}

func NewConsumerWithBufferSize[T any](name string, bufSize int, logLevel zapcore.Level, outputs []string) *Queue[T] {
	log := global.NewLogger("["+name+"]", logLevel, outputs, "")
	ret := &Queue[T]{
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

func (c *Queue[T]) Info() (int, int) {
	return c.que.Info()
}

func (c *Queue[T]) Name() string {
	return c.name
}

func (c *Queue[T]) Log() *zap.SugaredLogger {
	return c.log
}

func (c *Queue[T]) AddOnConsume(funs ...func(T)) *Queue[T] {
	c.onConsume = append(c.onConsume, funs...)
	return c
}

// AddOnClosed specifies functions invoked after the queue is closed and emptied
func (c *Queue[T]) AddOnClosed(funs ...func()) *Queue[T] {
	c.onClosed = append(c.onClosed, funs...)
	return c
}

func (c *Queue[T]) Push(inp T, prio ...bool) {
	c.que.Push(inp, prio...)
}

func (c *Queue[T]) PushAny(inp any) {
	c.que.PushAny(inp)
}

func (c *Queue[T]) Run() {
	c.log.Debugf("STARTING [%s]..", c.Log().Level())
	_ = c.log.Sync()
	c.que.Consume(c.onConsume...)
}

func (c *Queue[T]) Stop() {
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
