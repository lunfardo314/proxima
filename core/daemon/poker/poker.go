package poker

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Input struct {
		Wanted       *vertex.WrappedTx
		WhoIsWaiting *vertex.WrappedTx
		Cmd          Command
	}

	Environment interface {
		global.NodeGlobal
	}

	Poker struct {
		*queue.Queue[Input]
		Environment
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
	Name       = "poker"
	TraceTag   = Name
)

func New(env Environment) *Poker {
	return &Poker{
		Queue:       queue.NewQueueWithBufferSize[Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment: env,
		m:           make(map[*vertex.WrappedTx]waitingList),
	}
}

func (d *Poker) Start() {
	d.MarkWorkProcessStarted(Name)
	d.AddOnClosed(func() {
		d.MarkWorkProcessStopped(Name)
	})
	d.Queue.Start(d, d.Ctx())
	go d.periodicLoop()
}

func (d *Poker) Consume(inp Input) {
	switch inp.Cmd {
	case CommandAdd:
		util.Assertf(inp.Wanted != nil, "inp.Wanted != nil")
		util.Assertf(inp.WhoIsWaiting != nil, "inp.WhoIsWaiting != nil")
		d.addCmd(inp.Wanted, inp.WhoIsWaiting)

	case CommandPokeAll:
		util.Assertf(inp.Wanted != nil, "inp.Wanted != nil")
		util.Assertf(inp.WhoIsWaiting == nil, "inp.WhoIsWaiting == nil")
		d.pokeAllCmd(inp.Wanted)

	case CommandPeriodicCleanup:
		util.Assertf(inp.Wanted == nil, "inp.Wanted == nil")
		util.Assertf(inp.WhoIsWaiting == nil, "inp.WhoIsWaiting == nil")
		d.periodicCleanup()
	}
}

func (d *Poker) addCmd(wanted, whoIsWaiting *vertex.WrappedTx) {
	lst := d.m[wanted]
	if len(lst.waiting) == 0 {
		lst.waiting = []*vertex.WrappedTx{whoIsWaiting}
	} else {
		lst.waiting = util.AppendUnique(lst.waiting, whoIsWaiting)
	}
	lst.keepUntil = time.Now().Add(ttlWanted)
	d.m[wanted] = lst
}

func (d *Poker) pokeAllCmd(wanted *vertex.WrappedTx) {
	lst := d.m[wanted]
	d.Tracef(TraceTag, "pokeAllCmd with %s (%d waiting)", wanted.IDShortString(), len(lst.waiting))
	if len(lst.waiting) > 0 {
		for _, vid := range lst.waiting {
			d.Tracef(TraceTag, "poke %s with %s", vid.IDShortString(), wanted.IDShortString())
			vid.PokeWith(wanted)
		}
		delete(d.m, wanted)
	}
}

func (d *Poker) periodicCleanup() {
	toDelete := make([]*vertex.WrappedTx, 0)
	nowis := time.Now()
	for wanted, lst := range d.m {
		if nowis.After(lst.keepUntil) {
			toDelete = append(toDelete, wanted)
		}
	}
	for _, vid := range toDelete {
		delete(d.m, vid)
	}
	if len(toDelete) > 0 {
		d.Tracef(TraceTag, "purged %d entries", len(toDelete))
	}
}

func (d *Poker) periodicLoop() {
	for {
		select {
		case <-d.Ctx().Done():
			return
		case <-time.After(loopPeriod):
		}
		d.Push(Input{Cmd: CommandPeriodicCleanup})
	}
}

func (d *Poker) PokeMe(me, waitingFor *vertex.WrappedTx) {
	d.Push(Input{
		Wanted:       waitingFor,
		WhoIsWaiting: me,
		Cmd:          CommandAdd,
	})
}

func (d *Poker) PokeAllWith(vid *vertex.WrappedTx) {
	d.Push(Input{
		Wanted: vid,
		Cmd:    CommandPokeAll,
	})
}
