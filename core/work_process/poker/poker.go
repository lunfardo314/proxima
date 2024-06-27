package poker

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/depdag"
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
	cleanupLoopPeriod = 1 * time.Second
	ttlWanted         = 5 * time.Minute
	Name              = "poker"
	TraceTag          = Name
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
	go d.cleanupLoop()
}

func (d *Poker) Consume(inp Input) {
	switch inp.Cmd {
	case CommandAdd:
		d.Assertf(inp.Wanted != nil, "inp.Wanted != nil")
		d.Assertf(inp.WhoIsWaiting != nil, "inp.WhoIsWaiting != nil")
		d.addCmd(inp.Wanted, inp.WhoIsWaiting)

	case CommandPokeAll:
		d.Assertf(inp.Wanted != nil, "inp.Wanted != nil")
		d.Assertf(inp.WhoIsWaiting == nil, "inp.WhoIsWaiting == nil")
		d.pokeAllCmd(inp.Wanted)

	case CommandPeriodicCleanup:
		d.Assertf(inp.Wanted == nil, "inp.Wanted == nil")
		d.Assertf(inp.WhoIsWaiting == nil, "inp.WhoIsWaiting == nil")
		d.periodicCleanup()
	}
}

func (d *Poker) addCmd(wanted, whoIsWaiting *vertex.WrappedTx) {
	d.Tracef(TraceTag, "add: %s wants %s", whoIsWaiting.IDShortString, wanted.IDShortString)
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
	d.Tracef(TraceTag, "pokeAllCmd with %s (%d waiting)", wanted.IDShortString, len(lst.waiting))
	if len(lst.waiting) > 0 {
		for _, vid := range lst.waiting {
			d.Tracef(TraceTag, "poke %s with %s", vid.IDShortString, wanted.IDShortString)
			vid.Poke()
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
	d.Tracef(TraceTag, "wanted list size: %d", len(d.m))
}

func (d *Poker) cleanupLoop() {
	for {
		select {
		case <-d.Ctx().Done():
			d.saveDependencyDAG("poker_dependencies")
			return
		case <-time.After(cleanupLoopPeriod):
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

func (d *Poker) saveDependencyDAG(fname string) {
	nodes := make([]depdag.Node, 0, len(d.m))
	for vid, lst := range d.m {
		n := depdag.Node{
			ID:           vid.IDVeryShort(),
			Dependencies: make([]string, 0, len(lst.waiting)),
		}
		for _, vidWaiting := range lst.waiting {
			n.Dependencies = append(n.Dependencies, vidWaiting.IDVeryShort())
		}
		nodes = append(nodes, n)
	}
	depdag.SaveDAG(nodes, fname)
}
