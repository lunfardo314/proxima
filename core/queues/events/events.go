package events

import (
	"context"
	"sync"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/queue"
	"go.uber.org/zap/zapcore"
)

type (
	Input struct {
		cmdCode   byte
		eventCode eventtype.EventCode
		arg       any
	}

	Events struct {
		*queue.Queue[Input]
		eventHandlers map[eventtype.EventCode][]func(any)
	}
)

const (
	cmdCodeAddHandler = byte(iota)
	cmdCodePostEvent
)
const chanBufferSize = 10

func New(lvl zapcore.Level) *Events {
	return &Events{
		Queue:         queue.NewQueueWithBufferSize[Input]("events", chanBufferSize, lvl, nil),
		eventHandlers: make(map[eventtype.EventCode][]func(any)),
	}
}

func (c *Events) Start(ctx context.Context, doneOnClose *sync.WaitGroup) {
	c.AddOnClosed(func() {
		doneOnClose.Done()
	})
	c.Queue.Start(c, ctx)
}

func (c *Events) Consume(inp Input) {
	switch inp.cmdCode {
	case cmdCodeAddHandler:
		handlers := c.eventHandlers[inp.eventCode]
		if len(handlers) == 0 {
			handlers = []func(any){inp.arg.(func(any))}
		} else {
			handlers = append(handlers, inp.arg.(func(any)))
		}
		c.eventHandlers[inp.eventCode] = handlers
		c.Log().Debugf("added event handler for event code '%s'", inp.eventCode.String())
	case cmdCodePostEvent:
		for _, fun := range c.eventHandlers[inp.eventCode] {
			fun(inp.arg)
		}
	}
}

// OnEvent is async
func (c *Events) OnEvent(eventCode eventtype.EventCode, fun any) {
	handler, err := eventtype.MakeHandler(eventCode, fun)
	util.AssertNoError(err)
	c.Queue.Push(Input{
		cmdCode:   cmdCodeAddHandler,
		eventCode: eventCode,
		arg:       handler,
	})
}

func (c *Events) PostEvent(eventCode eventtype.EventCode, arg any) {
	c.Queue.Push(Input{
		cmdCode:   cmdCodePostEvent,
		eventCode: eventCode,
		arg:       arg,
	})
}
