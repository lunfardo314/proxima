package events

import (
	"context"
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/queue"
	"go.uber.org/zap"
)

type (
	EventData struct {
		cmdCode   byte
		eventCode eventtype.EventCode
		arg       any
	}

	EventQueue struct {
		*queue.Queue[EventData]
		eventHandlers map[eventtype.EventCode][]func(any)
	}
)

const (
	cmdCodeAddHandler = byte(iota)
	cmdCodePostEvent
)
const chanBufferSize = 10

func Start(ctx context.Context) *EventQueue {
	ret := &EventQueue{
		Queue:         queue.NewConsumerWithBufferSize[EventData]("events", chanBufferSize, zap.InfoLevel, nil),
		eventHandlers: make(map[eventtype.EventCode][]func(any)),
	}
	ret.AddOnConsume(ret.consume)
	go func() {
		ret.Log().Infof("starting..")
		ret.Run()
	}()

	go func() {
		<-ctx.Done()
		ret.Queue.Stop()
	}()
	return ret
}

func (c *EventQueue) consume(inp EventData) {
	switch inp.cmdCode {
	case cmdCodeAddHandler:
		handlers := c.eventHandlers[inp.eventCode]
		if len(handlers) == 0 {
			handlers = []func(any){inp.arg.(func(any))}
		} else {
			handlers = append(handlers, inp.arg.(func(any)))
		}
		c.eventHandlers[inp.eventCode] = handlers
		fmt.Printf("added event handler for %s\n", inp.eventCode.String())
	case cmdCodePostEvent:
		for _, fun := range c.eventHandlers[inp.eventCode] {
			fun(inp.arg)
		}
	}
}

// OnEvent is async
func (c *EventQueue) OnEvent(eventCode eventtype.EventCode, fun any) {
	handler, err := eventtype.MakeHandler(eventCode, fun)
	util.AssertNoError(err)
	c.Queue.Push(EventData{
		cmdCode:   cmdCodeAddHandler,
		eventCode: eventCode,
		arg:       handler,
	})
}

func (c *EventQueue) PostEvent(eventCode eventtype.EventCode, arg any) {
	c.Queue.Push(EventData{
		cmdCode:   cmdCodePostEvent,
		eventCode: eventCode,
		arg:       arg,
	})
}
