package events

import (
	"context"
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/consumer"
	"github.com/lunfardo314/proxima/util/eventtype"
	"go.uber.org/zap"
)

type (
	InputData struct {
		cmdCode   byte
		eventCode eventtype.EventCode
		arg       any
	}

	Events struct {
		*consumer.Consumer[InputData]
		eventHandlers map[eventtype.EventCode][]func(any)
	}
)

const (
	cmdCodeAddHandler = byte(iota)
	cmdCodePostEvent
)
const chanBufferSize = 10

func Start(ctx context.Context) *Events {
	ret := &Events{
		Consumer:      consumer.NewConsumerWithBufferSize[InputData]("events", chanBufferSize, zap.InfoLevel, nil),
		eventHandlers: make(map[eventtype.EventCode][]func(any)),
	}
	ret.AddOnConsume(ret.consume)
	go func() {
		ret.Log().Infof("starting..")
		ret.Run()
	}()

	go func() {
		<-ctx.Done()
		ret.Consumer.Stop()
	}()
	return ret
}

func (c *Events) consume(inp InputData) {
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
func (c *Events) OnEvent(eventCode eventtype.EventCode, fun any) {
	handler, err := eventtype.MakeHandler(eventCode, fun)
	util.AssertNoError(err)
	c.Consumer.Push(InputData{
		cmdCode:   cmdCodeAddHandler,
		eventCode: eventCode,
		arg:       handler,
	})
}

func (c *Events) PostEvent(eventCode eventtype.EventCode, arg any) {
	c.Consumer.Push(InputData{
		cmdCode:   cmdCodePostEvent,
		eventCode: eventCode,
		arg:       arg,
	})
}
