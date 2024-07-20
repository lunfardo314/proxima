package checkpoints

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.uber.org/zap"
)

// utility for debugging hanging loops

type (
	check struct {
		nextExpectedAfter time.Duration
		name              string
		nowis             time.Time
	}
	Checkpoints struct {
		ch  chan *check
		m   map[string]time.Time
		ctx context.Context
		log *zap.SugaredLogger
	}
)

func New(ctx context.Context, log ...*zap.SugaredLogger) *Checkpoints {
	ret := &Checkpoints{
		ch:  make(chan *check),
		m:   make(map[string]time.Time),
		ctx: ctx,
	}
	if len(log) > 0 {
		ret.log = log[0]
	}
	go ret.loop()
	return ret
}

func (c *Checkpoints) Check(name string, nextExpectedAfter ...time.Duration) {
	var next time.Duration
	if len(nextExpectedAfter) > 0 {
		next = nextExpectedAfter[0]
	}
	c.ch <- &check{
		nextExpectedAfter: next,
		name:              name,
		nowis:             time.Now(),
	}
}

func (c *Checkpoints) Close() {
	close(c.ch)
}

func (c *Checkpoints) loop() {
	for {
		select {
		case <-c.ctx.Done():
			return

		case checkData := <-c.ch:
			if checkData == nil {
				return
			}
			if checkData.nextExpectedAfter == 0 {
				delete(c.m, checkData.name)
				continue
			}
			deadline, ok := c.m[checkData.name]
			if ok {
				if deadline.Before(checkData.nowis) {
					c.fail(checkData.name)
				}
			}
			c.m[checkData.name] = checkData.nowis.Add(checkData.nextExpectedAfter)

		case <-time.After(100 * time.Millisecond):
			for name, d := range c.m {
				if d.Before(time.Now()) {
					c.fail(name)
				}
			}
		}
	}
}

func (c *Checkpoints) fail(name string) {
	msg := fmt.Sprintf("checkpoint '%s' failed", name)
	if c.log != nil {
		log.Fatal(msg)
	} else {
		panic(msg)
	}
}
