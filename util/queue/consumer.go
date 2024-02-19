package queue

import (
	"context"
	"sync"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	Queue[T any] struct {
		name      string
		que       *VarBuffered[T]
		onConsume []func(T)
		onClosed  []func()
		log       *zap.SugaredLogger
		stopOnce  sync.Once
	}

	Consumer[T any] interface {
		Consume(inp T)
	}
)

func NewQueue[T any](name string, logLevel zapcore.Level, outputs []string) *Queue[T] {
	return NewQueueWithBufferSize[T](name, defaultBufferSize, logLevel, outputs)
}

func NewQueueWithBufferSize[T any](name string, bufSize int, logLevel zapcore.Level, outputs []string) *Queue[T] {
	log := global.NewLogger("["+name+"]", logLevel, outputs, "")
	ret := &Queue[T]{
		name:      name,
		que:       New[T](bufSize),
		log:       log,
		onConsume: make([]func(T), 0),
		onClosed:  make([]func(), 0),
	}
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

func (c *Queue[T]) Run() {
	c.que.Consume(c.onConsume...)
}

func (c *Queue[T]) Stop() {
	c.stopOnce.Do(func() {
		c.Log().Debugf("STOPPING...")
		c.que.Close()
		for _, fun := range c.onClosed {
			fun()
		}
		_ = c.Log().Sync()
	})
}

func (c *Queue[T]) Start(consumer Consumer[T], ctx context.Context) {
	c.AddOnConsume(consumer.Consume)
	c.log.Debugf("STARTING [%s]..", c.Log().Level())
	_ = c.log.Sync()

	util.RunWrappedRoutine(c.Name(), func() {
		c.Run()
	}, func(err error) bool {
		c.log.Fatalf("uncaught exception in '%s': '%v'", c.Name(), err)
		return false
	})

	go func() {
		<-ctx.Done()
		c.Log().Debugf("STOPPING...")
		c.que.Close()
		for _, fun := range c.onClosed {
			fun()
		}
		_ = c.Log().Sync()
	}()
}
