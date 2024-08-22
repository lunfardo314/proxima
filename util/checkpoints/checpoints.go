package checkpoints

import (
	"time"
)

// utility for debugging hanging loops

type (
	check struct {
		nextExpectedAfter time.Duration
		name              string
		nowis             time.Time
	}
	Checkpoints struct {
		ch       chan *check
		m        map[string]time.Time
		callback func(name string)
	}
)

func New(callback func(name string)) *Checkpoints {
	ret := &Checkpoints{
		ch:       make(chan *check),
		m:        make(map[string]time.Time),
		callback: callback,
	}
	go ret.loop()
	return ret
}

// Check is nextExpectedAfter == 0 cancels checkpoint
func (c *Checkpoints) Check(name string, nextExpectedAfter time.Duration) {
	c.ch <- &check{
		nextExpectedAfter: nextExpectedAfter,
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
					c.callback(checkData.name)
				}
			}
			c.m[checkData.name] = checkData.nowis.Add(checkData.nextExpectedAfter)

		case <-time.After(100 * time.Millisecond):
			for name, d := range c.m {
				if d.Before(time.Now()) {
					c.callback(name)
				}
			}
		}
	}
}
