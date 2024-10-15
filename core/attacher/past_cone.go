package attacher

import (
	"sort"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	referencedSet struct {
		committed set.Set[*vertex.WrappedTx]
		delta     set.Set[*vertex.WrappedTx]
	}

	flagsPastCone uint8

	PastCone struct {
		Vertices   map[*vertex.WrappedTx]byte
		Rooted     map[*vertex.WrappedTx]set.Set[byte]
		Referenced referencedSet
	}

	_pastCone struct {
		*PastCone
		global.Logging
		name string
	}
)

// Attacher keeps list of Vertices which are important in order to determine if the sequencer transactions is valid.
// The Vertices of consideration are all Vertices in the past cone back to the 'Rooted' ones, i.e. those which belong
// to the baseline state.
// each vertex in the attacher has local flags, which defines its status in the scope of the attacher.
// The goal of the attacher is to make all Vertices marked as 'defined', i.e. either 'Rooted' or with its past cone checked
// and valid
// Flags (except 'asked for poke') become final and immutable after they are set 'ON'

const (
	flagAttachedVertexKnown             = flagsPastCone(0b00000001) // each vertex of consideration has this flag on
	flagAttachedVertexDefined           = flagsPastCone(0b00000010) // means vertex is 'defined', i.e. its validity is checked
	flagAttachedVertexCheckedIfRooted   = flagsPastCone(0b00000100) // means vertex has been checked if it is Rooted (it may or may not be Rooted)
	flagAttachedVertexEndorsementsSolid = flagsPastCone(0b00001000) // means all endorsements were validated
	flagAttachedVertexInputsSolid       = flagsPastCone(0b00010000) // means all consumed inputs are checked and valid
	flagAttachedVertexAskedForPoke      = flagsPastCone(0b00100000) //
)

func newPastCone() *PastCone {
	return &PastCone{
		Vertices:   make(map[*vertex.WrappedTx]byte),
		Rooted:     make(map[*vertex.WrappedTx]set.Set[byte]),
		Referenced: newReferencedSet(),
	}
}

func _newPastCone(env global.Logging, name string) *_pastCone {
	return &_pastCone{
		Logging:  env,
		name:     name,
		PastCone: newPastCone(),
	}
}

func (pc *PastCone) flags(vid *vertex.WrappedTx) flagsPastCone {
	return flagsPastCone(pc.Vertices[vid])
}

func (pc *_pastCone) setFlagsUp(vid *vertex.WrappedTx, f flagsPastCone) {
	flags := pc.flags(vid) | f
	pc.Vertices[vid] = byte(flags)
	pc.Assertf(flags.flagsUp(flagAttachedVertexKnown) && !flags.flagsUp(flagAttachedVertexDefined), "flags.FlagsUp(FlagKnown) && !flags.FlagsUp(FlagDefined)")
}

func (pc *_pastCone) undefinedList() []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)
	for vid, flags := range pc.Vertices {
		if !flagsPastCone(flags).flagsUp(flagAttachedVertexDefined) {
			ret = append(ret, vid)
		}
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (pc *_pastCone) undefinedListLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for _, vid := range pc.undefinedList() {
		ret.Add(vid.IDVeryShort())
	}
	return ret
}

func (pc *_pastCone) isRootedOutput(wOut vertex.WrappedOutput) bool {
	rootedIndices := pc.Rooted[wOut.VID]
	if len(rootedIndices) == 0 {
		return false
	}
	pc.Assertf(!pc.isKnownNotRooted(wOut.VID), "!a.isKnownNotRooted(wOut.VID)")
	return rootedIndices.Contains(wOut.Index)
}

