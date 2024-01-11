package events

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/lunfardo314/proxima/util/eventtype"
)

func TestEvents(t *testing.T) {
	e := Start(context.Background())
	EventTypeTestString := eventtype.RegisterNew[string]("a string event")
	EventTypeTestInt := eventtype.RegisterNew[int]("an int event")

	var wg sync.WaitGroup
	wg.Add(2)
	e.OnEvent(EventTypeTestString, func(arg string) {
		fmt.Printf("event string -> %s\n", arg)
		wg.Done()
	})
	e.OnEvent(EventTypeTestInt, func(arg int) {
		fmt.Printf("event int -> %d\n", arg)
		wg.Done()
	})

	e.PostEvent(EventTypeTestString, "kuku")
	e.PostEvent(EventTypeTestInt, 31415)
	wg.Wait()
}
