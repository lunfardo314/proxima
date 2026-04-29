package poker

import (
	"time"

	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
)

type (
	Input struct {
		Wanted       *vertex.WrappedTx
		WhoIsWaiting *vertex.WrappedTx
		Cmd          Command
	}

	environment interface {
		global.NodeGlobal
	}

	Poker struct {
		*core_modules.CoreModule[Input]
		environment
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

const (
	cleanupLoopPeriod = 5 * time.Second
	ttlWanted         = time.Minute
	Name              = "poker"
	TraceTag          = Name
)

func New(env environment) *Poker {
	ret := &Poker{
		environment: env,
		m:           make(map[*vertex.WrappedTx]waitingList),
	}
	ret.CoreModule = core_modules.New[Input](env, Name, ret.consume)
	ret.CoreModule.Start()

	env.RepeatInBackground(Name+"_cleanup_loop", cleanupLoopPeriod, func() bool {
		ret.Push(Input{Cmd: CommandPeriodicCleanup}, true)
		return true
	}, true)
	return ret
}

func (d *Poker) consume(inp Input) {
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
	//{ // debug
	//	d.Infof1("[poker] total %d entries", len(d.m))
	//	for vid := range d.m {
	//		if vid.Slot() <= 1 {
	//			d.Log().Infof("[poker] >>>>>>>>>>>>>>> in poker %s", vid.IDShortString())
	//		}
	//	}
	//}

	nowis := time.Now()
	count := 0
	for wanted, lst := range d.m {
		if nowis.After(lst.keepUntil) {
			delete(d.m, wanted)
			count++
		}
	}
	if count > 0 {
		d.LogTopicf("poker", 2, "[poker] purged %d entries, remain %d", count, len(d.m))
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