// markVertexDefined marks 'defined' without enforcing rooting has been checked
func (pc *_pastCone) markVertexDefinedDoNotEnforceRootedCheck(vid *vertex.WrappedTx) {
	flags := pc.flags(vid)
	if pc.isKnownRooted(vid) {
		pc.Assertf(!flags.flagsUp(flagAttachedVertexInputsSolid), "!flags.FlagsUp(flagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
		pc.Assertf(!flags.flagsUp(flagAttachedVertexEndorsementsSolid), "!flags.FlagsUp(flagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
	}
	if pc.isKnownNotRooted(vid) {
		pc.Assertf(flags.flagsUp(flagAttachedVertexInputsSolid), "flags.FlagsUp(flagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
		pc.Assertf(flags.flagsUp(flagAttachedVertexEndorsementsSolid), "flags.FlagsUp(flagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
	}
	pc.Referenced.mustReference(vid)
	pc.Vertices[vid] = byte(pc.flags(vid) | flagAttachedVertexKnown | flagAttachedVertexDefined)

	pc.Tracef(TraceTagMarkDefUndef, "markVertexDefinedDoNotEnforceRootedCheck in %s: %s is DEFINED", pc.name, vid.IDShortString)
}

// markVertexDefined marks 'defined' and enforces rooting has been checked
func (pc *_pastCone) markVertexDefined(vid *vertex.WrappedTx) {
	pc.Assertf(pc.flags(vid).flagsUp(flagAttachedVertexCheckedIfRooted), "flags.FlagsUp(flagAttachedVertexCheckedIfRooted): %s", vid.IDShortString)
	pc.markVertexDefinedDoNotEnforceRootedCheck(vid)
}

// markVertexUndefined vertex becomes 'known' but undefined
func (pc *_pastCone) markVertexUndefined(vid *vertex.WrappedTx) bool {
	if !pc.Referenced.reference(vid) {
		return false
	}
	f := pc.flags(vid)
	pc.Assertf(!f.flagsUp(flagAttachedVertexDefined), "!f.FlagsUp(FlagDefined)")
	pc.Vertices[vid] = byte(f | flagAttachedVertexKnown)

	pc.Tracef(TraceTagMarkDefUndef, "markVertexUndefined in %s: %s is UNDEFINED", pc.name, vid.IDShortString)
	return true
}

// mustMarkVertexRooted vertex becomes 'known' and marked Rooted and 'defined'
func (pc *_pastCone) mustMarkVertexRooted(vid *vertex.WrappedTx) {
	pc.Referenced.mustReference(vid)
	pc.Vertices[vid] = byte(pc.flags(vid) | flagAttachedVertexKnown | flagAttachedVertexCheckedIfRooted | flagAttachedVertexDefined)
	// creates entry in Rooted, probably empty, i.e. with or without output indices
	pc.Rooted[vid] = pc.Rooted[vid]
	pc.Assertf(pc.isKnownRooted(vid), "pc.isKnownNotRooted(vid)")
}

// mustMarkVertexNotRooted is marked definitely not Rooted
func (pc *_pastCone) mustMarkVertexNotRooted(vid *vertex.WrappedTx) {
	pc.Referenced.mustReference(vid)
	f := pc.flags(vid)
	pc.Vertices[vid] = byte(f | flagAttachedVertexKnown | flagAttachedVertexCheckedIfRooted)
	pc.Assertf(pc.isKnownNotRooted(vid), "pc.isKnownNotRooted(vid)")
}

func (pc *_pastCone) isKnown(vid *vertex.WrappedTx) bool {
	return pc.flags(vid).flagsUp(flagAttachedVertexKnown)
}

func (pc *_pastCone) isKnownDefined(vid *vertex.WrappedTx) bool {
	return pc.flags(vid).flagsUp(flagAttachedVertexKnown | flagAttachedVertexDefined)
}

func (pc *_pastCone) isKnownUndefined(vid *vertex.WrappedTx) bool {
	f := pc.flags(vid)
	if !f.flagsUp(flagAttachedVertexKnown) {
		return false
	}
	return !f.flagsUp(flagAttachedVertexDefined)
}

// isKnownNotRooted is definitely known it is not Rooted
func (pc *_pastCone) isKnownNotRooted(vid *vertex.WrappedTx) bool {
	if !pc.flags(vid).flagsUp(flagAttachedVertexCheckedIfRooted) {
		// not checked yet
		return false
	}
	// it was checked already
	known := pc.isKnownDefined(vid) || pc.isKnownUndefined(vid)
	_, rooted := pc.Rooted[vid]
	return known && !rooted
}

func (pc *_pastCone) isKnownRooted(vid *vertex.WrappedTx) (yes bool) {
	if !pc.flags(vid).flagsUp(flagAttachedVertexCheckedIfRooted) {
		// not checked yet
		return false
	}
	_, yes = pc.Rooted[vid]
	pc.Assertf(!yes || pc.isKnownDefined(vid) || pc.isKnownUndefined(vid), "!yes || pc.isKnownDefined(vid) || pc.isKnownUndefined(vid)")
	return
}

func newReferencedSet() referencedSet {
	return referencedSet{committed: set.New[*vertex.WrappedTx]()}
}

func (r *referencedSet) beginDelta() {
	util.Assertf(r.delta == nil, "r.delta == nil")
	r.delta = set.New[*vertex.WrappedTx]()
}

func (r *referencedSet) commitDelta() {
	r.delta.ForEach(func(vid *vertex.WrappedTx) bool {
		r.committed.Insert(vid)
		return true
	})
	r.delta = nil
}

func (r *referencedSet) rollbackDelta() {
	r.delta.ForEach(func(vid *vertex.WrappedTx) bool {
		vid.UnReference()
		return true
	})
	r.delta = nil
}

// reference references transaction and ensures it is referenced once or none
func (r *referencedSet) reference(vid *vertex.WrappedTx) bool {
	if r.committed.Contains(vid) {
		return true
	}
	if r.delta != nil && r.delta.Contains(vid) {
		return true
	}
	if !vid.Reference() {
		// failed to reference
		return false
	}
	if r.delta != nil {
		// delta buffer is open
		r.delta.Insert(vid)
	} else {
		r.committed.Insert(vid)
	}
	return true
}

func (r *referencedSet) mustReference(vid *vertex.WrappedTx) {
	util.Assertf(r.reference(vid), "r.reference(vid)")
}

func (r *referencedSet) unReferenceAll() {
	r.rollbackDelta()
	r.committed.ForEach(func(vid *vertex.WrappedTx) bool {
		vid.UnReference()
		return true
	})
	r.committed = set.New[*vertex.WrappedTx]()
}
