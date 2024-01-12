package poker

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
	"go.uber.org/zap/zapcore"
)

type (
	Input struct {
		Wanted       *vertex.WrappedTx
		WhoIsWaiting *vertex.WrappedTx
		Cmd          Command
	}

	Poker struct {
		*queue.Queue[Input]
		m map[*vertex.WrappedTx]waitingList
	}

	waitingList struct {
		waiting   []*vertex.WrappedTx
		keepUntil time.Time
	}

	Command byte
)

const (
	CommandAdd = Command(iota)
	CommandPokeAll
	CommandPeriodicCleanup
)

const chanBufferSize = 10

const (
	loopPeriod = 1 * time.Second
	ttlWanted  = 1 * time.Minute
)

func New(lvl zapcore.Level) *Poker {
	return &Poker{
		Queue: queue.NewQueueWithBufferSize[Input]("poke", chanBufferSize, lvl, nil),
		m:     make(map[*vertex.WrappedTx]waitingList),
	}
}

func (c *Poker) Start(ctx context.Context, doneOnClose *sync.WaitGroup) {
	c.AddOnClosed(func() {
		doneOnClose.Done()
	})
	c.Queue.Start(c, ctx)
	go c.periodicLoop(ctx)
}

func (c *Poker) Consume(inp Input) {
	switch inp.Cmd {
	case CommandAdd:
		util.Assertf(inp.Wanted != nil, "inp.Wanted != nil")
		util.Assertf(inp.WhoIsWaiting != nil, "inp.WhoIsWaiting != nil")
		c.addCmd(inp.Wanted, inp.WhoIsWaiting)

	case CommandPokeAll:
		util.Assertf(inp.Wanted != nil, "inp.Wanted != nil")
		util.Assertf(inp.WhoIsWaiting == nil, "inp.WhoIsWaiting == nil")
		c.pokeAllCmd(inp.Wanted)

	case CommandPeriodicCleanup:
		util.Assertf(inp.Wanted == nil, "inp.Wanted == nil")
		util.Assertf(inp.WhoIsWaiting == nil, "inp.WhoIsWaiting == nil")
		c.periodicCleanup()
	}
}

func (c *Poker) addCmd(wanted, whoIsWaiting *vertex.WrappedTx) {
	lst := c.m[wanted]
	if len(lst.waiting) == 0 {
		lst.waiting = []*vertex.WrappedTx{whoIsWaiting}
	} else {
		lst.waiting = util.AppendUnique(lst.waiting, whoIsWaiting)
	}
	lst.keepUntil = time.Now().Add(ttlWanted)
	c.m[wanted] = lst
}

func (c *Poker) pokeAllCmd(wanted *vertex.WrappedTx) {
	lst := c.m[wanted]
	//c.Log().Infof("TRACE pokeAllCmd with %s (%d waiting)", wanted.IDShortString(), len(lst.waiting))
	if len(lst.waiting) > 0 {
		for _, vid := range lst.waiting {
			//c.Log().Infof("TRACE poke %s with %s", vid.IDShortString(), wanted.IDShortString())
			vid.PokeWith(wanted)
		}
		delete(c.m, wanted)
	}
}

func (c *Poker) periodicCleanup() {
	toDelete := make([]*vertex.WrappedTx, 0)
	nowis := time.Now()
	for wanted, lst := range c.m {
		if nowis.After(lst.keepUntil) {
			toDelete = append(toDelete, wanted)
		}
	}
	for _, vid := range toDelete {
		delete(c.m, vid)
	}
}

func (c *Poker) periodicLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(loopPeriod):
		}
		c.Push(Input{Cmd: CommandPeriodicCleanup})
	}
}

func (c *Poker) PokeMe(me, waitingFor *vertex.WrappedTx) {
	c.Push(Input{
		Wanted:       waitingFor,
		WhoIsWaiting: me,
		Cmd:          CommandAdd,
	})
}

func (c *Poker) PokeAllWith(vid *vertex.WrappedTx) {
	c.Push(Input{
		Wanted: vid,
		Cmd:    CommandPokeAll,
	})
}
