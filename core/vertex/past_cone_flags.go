package vertex

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/lines"
)

// Attacher keeps list of past cone vertices.
// The vertices of consideration are all Vertices in the past cone back to the 'Rooted' ones, i.e. those which belong
// to the baseline state.
// each vertex in the attacher has local flags, which defines its status in the scope of the attacher
// The goal of the attacher is to make all vertices marked as 'defined', i.e. either 'Rooted' or with its past cone checked
// and valid
// Flags (except 'asked for poke') become final and immutable after they are set 'ON'

type (

	// PastConeExt past cone data augmented with logging and flag interpretation
	PastConeExt struct {
		*PastCone
		global.Logging
		name string
	}
)

const (
	FlagAttachedVertexKnown             = FlagsPastCone(0b00000001) // each vertex of consideration has this flag on
	FlagAttachedVertexDefined           = FlagsPastCone(0b00000010) // means vertex is 'defined', i.e. its validity is checked
	FlagAttachedVertexCheckedIfRooted   = FlagsPastCone(0b00000100) // means vertex has been checked if it is Rooted (it may or may not be Rooted)
	FlagAttachedVertexEndorsementsSolid = FlagsPastCone(0b00001000) // means all endorsements were validated
	FlagAttachedVertexInputsSolid       = FlagsPastCone(0b00010000) // means all consumed inputs are checked and valid
	FlagAttachedVertexAskedForPoke      = FlagsPastCone(0b00100000) //
)

func NewPastConeExt(env global.Logging, name string) *PastConeExt {
	return &PastConeExt{
		Logging:  env,
		name:     name,
		PastCone: NewPastCone(),
	}
}

func (f FlagsPastCone) FlagsUp(fl FlagsPastCone) bool {
	return f&fl == fl
}

func (f FlagsPastCone) String() string {
	return fmt.Sprintf("%08b known: %v, defined: %v, checkedRooted: %v, endorsementsOk: %v, inputsOk: %v, asked for poke: %v",
		f,
		f.FlagsUp(FlagAttachedVertexKnown),
		f.FlagsUp(FlagAttachedVertexDefined),
		f.FlagsUp(FlagAttachedVertexCheckedIfRooted),
		f.FlagsUp(FlagAttachedVertexEndorsementsSolid),
		f.FlagsUp(FlagAttachedVertexInputsSolid),
		f.FlagsUp(FlagAttachedVertexAskedForPoke),
	)
}

func (pc *PastConeExt) Flags(vid *WrappedTx) FlagsPastCone {
	return FlagsPastCone(pc.Vertices[vid])
}

func (pc *PastConeExt) SetFlagsUp(vid *WrappedTx, f FlagsPastCone) {
	flags := pc.Flags(vid) | f
	pc.Vertices[vid] = flags
	pc.Assertf(flags.FlagsUp(FlagAttachedVertexKnown) && !flags.FlagsUp(FlagAttachedVertexDefined), "flags.FlagsUp(FlagKnown) && !flags.FlagsUp(FlagDefined)")
}

