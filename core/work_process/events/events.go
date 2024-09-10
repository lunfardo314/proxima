package events

import (
	"github.com/lunfardo314/proxima/core/work_process"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/eventtype"
)

type (
	Input struct {
		cmdCode   byte
		eventCode eventtype.EventCode
		arg       any
	}

	environment interface {
		global.NodeGlobal
	}

	Events struct {
		*work_process.WorkProcess[Input]
		eventHandlers map[eventtype.EventCode][]func(any)
	}
)

const (
	cmdCodeAddHandler = byte(iota)
	cmdCodePostEvent
)

const (
	Name     = "events"
	TraceTag = Name
)

func New(env environment) *Events {
	ret := &Events{
		eventHandlers: make(map[eventtype.EventCode][]func(any)),
	}
	ret.WorkProcess = work_process.New[Input](env, Name, ret.consume)
	ret.WorkProcess.Start()
	return ret
}

func (d *Events) consume(inp Input) {
	switch inp.cmdCode {
	case cmdCodeAddHandler:
		handlers := d.eventHandlers[inp.eventCode]
		if len(handlers) == 0 {
			handlers = []func(any){inp.arg.(func(any))}
		} else {
			handlers = append(handlers, inp.arg.(func(any)))
		}
		d.eventHandlers[inp.eventCode] = handlers
		d.Tracef(TraceTag, "added event handler for event code '%s'", inp.eventCode.String)
	case cmdCodePostEvent:
		d.Tracef(TraceTag, "posted event '%s'", inp.eventCode.String)
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
