package eventtype

import (
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/util"
)

type (
	EventCode int

	eventType struct {
		name          string
		checkDataType func(any) bool
		dataTypeName  string
		makeHandler   func(any) (func(any), error)
	}
)

var (
	eventTyperMutex sync.RWMutex
	eventTypes      = make([]eventType, 0)
)

// RegisterNew registers new event type globally. Returns newly registered EventCode
func RegisterNew[T any](name string) EventCode {
	eventTyperMutex.Lock()
	defer eventTyperMutex.Unlock()

	var nullT T

	ret := EventCode(len(eventTypes))
	eventTypes = append(eventTypes, eventType{
		name:         name,
		dataTypeName: fmt.Sprintf("%T", nullT),
		checkDataType: func(arg any) bool {
			_, ok := arg.(T)
			return ok
		},
		makeHandler: func(fun any) (func(any), error) {
			var nullHandler func(T)
			handler, ok := fun.(func(T))
			if !ok {
				return nil, fmt.Errorf("wrong event handler type. Event: %s(%d), expected: %s, got: %T",
					name, ret, fmt.Sprintf("%T", nullHandler), fun)
			}
			return func(arg any) {
				argConv, ok := arg.(T)
				util.Assertf(ok, "wrong argument type of the event: expected '%T', got: '%T'", nullT, arg)
				handler(argConv)
			}, nil
		},
	})
	return ret
}

func MakeHandler(code EventCode, fun any) (func(any), error) {
	eventTyperMutex.RLock()
	defer eventTyperMutex.RUnlock()

	if int(code) >= len(eventTypes) {
		return nil, fmt.Errorf("wrong event code %d", code)
	}
	return eventTypes[code].makeHandler(fun)
}

func CheckArgType(code EventCode, arg any) error {
	if int(code) >= len(eventTypes) {
		return fmt.Errorf("wrong event code %d", code)
	}
	if !eventTypes[code].checkDataType(arg) {
		return fmt.Errorf("wrong argument type for event %s(%d). Expected %s, got %T",
			eventTypes[code].name, code, eventTypes[code].dataTypeName, arg)
	}
	return nil
}

func (c EventCode) String() string {
	eventTyperMutex.RLock()
	defer eventTyperMutex.RUnlock()

	if int(c) >= len(eventTypes) {
		return fmt.Sprintf("wrong(%d)", c)
	}
	return fmt.Sprintf("%s(%d)", eventTypes[c].name, c)
}