func (pc *PastConeExt) UndefinedList() []*WrappedTx {
	ret := make([]*WrappedTx, 0)
	for vid, flags := range pc.Vertices {
		if !flags.FlagsUp(FlagAttachedVertexDefined) {
			ret = append(ret, vid)
		}
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (pc *PastConeExt) UndefinedListLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for _, vid := range pc.UndefinedList() {
		ret.Add(vid.IDVeryShort())
	}
	return ret
}

func (pc *PastConeExt) IsRootedOutput(wOut WrappedOutput) bool {
	rootedIndices := pc.Rooted[wOut.VID]
	if len(rootedIndices) == 0 {
		return false
	}
	pc.Assertf(!pc.IsKnownNotRooted(wOut.VID), "!a.IsKnownNotRooted(wOut.VID)")
	return rootedIndices.Contains(wOut.Index)
}

// MarkVertexDefinedDoNotEnforceRootedCheck marks 'defined' without enforcing rooting has been checked
func (pc *PastConeExt) MarkVertexDefinedDoNotEnforceRootedCheck(vid *WrappedTx) {
	flags := pc.Flags(vid)
	if pc.IsKnownRooted(vid) {
		pc.Assertf(!flags.FlagsUp(FlagAttachedVertexInputsSolid), "!flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
		pc.Assertf(!flags.FlagsUp(FlagAttachedVertexEndorsementsSolid), "!flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
	}
	if pc.IsKnownNotRooted(vid) {
		pc.Assertf(flags.FlagsUp(FlagAttachedVertexInputsSolid), "flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
		pc.Assertf(flags.FlagsUp(FlagAttachedVertexEndorsementsSolid), "flags.FlagsUp(FlagAttachedVertexInputsSolid): %s\n     %s", vid.IDShortString, flags.String)
	}
	pc.Vertices[vid] = pc.Flags(vid) | FlagAttachedVertexKnown | FlagAttachedVertexDefined
}

// MarkVertexDefined marks 'defined' and enforces rooting has been checked
func (pc *PastConeExt) MarkVertexDefined(vid *WrappedTx) {
	pc.Assertf(pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedIfRooted), "flags.FlagsUp(FlagAttachedVertexCheckedIfRooted): %s", vid.IDShortString)
	pc.MarkVertexDefinedDoNotEnforceRootedCheck(vid)
}

// MarkVertexUndefined vertex becomes 'known' but undefined
func (pc *PastConeExt) MarkVertexUndefined(vid *WrappedTx) bool {
	f := pc.Flags(vid)
	pc.Assertf(!f.FlagsUp(FlagAttachedVertexDefined), "!f.FlagsUp(FlagDefined)")
	if !f.FlagsUp(FlagAttachedVertexKnown) {
		if !vid.Reference() {
			return false
		}
	}
	pc.Vertices[vid] = f | FlagAttachedVertexKnown
	return true
}

// MustMarkVertexRooted vertex becomes 'known' and marked Rooted and 'defined'
func (pc *PastConeExt) MustMarkVertexRooted(vid *WrappedTx) {
	if !pc.IsKnown(vid) {
		vid.MustReference()
	}
	pc.Vertices[vid] = pc.Flags(vid) | FlagAttachedVertexKnown | FlagAttachedVertexCheckedIfRooted | FlagAttachedVertexDefined
	// creates entry in Rooted, probably empty, i.e. with or without output indices
	pc.Rooted[vid] = pc.Rooted[vid]
	pc.Assertf(pc.IsKnownRooted(vid), "pc.IsKnownNotRooted(vid)")
}

// MustMarkVertexNotRooted is marked definitely not Rooted
func (pc *PastConeExt) MustMarkVertexNotRooted(vid *WrappedTx) {
	if !pc.IsKnown(vid) {
		vid.MustReference()
	}
	f := pc.Flags(vid)
	pc.Vertices[vid] = f | FlagAttachedVertexKnown | FlagAttachedVertexCheckedIfRooted
	pc.Assertf(pc.IsKnownNotRooted(vid), "pc.IsKnownNotRooted(vid)")
}

func (pc *PastConeExt) IsKnown(vid *WrappedTx) bool {
	return pc.Flags(vid).FlagsUp(FlagAttachedVertexKnown)
}

func (pc *PastConeExt) IsKnownDefined(vid *WrappedTx) bool {
	return pc.Flags(vid).FlagsUp(FlagAttachedVertexKnown | FlagAttachedVertexDefined)
}

func (pc *PastConeExt) IsKnownUndefined(vid *WrappedTx) bool {
	f := pc.Flags(vid)
	if !f.FlagsUp(FlagAttachedVertexKnown) {
		return false
	}
	return !f.FlagsUp(FlagAttachedVertexDefined)
}

// IsKnownNotRooted is definitely known it is not Rooted
func (pc *PastConeExt) IsKnownNotRooted(vid *WrappedTx) bool {
	if !pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedIfRooted) {
		// not checked yet
		return false
	}
	// it was checked already
	known := pc.IsKnownDefined(vid) || pc.IsKnownUndefined(vid)
	_, rooted := pc.Rooted[vid]
	return known && !rooted
}

func (pc *PastConeExt) IsKnownRooted(vid *WrappedTx) (yes bool) {
	if !pc.Flags(vid).FlagsUp(FlagAttachedVertexCheckedIfRooted) {
		// not checked yet
		return false
	}
	_, yes = pc.Rooted[vid]
	pc.Assertf(!yes || pc.IsKnownDefined(vid) || pc.IsKnownUndefined(vid), "!yes || pc.IsKnownDefined(vid) || pc.IsKnownUndefined(vid)")
	return
}

func (pc *PastConeExt) ContainsUndefinedExcept(except *WrappedTx) bool {
	for vid, flags := range pc.Vertices {
		if !FlagsPastCone(flags).FlagsUp(FlagAttachedVertexDefined) && vid != except {
			return true
		}
	}
	return false
}

func (pc *PastConeExt) CheckPastCone(rootVid *WrappedTx) (err error) {
	if pc.ContainsUndefinedExcept(rootVid) {
		return fmt.Errorf("still contains undefined Vertices")
	}

	// should be at least one Rooted output ( ledger baselineCoverage must be > 0)
	if len(pc.Rooted) == 0 {
		return fmt.Errorf("at least one Rooted output is expected")
	}
	for vid := range pc.Rooted {
		if !pc.IsKnownDefined(vid) {
			return fmt.Errorf("all Rooted must be defined. This one is not: %s", vid.IDShortString())
		}
	}
	if len(pc.Vertices) == 0 {
		return fmt.Errorf("'vertices' is empty")
	}
	sumRooted := uint64(0)
	for vid, consumed := range pc.Rooted {
		var o *ledger.Output
		consumed.ForEach(func(idx byte) bool {
			o, err = vid.OutputAt(idx)
			if err != nil {
				return false
			}
			sumRooted += o.Amount()
			return true
		})
	}
	if err != nil {
		return
	}
	if sumRooted == 0 {
		err = fmt.Errorf("sum of Rooted cannot be 0")
		return
	}
	for vid, flags := range pc.Vertices {
		if !flags.FlagsUp(FlagAttachedVertexKnown) {
			return fmt.Errorf("wrong flags 1 %08b in %s", flags, vid.IDShortString())
		}
		if !flags.FlagsUp(FlagAttachedVertexDefined) && vid != rootVid {
			return fmt.Errorf("wrong flags 2 %08b in %s", flags, vid.IDShortString())
		}
		if vid == rootVid {
			continue
		}
		status := vid.GetTxStatus()
		if status == Bad {
			return fmt.Errorf("BAD vertex in the past cone: %s", vid.IDShortString())
		}
		// transaction can be undefined in the past cone (virtual, non-sequencer etc)

		if pc.IsKnownRooted(vid) {
			// do not check dependencies if transaction is Rooted
			continue
		}
		vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			missingInputs, missingEndorsements := v.NumMissingInputs()
			if missingInputs+missingEndorsements > 0 {
				err = fmt.Errorf("not all dependencies solid in %s\n      missing inputs: %d\n      missing endorsements: %d,\n      missing input txs: [%s]",
					vid.IDShortString(), missingInputs, missingEndorsements, v.MissingInputTxIDString())
			}
		}})
		if err != nil {
			return
		}
	}
	return nil
}
