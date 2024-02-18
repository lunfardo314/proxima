package events

import (
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Input struct {
		cmdCode   byte
		eventCode eventtype.EventCode
		arg       any
	}

	Environment interface {
		global.NodeGlobal
	}

	Events struct {
		Environment
		*queue.Queue[Input]
		eventHandlers map[eventtype.EventCode][]func(any)
	}
)

const (
	cmdCodeAddHandler = byte(iota)
	cmdCodePostEvent
)
const chanBufferSize = 10

const (
	Name     = "events"
	TraceTag = Name
)

func New(env Environment) *Events {
	return &Events{
		Environment:   env,
		Queue:         queue.NewQueueWithBufferSize[Input](Name, chanBufferSize, env.Log().Level(), nil),
		eventHandlers: make(map[eventtype.EventCode][]func(any)),
	}
}

func (d *Events) Start() {
	d.MarkWorkProcessStarted(Name)
	d.AddOnClosed(func() {
		d.MarkWorkProcessStopped(Name)
	})
	d.Queue.Start(d, d.Environment.Ctx())
}

func (d *Events) Consume(inp Input) {
	switch inp.cmdCode {
	case cmdCodeAddHandler:
		handlers := d.eventHandlers[inp.eventCode]
		if len(handlers) == 0 {
			handlers = []func(any){inp.arg.(func(any))}
		} else {
			handlers = append(handlers, inp.arg.(func(any)))
		}
		d.eventHandlers[inp.eventCode] = handlers
		d.Tracef(TraceTag, "added event handler for event code '%s'", inp.eventCode.String())
	case cmdCodePostEvent:
		d.Tracef(TraceTag, "posted event '%s'", inp.eventCode.String())
		for _, fun := range d.eventHandlers[inp.eventCode] {
			fun(inp.arg)
		}
	}
}

// OnEvent is async
func (d *Events) OnEvent(eventCode eventtype.EventCode, fun any) {
	handler, err := eventtype.MakeHandler(eventCode, fun)
	util.AssertNoError(err)
	d.Queue.Push(Input{
		cmdCode:   cmdCodeAddHandler,
		eventCode: eventCode,
		arg:       handler,
	})
}

func (d *Events) PostEvent(eventCode eventtype.EventCode, arg any) {
	d.Queue.Push(Input{
		cmdCode:   cmdCodePostEvent,
		eventCode: eventCode,
		arg:       arg,
	})
}
