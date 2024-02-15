package events

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util/eventtype"
	"go.uber.org/zap"
)

func TestEvents(t *testing.T) {
	glb := global.New("", zap.DebugLevel, nil)
	glb.EnableTraceTags("events")
	e := New(glb)
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)

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
	cancel()
	glb.Wait()
}
