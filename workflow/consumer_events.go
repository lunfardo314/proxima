package workflow

import (
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/eventtype"
)

const EventsName = "events"

// TODO implement remove handler

type (
	EventsInputData struct {
		EventCode eventtype.EventCode
		Arg       any
	}

	EventsConsumer struct {
		*Consumer[*EventsInputData]
	}
)

func (w *Workflow) initEventsConsumer() {
	c := &EventsConsumer{
		Consumer: NewConsumer[*EventsInputData](EventsName, w),
	}
	c.AddOnConsume(c.consume)
	c.AddOnClosed(func() {
		w.terminateWG.Done()
	})

	w.eventsConsumer = c
}

func (c *EventsConsumer) consume(inp *EventsInputData) {
	c.glb.handlersMutex.RLock()
	defer c.glb.handlersMutex.RUnlock()

	for _, h := range c.glb.eventHandlers[inp.EventCode] {
		h(inp.Arg)
	}
}

func (w *Workflow) PostEvent(code eventtype.EventCode, arg any) {
	util.AssertNoError(eventtype.CheckArgType(code, arg))
	w.eventsConsumer.Push(&EventsInputData{
		EventCode: code,
		Arg:       arg,
	})
}

func (w *Workflow) OnEvent(code eventtype.EventCode, fun any) error {
	h, err := eventtype.MakeHandler(code, fun)
	if err != nil {
		return err
	}

	w.handlersMutex.Lock()
	defer w.handlersMutex.Unlock()

	lst, ok := w.eventHandlers[code]
	if !ok {
		lst = make([]func(any), 0)
	}
	lst = append(lst, h)
	w.eventHandlers[code] = lst
	return nil
}

func (w *Workflow) MustOnEvent(code eventtype.EventCode, fun any) {
	util.AssertNoError(w.OnEvent(code, fun))
}
