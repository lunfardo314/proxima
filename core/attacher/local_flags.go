package attacher

import "github.com/lunfardo314/proxima/core/vertex"

// Attacher keeps list of vertices which are important in order to determine if the sequencer transactions is valid.
// The vertices of consideration are all vertices in the past cone back to the 'rooted' ones, i.e. those which belong
// to the baseline state.
// each vertex in the attacher has local flags, which defines its status in the scope of the attacher.
// The goal of the attacher is to make all vertices marked as 'defined', i.e. either 'rooted' or with its past cone checked
// and valid
// Flags (except 'asked for poke') become final and immutable after they are set 'ON'

const (
	flagAttachedVertexKnown             = Flags(0b00000001) // each vertex of consideration has this flag on
	flagAttachedVertexDefined           = Flags(0b00000010) // means vertex is 'defined', i.e. its validity is checked
	flagAttachedVertexCheckedIfRooted   = Flags(0b00000100) // means vertex has been checked if it is rooted (it may or may not be rooted)
	flagAttachedVertexEndorsementsSolid = Flags(0b00001000) // means all endorsements were validated
	flagAttachedVertexInputsSolid       = Flags(0b00010000) // means all consumed inputs are checked and valid
	flagAttachedVertexAskedForPoke      = Flags(0b00100000) //
)

func (a *attacher) flags(vid *vertex.WrappedTx) Flags {
	return a.vertices[vid]
}

func (a *attacher) setFlagsUp(vid *vertex.WrappedTx, f Flags) {
	flags := a.flags(vid) | f
	a.vertices[vid] = flags
	a.Assertf(flags.FlagsUp(flagAttachedVertexKnown) && !flags.FlagsUp(flagAttachedVertexDefined), "flags.FlagsUp(FlagKnown) && !flags.FlagsUp(FlagDefined)")
}

func (a *attacher) markVertexDefined(vid *vertex.WrappedTx) {
	flags := a.flags(vid)
	a.Assertf(flags.FlagsUp(flagAttachedVertexCheckedIfRooted), "flags.FlagsUp(flagAttachedVertexCheckedIfRooted): %s", vid.IDShortString)

	if a.isKnownRooted(vid) {
		a.Assertf(!flags.FlagsUp(flagAttachedVertexInputsSolid), "!flags.FlagsUp(flagAttachedVertexInputsSolid): %s", vid.IDShortString)
		a.Assertf(!flags.FlagsUp(flagAttachedVertexEndorsementsSolid), "!flags.FlagsUp(flagAttachedVertexInputsSolid): %s", vid.IDShortString)
	} else {
		a.Assertf(flags.FlagsUp(flagAttachedVertexInputsSolid), "flags.FlagsUp(flagAttachedVertexInputsSolid): %s", vid.IDShortString)
		a.Assertf(flags.FlagsUp(flagAttachedVertexEndorsementsSolid), "flags.FlagsUp(flagAttachedVertexInputsSolid)L %s", vid.IDShortString)
	}
	a.referenced.mustReference(vid)
	a.vertices[vid] = a.flags(vid) | flagAttachedVertexKnown | flagAttachedVertexDefined

	a.Tracef(TraceTagMarkDefUndef, "markVertexDefined in %s: %s is DEFINED", a.name, vid.IDShortString)
}

// markVertexUndefined vertex becomes 'known' but undefined
func (a *attacher) markVertexUndefined(vid *vertex.WrappedTx) bool {
	if !a.referenced.reference(vid) {
		return false
	}
	f := a.flags(vid)
	a.Assertf(!f.FlagsUp(flagAttachedVertexDefined), "!f.FlagsUp(FlagDefined)")
	a.vertices[vid] = f | flagAttachedVertexKnown

	a.Tracef(TraceTagMarkDefUndef, "markVertexUndefined in %s: %s is UNDEFINED", a.name, vid.IDShortString)
	return true
}

// mustMarkVertexRooted vertex becomes 'known' and marked rooted and 'defined'
func (a *attacher) mustMarkVertexRooted(vid *vertex.WrappedTx) {
	a.referenced.mustReference(vid)
	a.vertices[vid] = a.flags(vid) | flagAttachedVertexKnown | flagAttachedVertexCheckedIfRooted | flagAttachedVertexDefined
	// creates entry in rooted, probably empty, i.e. with or without output indices
	a.rooted[vid] = a.rooted[vid]
	a.Assertf(a.isKnownRooted(vid), "a.isKnownNotRooted(vid)")
}

// mustMarkVertexNotRooted is marked definitely not rooted
func (a *attacher) mustMarkVertexNotRooted(vid *vertex.WrappedTx) {
	a.referenced.mustReference(vid)
	f := a.flags(vid)
	a.vertices[vid] = f | flagAttachedVertexKnown | flagAttachedVertexCheckedIfRooted
	a.Assertf(a.isKnownNotRooted(vid), "a.isKnownNotRooted(vid)")
}

func (a *attacher) isKnown(vid *vertex.WrappedTx) bool {
	return a.flags(vid).FlagsUp(flagAttachedVertexKnown)
}

func (a *attacher) isKnownDefined(vid *vertex.WrappedTx) bool {
	return a.flags(vid).FlagsUp(flagAttachedVertexKnown | flagAttachedVertexDefined)
}

func (a *attacher) isKnownUndefined(vid *vertex.WrappedTx) bool {
	f := a.flags(vid)
	if !f.FlagsUp(flagAttachedVertexKnown) {
		return false
	}
	return !f.FlagsUp(flagAttachedVertexDefined)
}

// isKnownNotRooted is definitely known it is not rooted
func (a *attacher) isKnownNotRooted(vid *vertex.WrappedTx) bool {
	if !a.flags(vid).FlagsUp(flagAttachedVertexCheckedIfRooted) {
		// not checked yet
		return false
	}
	// it was checked already
	known := a.isKnownDefined(vid) || a.isKnownUndefined(vid)
	_, rooted := a.rooted[vid]
	return known && !rooted
}

func (a *attacher) isKnownRooted(vid *vertex.WrappedTx) (yes bool) {
	if !a.flags(vid).FlagsUp(flagAttachedVertexCheckedIfRooted) {
		// not checked yet
		return false
	}
	_, yes = a.rooted[vid]
	a.Assertf(!yes || a.isKnownDefined(vid) || a.isKnownUndefined(vid), "!yes || a.isKnownDefined(vid) || a.isKnownUndefined(vid)")
	return
}
